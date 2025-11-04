
#!/usr/bin/env bash
#
# cf-lb-sync.sh
#
# Common commands:
#   systemctl start cf-lb-sync.service
#   systemctl stop cf-lb-sync.service
#   systemctl restart cf-lb-sync.service
#   systemctl status cf-lb-sync.service
#   systemctl enable cf-lb-sync.service
#   systemctl disable cf-lb-sync.service
#
# Systemd unit file location:
#   /etc/systemd/system/cf-lb-sync.service

set -euo pipefail

#
# push this file with :
#
# scp cf-lb-sync.sh root@vault:/tmp/cf-lb-sync.sh && \
#   ssh root@vault "pct push 106 /tmp/cf-lb-sync.sh \
#     /usr/local/bin/cf-lb-sync.sh"
#
# DNS Load Balancer Sync Script
# Monitors Cloudflare DNS and syncs changes to local BIND server
# Logs all actions to Logstash for monitoring and alerting

# DNS records to monitor and sync
SOURCE_RECORD="lb-home.goodkind.io"      # Cloudflare source
TARGET_RECORD="home.goodkind.io"         # BIND target
LOGSTASH_HOST="logstash.home.goodkind.io"
LOGSTASH_PORT=5044
TSIG_KEY="/etc/bind/keys/dhcp-update.key"

# Graceful shutdown handler
cleanup() {
    log_to_logstash "info" "shutdown" "$CURRENT_IP" "$CURRENT_IPV6" \
        "Script shutting down" "success"
    exit 0
}

trap cleanup SIGINT SIGTERM

# Send ECS-compliant JSON logs to Logstash via HTTP
# Args: level, action, new_ipv4, new_ipv6, message, outcome,
#       [old_ipv4], [old_ipv6]
log_to_logstash() {
    local level=$1
    local action=$2
    local new_ipv4=$3
    local new_ipv6=$4
    local message=${5:-""}
    local outcome=${6:-"unknown"}
    local old_ipv4=${7:-""}
    local old_ipv6=${8:-""}

    # Build change tracking fields if old values provided
    local change_fields=""
    if [ -n "$old_ipv4" ] || [ -n "$old_ipv6" ]; then
        change_fields=$(cat <<CHANGE
  "old_ipv4": "$old_ipv4",
  "old_ipv6": "$old_ipv6",
  "new_ipv4": "$new_ipv4",
  "new_ipv6": "$new_ipv6",
CHANGE
)
    fi

    # Build flattened JSON log entry
    local json=$(cat <<JSON
{
  "@timestamp": "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)",
  "data_stream": {
    "type": "logs",
    "dataset": "bind.cflbsync",
    "namespace": "default"
  },
  "service_name": "cf-lb-sync",
  "service_type": "dns-monitor",
  "hostname": "$(hostname)",
  "level": "$level",
  "action": "$action",
  "outcome": "$outcome",
$change_fields
  "source_record": "$SOURCE_RECORD",
  "target_record": "$TARGET_RECORD",
  "message": "$message"
}
JSON
)

    # Log to syslog (readable format, under 80 cols)
    # Map level to syslog priority
    local priority="user.info"
    [ "$level" = "error" ] && priority="user.error"
    [ "$level" = "warning" ] && priority="user.warning"
    
    logger -t cf-lb-sync -p "$priority" "$level: $action - $message"
    logger -t cf-lb-sync -p "$priority" "  outcome=$outcome"
    logger -t cf-lb-sync -p "$priority" "  source=$SOURCE_RECORD -> target=$TARGET_RECORD"
    if [ -n "$old_ipv4" ] || [ -n "$old_ipv6" ]; then
        logger -t cf-lb-sync -p "$priority" "  old: A=$old_ipv4 AAAA=$old_ipv6"
    fi
    logger -t cf-lb-sync -p "$priority" "  new: A=$new_ipv4 AAAA=$new_ipv6"

    # Send compacted JSON to Logstash via HTTP
    local curl_output
    local http_code
    curl_output=$(echo "$json" | jq -c '.' 2>/dev/null \
      | curl -s -X POST -H "Content-Type: application/json" \
        -w "\n%{http_code}" \
        -d @- "http://$LOGSTASH_HOST:$LOGSTASH_PORT" 2>&1) || true
    
    http_code=$(echo "$curl_output" | tail -n1)
    if [ -z "$http_code" ] || [ "$http_code" -lt 200 ] \
       || [ "$http_code" -ge 300 ]; then
        logger -t cf-lb-sync -p user.error "error: logstash_send_failed - http_code=$http_code"
    fi
}

# Validate IPv4 address format
is_valid_ipv4() {
    local ip=$1
    [[ $ip =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
    
    local IFS='.'
    local -a octets=($ip)
    for octet in "${octets[@]}"; do
        [[ $octet -le 255 ]] || return 1
    done
    return 0
}

# Validate IPv6 address format
is_valid_ipv6() {
    local ip=$1
    [[ $ip =~ ^([0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}$ ]] || return 1
    return 0
}

# Query Cloudflare for DNS records
query_cloudflare() {
    local record=$1
    local type=$2
    local nameserver=$3
    
    dig +short "$record" "@$nameserver" "$type" | head -1
}

# Query local BIND for DNS records
query_bind() {
    local record=$1
    local type=$2
    
    dig +short "$record" @localhost "$type" | head -1
}

# Query local BIND server for current records
CURRENT_IP=$(query_bind "$TARGET_RECORD" "A")
CURRENT_IPV6=$(query_bind "$TARGET_RECORD" "AAAA")

logger -t cf-lb-sync -p user.info "Starting cf-lb-sync script"
log_to_logstash "info" "startup" "$CURRENT_IP" "$CURRENT_IPV6" \
    "DNS sync service started" "success"

# Main sync loop - runs every 5 seconds
while true; do
    # Query Cloudflare DNS for current A and AAAA records
    NEW_IP=$(query_cloudflare "$SOURCE_RECORD" "A" "1.1.1.1")
    NEW_IPV6=$(query_cloudflare "$SOURCE_RECORD" "AAAA" "2606:4700:4700::1111")

    # If Cloudflare query fails, log error and retry
    if [ -z "$NEW_IP" ] || [ -z "$NEW_IPV6" ]; then
        log_to_logstash "error" "query_failed" "$NEW_IP" "$NEW_IPV6" \
            "Failed to query Cloudflare DNS" "failure"
        sleep 5
        continue
    fi

    # Validate IP addresses to prevent DNS poisoning
    if ! is_valid_ipv4 "$NEW_IP" || ! is_valid_ipv6 "$NEW_IPV6"; then
        log_to_logstash "error" "validation_failed" \
            "$NEW_IP" "$NEW_IPV6" \
            "IP address validation failed" "failure"
        sleep 5
        continue
    fi

    # Compare Cloudflare and BIND records - update if different
    if [ "$NEW_IP" != "$CURRENT_IP" ] \
       || [ "$NEW_IPV6" != "$CURRENT_IPV6" ]; then
        
        # Log the detected change with old/new comparison
        log_to_logstash "info" "record_changed" \
            "$NEW_IP" "$NEW_IPV6" \
            "DNS record change detected" "success" \
            "$CURRENT_IP" "$CURRENT_IPV6"

        # Perform dynamic DNS update to BIND
        # Use TSIG key for authenticated updates (required for DNSSEC)
        set +e
        UPDATE_OUTPUT=$(nsupdate -k "$TSIG_KEY" << NSUPDATE 2>&1
server ::1
zone home.goodkind.io
update delete $TARGET_RECORD A
update delete $TARGET_RECORD AAAA
update add $TARGET_RECORD 5 A $NEW_IP
update add $TARGET_RECORD 5 AAAA $NEW_IPV6
send
NSUPDATE
)
        UPDATE_EXIT_CODE=$?
        set -e

        # Check for REFUSED, NOTAUTH, or other errors in output
        if echo "$UPDATE_OUTPUT" | grep -qi "refused\|notauth\|failed"; then
            UPDATE_EXIT_CODE=1
        fi

        # Log update success or failure with old/new comparison
        if [ $UPDATE_EXIT_CODE -eq 0 ] && ! echo "$UPDATE_OUTPUT" | grep -qi "error\|refused\|failed"; then
            log_to_logstash "info" "ddns_update" \
                "$NEW_IP" "$NEW_IPV6" \
                "BIND DNS records updated successfully" "success" \
                "$CURRENT_IP" "$CURRENT_IPV6"
            CURRENT_IP=$NEW_IP
            CURRENT_IPV6=$NEW_IPV6
        else
            log_to_logstash "error" "ddns_update" \
                "$NEW_IP" "$NEW_IPV6" \
                "BIND DNS update failed: $UPDATE_OUTPUT" "failure" \
                "$CURRENT_IP" "$CURRENT_IPV6"
        fi
    fi

    # Wait 5 seconds before next check
    sleep 5
done
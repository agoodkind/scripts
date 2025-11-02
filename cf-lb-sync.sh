
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
# scp cf-lb-sync.sh root@vault:/tmp/cf-lb-sync.sh && ssh root@vault "pct push 106 /tmp/cf-lb-sync.sh /usr/local/bin/cf-lb-sync.sh"
#
# DNS Load Balancer Sync Script
# Monitors Cloudflare DNS and syncs changes to local BIND server
# Logs all actions to Logstash for monitoring and alerting

# DNS records to monitor and sync
RECORD_NAME="lb-home.goodkind.io"        # Cloudflare source
TARGET_RECORD="home.goodkind.io"         # BIND target
LOGSTASH_HOST="logstash.home.goodkind.io"
LOGSTASH_PORT=5044

# Graceful shutdown handler
cleanup() {
    local details
    details=$(jq -n '{message: "Script shutting down"}')
    log_to_logstash "INFO" "shutdown" "$details"
    exit 0
}

trap cleanup SIGINT SIGTERM

# Send structured JSON logs to Logstash via TCP
# Args: log_level, action, details (JSON object)
log_to_logstash() {
    local level=$1
    local action=$2
    local details=$3

    # Build JSON log entry with service identifier
    local json=$(cat <<JSON
{
  "@timestamp": "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)",
  "service": "cf-lb-sync",
  "host": {
    "name": "$(hostname)"
  },
  "log_type": "json",
  "log_level": "$level",
  "action": "$action",
  "details": $details,
  "data_stream": {
    "type": "logs",
    "dataset": "lbsync",
    "namespace": "bind"
  }
}
JSON
)

    # Send compacted JSON to Logstash via HTTP
    echo "$json" | jq -c '.' 2>/dev/null \
      | curl -s -X POST -H "Content-Type: application/json" \
        -d @- "http://$LOGSTASH_HOST:$LOGSTASH_PORT" \
        > /dev/null 2>&1 || true
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

# Main sync loop - runs every 5 seconds
while true; do
    # Query Cloudflare DNS for current A and AAAA records
    NEW_IP=$(query_cloudflare "$RECORD_NAME" "A" "1.1.1.1")
    NEW_IPV6=$(query_cloudflare "$RECORD_NAME" "AAAA" "2606:4700:4700::1111")

    # If Cloudflare query fails, log error and retry
    if [ -z "$NEW_IP" ] || [ -z "$NEW_IPV6" ]; then
        DETAILS=$(jq -n --arg msg "Cloudflare query failed" \
          '{error: $msg}')
        log_to_logstash "ERROR" "query_cloudflare" "$DETAILS"
        sleep 5
        continue
    fi

    # Validate IP addresses to prevent DNS poisoning
    if ! is_valid_ipv4 "$NEW_IP"; then
        DETAILS=$(jq -n --arg ip "$NEW_IP" \
          '{error: "Invalid IPv4 address", invalid_ip: $ip}')
        log_to_logstash "ERROR" "validation_failed" "$DETAILS"
        sleep 5
        continue
    fi

    if ! is_valid_ipv6 "$NEW_IPV6"; then
        DETAILS=$(jq -n --arg ip "$NEW_IPV6" \
          '{error: "Invalid IPv6 address", invalid_ip: $ip}')
        log_to_logstash "ERROR" "validation_failed" "$DETAILS"
        sleep 5
        continue
    fi

    # Log successful Cloudflare query results
    DETAILS=$(jq -n \
      --arg ip "$NEW_IP" \
      --arg ipv6 "$NEW_IPV6" \
      '{cloudflare_a: $ip, cloudflare_aaaa: $ipv6}')
    log_to_logstash "DEBUG" "query_cloudflare" "$DETAILS"

    # Query local BIND server for current records
    CURRENT_IP=$(query_bind "$TARGET_RECORD" "A")
    CURRENT_IPV6=$(query_bind "$TARGET_RECORD" "AAAA")

    # Log current BIND records
    DETAILS=$(jq -n \
      --arg ip "$CURRENT_IP" \
      --arg ipv6 "$CURRENT_IPV6" \
      '{current_a: $ip, current_aaaa: $ipv6}')
    log_to_logstash "DEBUG" "query_bind" "$DETAILS"

    # Compare Cloudflare and BIND records - update if different
    if [ "$NEW_IP" != "$CURRENT_IP" ] \
       || [ "$NEW_IPV6" != "$CURRENT_IPV6" ]; then
        
        # Log the detected change
        DETAILS=$(jq -n \
          --arg old_a "$CURRENT_IP" \
          --arg new_a "$NEW_IP" \
          --arg old_aaaa "$CURRENT_IPV6" \
          --arg new_aaaa "$NEW_IPV6" \
          '{old_a: $old_a, new_a: $new_a,
            old_aaaa: $old_aaaa, new_aaaa: $new_aaaa}')
        log_to_logstash "WARN" "record_changed" "$DETAILS"

        # Perform dynamic DNS update to BIND
        # Use -l flag for localhost-only mode (no server command needed)
        UPDATE_OUTPUT=$(nsupdate -l << NSUPDATE 2>&1
zone home.goodkind.io
update delete $TARGET_RECORD A
update delete $TARGET_RECORD AAAA
update add $TARGET_RECORD 5 A $NEW_IP
update add $TARGET_RECORD 5 AAAA $NEW_IPV6
send
NSUPDATE
)
        UPDATE_EXIT_CODE=$?

        # Log update success or failure
        if [ $UPDATE_EXIT_CODE -eq 0 ]; then
            DETAILS=$(jq -n \
              --arg a "$NEW_IP" \
              --arg aaaa "$NEW_IPV6" \
              --arg zone "$TARGET_RECORD" \
              '{zone: $zone, updated_a: $a,
                updated_aaaa: $aaaa, ttl: 5}')
            log_to_logstash "SUCCESS" "ddns_update" "$DETAILS"
        else
            DETAILS=$(jq -n --arg err "$UPDATE_OUTPUT" \
              '{error: $err}')
            log_to_logstash "ERROR" "ddns_update" "$DETAILS"
        fi
    else
        # Records match - no update needed
        DETAILS=$(jq -n \
          --arg a "$CURRENT_IP" \
          --arg aaaa "$CURRENT_IPV6" \
          '{no_change_a: $a, no_change_aaaa: $aaaa}')
        log_to_logstash "DEBUG" "no_change" "$DETAILS"
    fi

    # Wait 5 seconds before next check
    sleep 5
done
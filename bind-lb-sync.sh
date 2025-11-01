#!/bin/bash

# DNS Load Balancer Sync Script
# Monitors Cloudflare DNS and syncs changes to local BIND server
# Logs all actions to Logstash for monitoring and alerting

# DNS records to monitor and sync
RECORD_NAME="lb-home.goodkind.io"        # Cloudflare source
TARGET_RECORD="home.goodkind.io"         # BIND target
LOGSTASH_HOST="logstash.home.goodkind.io"
LOGSTASH_PORT=5044

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
  "service": "bind-lb-sync",
  "host": {
    "name": "$(hostname)"
  },
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

# Main sync loop - runs every 5 seconds
while true; do
    # Query Cloudflare DNS for current A and AAAA records
    NEW_IP=$(dig +short "$RECORD_NAME" @1.1.1.1 A | head -1)
    NEW_IPV6=$(dig +short "$RECORD_NAME" \
      @2606:4700:4700::1111 AAAA | head -1)

    # If Cloudflare query fails, log error and retry
    if [ -z "$NEW_IP" ] || [ -z "$NEW_IPV6" ]; then
        DETAILS=$(jq -n --arg msg "Cloudflare query failed" \
          '{error: $msg}')
        log_to_logstash "ERROR" "query_cloudflare" "$DETAILS"
        sleep 5
        continue
    fi

    # Log successful Cloudflare query results
    DETAILS=$(jq -n \
      --arg ip "$NEW_IP" \
      --arg ipv6 "$NEW_IPV6" \
      '{cloudflare_a: $ip, cloudflare_aaaa: $ipv6}')
    log_to_logstash "INFO" "query_cloudflare" "$DETAILS"

    # Query local BIND server for current records
    CURRENT_IP=$(dig +short "$TARGET_RECORD" @localhost A \
      | head -1)
    CURRENT_IPV6=$(dig +short "$TARGET_RECORD" @localhost AAAA \
      | head -1)

    # Log current BIND records
    DETAILS=$(jq -n \
      --arg ip "$CURRENT_IP" \
      --arg ipv6 "$CURRENT_IPV6" \
      '{current_a: $ip, current_aaaa: $ipv6}')
    log_to_logstash "INFO" "query_bind" "$DETAILS"

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
        UPDATE_OUTPUT=$(nsupdate -k /etc/bind/rndc.key \
          << NSUPDATE 2>&1
server 127.0.0.1
zone home.goodkind.io
update delete $TARGET_RECORD A
update delete $TARGET_RECORD AAAA
update add $TARGET_RECORD 300 A $NEW_IP
update add $TARGET_RECORD 300 AAAA $NEW_IPV6
send
NSUPDATE
)

        # Log update success or failure
        if [ $? -eq 0 ]; then
            DETAILS=$(jq -n \
              --arg a "$NEW_IP" \
              --arg aaaa "$NEW_IPV6" \
              --arg zone "$TARGET_RECORD" \
              '{zone: $zone, updated_a: $a,
                updated_aaaa: $aaaa, ttl: 300}')
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
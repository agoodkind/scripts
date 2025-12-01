#!/usr/bin/env bash

# Monitor primary WAN (vmbr0) and failover to backup (enp4s0) if needed
# This script checks connectivity and automatically switches routes when the primary WAN fails

PRIMARY_IF="vmbr0"
BACKUP_IF="enp4s0"
PRIMARY_GW4="10.250.0.1"
PRIMARY_GW6="3d06:bad:b01::1"
TEST_HOST4="8.8.8.8"
TEST_HOST6="2001:4860:4860::8888"
LOG="/var/log/wan-monitor.log"
STATE_FILE="/var/run/wan-monitor.state"
EMAIL_RECIPIENT="alex@goodkind.io"
SEND_EMAIL_SCRIPT="/opt/scripts/send-email"

log() {
    echo "[$(date +"%Y-%m-%d %H:%M:%S")] $1" | tee -a "$LOG"
}

send_email() {
    local subject="$1"
    local message="$2"
    
    if [ -f "$SEND_EMAIL_SCRIPT" ] && [ -x "$SEND_EMAIL_SCRIPT" ]; then
        "$SEND_EMAIL_SCRIPT" -t "$EMAIL_RECIPIENT" -s "$subject" -m "$message" -n "WAN Monitor" 2>&1 | tee -a "$LOG"
    else
        log "Warning: send-email script not found at $SEND_EMAIL_SCRIPT. Email not sent."
        log "Subject: $subject"
        log "Message: $message"
    fi
}

get_current_state() {
    if [ -f "$STATE_FILE" ]; then
        cat "$STATE_FILE"
    else
        echo "unknown"
    fi
}

set_state() {
    echo "$1" > "$STATE_FILE"
}

check_connectivity() {
    local iface=$1
    local gw4=$2
    local test4=$3
    
    if ! ip link show "$iface" | grep -q "state UP"; then
        return 1
    fi
    
    if [ -n "$gw4" ] && ! ping -c 1 -W 1 -I "$iface" "$gw4" >/dev/null 2>&1; then
        return 1
    fi
    
    if [ -n "$test4" ] && ! ping -c 1 -W 2 -I "$iface" "$test4" >/dev/null 2>&1; then
        return 1
    fi
    
    return 0
}

adjust_routes() {
    local use_backup=$1
    local current_state=$(get_current_state)
    
    if [ "$use_backup" = "true" ]; then
        BACKUP_GW4=$(ip route show default dev "$BACKUP_IF" 2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
        if [ -n "$BACKUP_GW4" ]; then
            ip route del default dev "$BACKUP_IF" 2>/dev/null
            ip route add default via "$BACKUP_GW4" dev "$BACKUP_IF" metric 50 2>/dev/null
            log "Switched to backup WAN ($BACKUP_IF)"
            
            if [ "$current_state" != "backup" ]; then
                send_email "WAN Failover: Using Backup Connection" \
                    "Primary WAN ($PRIMARY_IF) is down. Traffic has been switched to backup WAN ($BACKUP_IF).\n\nGateway: $BACKUP_GW4\nTime: $(date)"
                set_state "backup"
            fi
        fi
    else
        ip route del default dev "$PRIMARY_IF" 2>/dev/null
        ip route add default via "$PRIMARY_GW4" dev "$PRIMARY_IF" metric 100 2>/dev/null
        log "Switched back to primary WAN ($PRIMARY_IF)"
        
        if [ "$current_state" != "primary" ]; then
            send_email "WAN Recovery: Primary Connection Restored" \
                "Primary WAN ($PRIMARY_IF) is back online. Traffic has been switched back from backup WAN ($BACKUP_IF).\n\nGateway: $PRIMARY_GW4\nTime: $(date)"
            set_state "primary"
        fi
    fi
}

CURRENT_STATE=$(get_current_state)

if check_connectivity "$PRIMARY_IF" "$PRIMARY_GW4" "$TEST_HOST4"; then
    CURRENT_METRIC=$(ip route show default dev "$PRIMARY_IF" 2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
    if [ "$CURRENT_METRIC" != "100" ]; then
        adjust_routes false
    elif [ "$CURRENT_STATE" != "primary" ] && [ "$CURRENT_STATE" != "unknown" ]; then
        # Already on primary, just update state
        set_state "primary"
    fi
else
    if check_connectivity "$BACKUP_IF" "" "$TEST_HOST4"; then
        CURRENT_METRIC=$(ip route show default dev "$BACKUP_IF" 2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
        if [ "$CURRENT_METRIC" != "50" ]; then
            adjust_routes true
        elif [ "$CURRENT_STATE" != "backup" ] && [ "$CURRENT_STATE" != "unknown" ]; then
            # Already on backup, just update state
            set_state "backup"
        fi
    else
        log "Both WAN interfaces appear to be down"
        if [ "$CURRENT_STATE" != "down" ]; then
            send_email "WAN Alert: All Connections Down" \
                "Both primary WAN ($PRIMARY_IF) and backup WAN ($BACKUP_IF) are down.\n\nNo internet connectivity available.\nTime: $(date)"
            set_state "down"
        fi
    fi
fi


#!/usr/bin/env bash

# Monitor primary WAN (vmbr0) and failover to backup (enp4s0) if needed
# This script checks connectivity and automatically switches routes when
# the primary WAN fails
# Usage: wan-monitor.sh [--dry-run]

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

# Parse arguments
DRY_RUN=false
if [[ "$*" =~ --dry-run ]] || [[ "$*" =~ -d ]]; then
    DRY_RUN=true
    log "=== DRY RUN MODE: No routes will be changed, but emails \
will be sent ==="
fi

log() {
    echo "[$(date +"%Y-%m-%d %H:%M:%S")] $1" | tee -a "$LOG"
}

send_email() {
    local subject="$1"
    local message="$2"
    
    if [ -f "$SEND_EMAIL_SCRIPT" ] && [ -x "$SEND_EMAIL_SCRIPT" ]; then
        "$SEND_EMAIL_SCRIPT" -t "$EMAIL_RECIPIENT" -s "$subject" \
            -m "$message" -n "WAN Monitor" 2>&1 | tee -a "$LOG"
    else
        log "Warning: send-email script not found at \
$SEND_EMAIL_SCRIPT. Email not sent."
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
    local gw6=$3
    local test4=$4
    local test6=$5
    
    if ! ip link show "$iface" | grep -q "state UP"; then
        return 1
    fi
    
    local ipv6_available=false
    local ipv6_ok=false
    local ipv4_ok=false
    
    # IPv6 checks first (priority)
    if ip -6 addr show "$iface" | grep -q "inet6.*scope global"; then
        ipv6_available=true
        
        # Check IPv6 gateway if provided
        if [ -n "$gw6" ]; then
            if ping6 -c 1 -W 1 -I "$iface" "$gw6" >/dev/null 2>&1; then
                ipv6_ok=true
            fi
        else
            # No gateway specified, check external connectivity
            if [ -n "$test6" ] && \
                ping6 -c 1 -W 2 -I "$iface" "$test6" >/dev/null 2>&1; then
                ipv6_ok=true
            fi
        fi
        
        # Also check external IPv6 connectivity if gateway check passed
        if [ "$ipv6_ok" = true ] && [ -n "$test6" ] && [ -n "$gw6" ]; then
            if ! ping6 -c 1 -W 2 -I "$iface" "$test6" \
                >/dev/null 2>&1; then
                ipv6_ok=false
            fi
        fi
    fi
    
    # IPv4 checks (secondary, but still required)
    if [ -n "$gw4" ]; then
        if ping -c 1 -W 1 -I "$iface" "$gw4" >/dev/null 2>&1; then
            ipv4_ok=true
        fi
    fi
    
    if [ "$ipv4_ok" = true ] && [ -n "$test4" ]; then
        if ! ping -c 1 -W 2 -I "$iface" "$test4" >/dev/null 2>&1; then
            ipv4_ok=false
        fi
    fi
    
    # For primary interface: IPv6 is required, IPv4 is also required
    # For backup interface: At least one must work
    if [ "$iface" = "$PRIMARY_IF" ]; then
        # Primary must have both IPv6 and IPv4 working
        if [ "$ipv6_available" = true ] && [ "$ipv6_ok" = true ] && \
            [ "$ipv4_ok" = true ]; then
            return 0
        fi
    else
        # Backup: IPv6 preferred, but IPv4 acceptable if IPv6 not
        # available
        if [ "$ipv6_available" = true ] && [ "$ipv6_ok" = true ]; then
            return 0
        elif [ "$ipv4_ok" = true ]; then
            return 0
        fi
    fi
    
    return 1
}

adjust_routes() {
    local use_backup=$1
    local current_state=$(get_current_state)
    
    if [ "$use_backup" = "true" ]; then
        BACKUP_GW4=$(ip route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
        BACKUP_GW6=$(ip -6 route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
        
        if [ "$DRY_RUN" = true ]; then
            log "[DRY RUN] Would switch to backup WAN ($BACKUP_IF)"
            log "[DRY RUN] IPv4 Gateway: ${BACKUP_GW4:-N/A}"
            log "[DRY RUN] IPv6 Gateway: ${BACKUP_GW6:-N/A}"
        else
            if [ -n "$BACKUP_GW4" ]; then
                ip route del default dev "$BACKUP_IF" 2>/dev/null
                ip route add default via "$BACKUP_GW4" dev "$BACKUP_IF" \
                    metric 50 2>/dev/null
            fi
            
            # IPv6 route adjustment (priority)
            if [ -n "$BACKUP_GW6" ]; then
                ip -6 route del default dev "$BACKUP_IF" 2>/dev/null
                ip -6 route add default via "$BACKUP_GW6" dev \
                    "$BACKUP_IF" metric 50 pref medium 2>/dev/null
            fi
        fi
        
        log "Switched to backup WAN ($BACKUP_IF)"
        
        if [ "$current_state" != "backup" ]; then
            local dry_run_msg=""
            [ "$DRY_RUN" = true ] && \
                dry_run_msg="\n\n[DRY RUN - No routes were actually \
changed]"
            send_email "WAN Failover: Using Backup Connection" \
                "Primary WAN ($PRIMARY_IF) is down. Traffic has been \
switched to backup WAN ($BACKUP_IF).\n\nIPv4 Gateway: \
${BACKUP_GW4:-N/A}\nIPv6 Gateway: ${BACKUP_GW6:-N/A}\nTime: \
$(date)${dry_run_msg}"
            if [ "$DRY_RUN" != true ]; then
                set_state "backup"
            fi
        fi
    else
        if [ "$DRY_RUN" = true ]; then
            log "[DRY RUN] Would switch back to primary WAN \
($PRIMARY_IF)"
            log "[DRY RUN] IPv4 Gateway: $PRIMARY_GW4"
            log "[DRY RUN] IPv6 Gateway: $PRIMARY_GW6"
        else
            # IPv4 route
            ip route del default dev "$PRIMARY_IF" 2>/dev/null
            ip route add default via "$PRIMARY_GW4" dev "$PRIMARY_IF" \
                metric 100 2>/dev/null
            
            # IPv6 route (priority)
            ip -6 route del default dev "$PRIMARY_IF" 2>/dev/null
            ip -6 route add default via "$PRIMARY_GW6" dev \
                "$PRIMARY_IF" metric 100 pref medium 2>/dev/null
        fi
        
        log "Switched back to primary WAN ($PRIMARY_IF)"
        
        if [ "$current_state" != "primary" ]; then
            local dry_run_msg=""
            [ "$DRY_RUN" = true ] && \
                dry_run_msg="\n\n[DRY RUN - No routes were actually \
changed]"
            send_email "WAN Recovery: Primary Connection Restored" \
                "Primary WAN ($PRIMARY_IF) is back online. Traffic has \
been switched back from backup WAN ($BACKUP_IF).\n\nIPv4 Gateway: \
$PRIMARY_GW4\nIPv6 Gateway: $PRIMARY_GW6\nTime: $(date)${dry_run_msg}"
            if [ "$DRY_RUN" != true ]; then
                set_state "primary"
            fi
        fi
    fi
}

CURRENT_STATE=$(get_current_state)

# Check primary WAN (IPv6 first, then IPv4)
if check_connectivity "$PRIMARY_IF" "$PRIMARY_GW4" "$PRIMARY_GW6" \
    "$TEST_HOST4" "$TEST_HOST6"; then
    CURRENT_METRIC=$(ip route show default dev "$PRIMARY_IF" \
        2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
    CURRENT_METRIC6=$(ip -6 route show default dev "$PRIMARY_IF" \
        2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
    
    if [ "$CURRENT_METRIC" != "100" ] || \
        [ "$CURRENT_METRIC6" != "100" ]; then
        adjust_routes false
    elif [ "$CURRENT_STATE" != "primary" ] && \
        [ "$CURRENT_STATE" != "unknown" ]; then
        # Already on primary, just update state
        set_state "primary"
    fi
else
    # Get backup gateway for IPv6
    BACKUP_GW6=$(ip -6 route show default dev "$BACKUP_IF" \
        2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
    
    # Check backup WAN (IPv6 first, then IPv4)
    if check_connectivity "$BACKUP_IF" "" "$BACKUP_GW6" "$TEST_HOST4" \
        "$TEST_HOST6"; then
        CURRENT_METRIC=$(ip route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
        CURRENT_METRIC6=$(ip -6 route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
        
        if [ "$CURRENT_METRIC" != "50" ] || \
            [ "$CURRENT_METRIC6" != "50" ]; then
            adjust_routes true
        elif [ "$CURRENT_STATE" != "backup" ] && \
            [ "$CURRENT_STATE" != "unknown" ]; then
            # Already on backup, just update state
            set_state "backup"
        fi
    else
        log "Both WAN interfaces appear to be down"
        if [ "$CURRENT_STATE" != "down" ]; then
            local dry_run_msg=""
            [ "$DRY_RUN" = true ] && \
                dry_run_msg="\n\n[DRY RUN - No routes were actually \
changed]"
            send_email "WAN Alert: All Connections Down" \
                "Both primary WAN ($PRIMARY_IF) and backup WAN \
($BACKUP_IF) are down.\n\nNo internet connectivity available (IPv6 \
and IPv4).\nTime: $(date)${dry_run_msg}"
            if [ "$DRY_RUN" != true ]; then
                set_state "down"
            fi
        fi
    fi
fi

if [ "$DRY_RUN" = true ]; then
    log "=== DRY RUN COMPLETE ==="
fi

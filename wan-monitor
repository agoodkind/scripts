#!/usr/bin/env bash

# Network Failover Monitor
# Monitor primary interface (vmbr0) and failover to backup (vmbr1) if needed.
# Checks IPv6 connectivity first (priority), then IPv4.
#
# DEPLOYMENT:
#   Target host: vault (Proxmox host)
#   Script location: /opt/scripts/wan-monitor
#   Deploy with: cd configs/proxmox && rake wan_monitor:deploy
#
# RELATED FILES (in configs/proxmox/):
#   systemd/wan-monitor.service         - Systemd service unit (oneshot)
#   systemd/wan-monitor.timer           - Timer (runs every 10s)
#   systemd/wan-monitor-notify.service  - Start/stop notifications
#   dhclient-hooks/vmbr1-metric         - Sets metric 199 on vmbr1 DHCP
#
# ON VAULT:
#   /etc/systemd/system/wan-monitor.service
#   /etc/systemd/system/wan-monitor.timer
#   /etc/systemd/system/wan-monitor-notify.service
#   /etc/dhcp/dhclient-exit-hooks.d/vmbr1-metric
#   /var/log/wan-monitor.log       - Event log (tab-separated, parseable)
#   /var/run/wan-monitor.state     - Current state (primary/backup/down)
#   /var/run/wan-monitor.stats     - Statistics counters
#
# COMMANDS:
#   rake wan_monitor:status  - Check service status
#   rake wan_monitor:logs    - View recent logs

PRIMARY_IF="vmbr0"
BACKUP_IF="vmbr1"
PRIMARY_GW4="10.250.0.1"
PRIMARY_GW6="3d06:bad:b01::1"
TEST_HOST4="8.8.8.8"
TEST_HOST6="2001:4860:4860::8888"
LOG="/var/log/wan-monitor.log"
STATE_FILE="/var/run/wan-monitor.state"
STATS_FILE="/var/run/wan-monitor.stats"
EMAIL_RECIPIENT="alex@goodkind.io"
SEND_EMAIL_SCRIPT="/opt/scripts/send-email"

# Combined log + journal: tab-separated, human readable, parseable
# Format: TIMESTAMP\tEVENT_TYPE\tDETAILS
log_event() {
    local event_type="$1"
    local details="$2"
    printf "%s\t%s\t%s\n" "$(date '+%Y-%m-%d %H:%M:%S')" \
        "$event_type" "$details" >> "$LOG"
}

# Convenience wrapper for simple log messages
log() {
    log_event "log" "$1"
    echo "[$(date +"%Y-%m-%d %H:%M:%S")] $1"
}

# Initialize log file and log script start
init_log() {
    if [ ! -f "$LOG" ]; then
        touch "$LOG"
        log_event "init" "Log file created"
    fi
    log_event "run" "Script started (pid=$$)"
}

# Get a stat value from stats file
get_stat() {
    local key="$1"
    local default="$2"
    local val
    if [ -f "$STATS_FILE" ]; then
        val=$(grep "^$key=" "$STATS_FILE" 2>/dev/null | cut -d= -f2)
        echo "${val:-$default}"
    else
        echo "$default"
    fi
}

# Set a stat value
set_stat() {
    local key="$1"
    local val="$2"
    if [ -f "$STATS_FILE" ]; then
        if grep -q "^$key=" "$STATS_FILE" 2>/dev/null; then
            sed -i "s/^$key=.*/$key=$val/" "$STATS_FILE"
        else
            echo "$key=$val" >> "$STATS_FILE"
        fi
    else
        echo "$key=$val" > "$STATS_FILE"
    fi
}

# Increment a counter stat
inc_stat() {
    local key="$1"
    local cur
    cur=$(get_stat "$key" 0)
    set_stat "$key" $((cur + 1))
}

# Format seconds as human readable duration
format_duration() {
    local secs=$1
    if [ "$secs" -lt 60 ]; then
        echo "${secs}s"
    elif [ "$secs" -lt 3600 ]; then
        echo "$((secs/60))m $((secs%60))s"
    elif [ "$secs" -lt 86400 ]; then
        echo "$((secs/3600))h $((secs%3600/60))m"
    else
        echo "$((secs/86400))d $((secs%86400/3600))h"
    fi
}

# Get stats summary for emails
get_stats_summary() {
    local now state_since duration failovers recoveries outages
    local last_primary last_failover
    now=$(date +%s)
    state_since=$(get_stat "state_since" "$now")
    duration=$((now - state_since))
    failovers=$(get_stat "failovers" 0)
    recoveries=$(get_stat "recoveries" 0)
    outages=$(get_stat "outages" 0)
    last_primary=$(get_stat "last_primary" "never")
    last_failover=$(get_stat "last_failover" "never")
    
    [ "$last_primary" != "never" ] && \
        last_primary=$(date -d "@$last_primary" '+%Y-%m-%d %H:%M' 2>/dev/null || \
        date -r "$last_primary" '+%Y-%m-%d %H:%M' 2>/dev/null || echo "$last_primary")
    [ "$last_failover" != "never" ] && \
        last_failover=$(date -d "@$last_failover" '+%Y-%m-%d %H:%M' 2>/dev/null || \
        date -r "$last_failover" '+%Y-%m-%d %H:%M' 2>/dev/null || echo "$last_failover")
    
    echo "Current state duration: $(format_duration $duration)"
    echo "Total failovers: $failovers"
    echo "Total recoveries: $recoveries"
    echo "Total outages: $outages"
    echo "Last primary: $last_primary"
    echo "Last failover: $last_failover"
}

# Update stats on state change
update_stats_on_change() {
    local new_state="$1"
    local old_state="$2"
    local now
    now=$(date +%s)
    
    set_stat "state_since" "$now"
    set_stat "last_state" "$old_state"
    
    case "$new_state" in
        primary)
            set_stat "last_primary" "$now"
            [ "$old_state" = "backup" ] || [ "$old_state" = "down" ] && \
                inc_stat "recoveries"
            ;;
        backup)
            set_stat "last_failover" "$now"
            [ "$old_state" = "primary" ] && inc_stat "failovers"
            ;;
        down)
            set_stat "last_outage" "$now"
            inc_stat "outages"
            ;;
    esac
}

show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Monitor primary interface interface and automatically failover to backup when
needed. Checks IPv6 connectivity first (priority), then IPv4.

OPTIONS:
    --dry-run, -d    Run in dry-run mode (no route changes, sends emails)
    --test-email, -t Send a test email
    --stats          Show current stats and journal
    --notify-start   Send service started notification (used by systemd)
    --notify-stop    Send service stopped notification (used by systemd)
    --help, -h       Show this help message

EXAMPLES:
    $0                    # Normal operation
    $0 --dry-run          # Test mode, no route changes
    $0 --test-email       # Send test email
    $0 --dry-run -t       # Dry-run with test email

CONFIGURATION:
    Primary interface:   	$PRIMARY_IF ($PRIMARY_GW4 / $PRIMARY_GW6)
    Backup interface:   	$BACKUP_IF
    Log file:     			$LOG
    State file:   			$STATE_FILE
    Email:        			$EMAIL_RECIPIENT
EOF
}

send_email() {
    local subject="$1"
    local message="$2"
    
    if [ -f "$SEND_EMAIL_SCRIPT" ] && [ -x "$SEND_EMAIL_SCRIPT" ]; then
        if "$SEND_EMAIL_SCRIPT" -t "$EMAIL_RECIPIENT" -s "$subject" \
            -m "$message" -n "Network Monitor" 2>&1; then
            log_event "email_sent" "$subject"
        else
            log_event "email_failed" "$subject"
        fi
    else
        log_event "email_failed" "send-email script not found"
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
    local current_state
    current_state=$(get_current_state)
    
    if [ "$use_backup" = "true" ]; then
        BACKUP_GW4=$(ip route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
        BACKUP_GW6=$(ip -6 route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
        
        if [ "$DRY_RUN" = true ]; then
            log "[DRY RUN] Would switch to backup interface ($BACKUP_IF)"
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
        
        log "Switched to backup interface ($BACKUP_IF)"
        log_event "route_adjust" "Switched to backup interface"
        
        if [ "$current_state" != "backup" ]; then
            log_event "state_change" "primary->backup (failover)"
            update_stats_on_change "backup" "$current_state"
            dry_run_msg=""
            [ "$DRY_RUN" = true ] && \
                dry_run_msg="\n\n[DRY RUN - No routes were actually \
changed]"
            send_email "Network Failover: Using Backup Connection" \
                "Primary interface ($PRIMARY_IF) is down. Traffic has been \
switched to backup interface ($BACKUP_IF).\n\nIPv4 Gateway: \
${BACKUP_GW4:-N/A}\nIPv6 Gateway: ${BACKUP_GW6:-N/A}\nTime: \
$(date)${dry_run_msg}\n\n--- Stats ---\n$(get_stats_summary)"
            if [ "$DRY_RUN" != true ]; then
                set_state "backup"
            fi
        fi
    else
        if [ "$DRY_RUN" = true ]; then
            log "[DRY RUN] Would switch back to primary interface \
($PRIMARY_IF)"
            log "[DRY RUN] IPv4 Gateway: $PRIMARY_GW4"
            log "[DRY RUN] IPv6 Gateway: $PRIMARY_GW6"
        else
            # IPv4 route - primary
            ip route del default dev "$PRIMARY_IF" 2>/dev/null
            ip route add default via "$PRIMARY_GW4" dev "$PRIMARY_IF" \
                metric 100 2>/dev/null
            
            # IPv6 route - primary
            ip -6 route del default dev "$PRIMARY_IF" 2>/dev/null
            ip -6 route add default via "$PRIMARY_GW6" dev \
                "$PRIMARY_IF" metric 100 pref medium 2>/dev/null
            
            # Reset backup routes to higher metric
            BACKUP_GW4=$(ip route show default dev "$BACKUP_IF" \
                2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
            BACKUP_GW6=$(ip -6 route show default dev "$BACKUP_IF" \
                2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
            if [ -n "$BACKUP_GW4" ]; then
                ip route del default via "$BACKUP_GW4" dev "$BACKUP_IF" \
                    2>/dev/null
                ip route add default via "$BACKUP_GW4" dev "$BACKUP_IF" \
                    metric 199 2>/dev/null
            fi
            if [ -n "$BACKUP_GW6" ]; then
                ip -6 route del default via "$BACKUP_GW6" dev "$BACKUP_IF" \
                    2>/dev/null
                ip -6 route add default via "$BACKUP_GW6" dev "$BACKUP_IF" \
                    metric 199 pref medium 2>/dev/null
            fi
        fi
        
        log "Switched back to primary interface ($PRIMARY_IF)"
        log_event "route_adjust" "Switched to primary interface"
        
        if [ "$current_state" != "primary" ]; then
            log_event "state_change" "backup->primary (recovery)"
            update_stats_on_change "primary" "$current_state"
            dry_run_msg=""
            [ "$DRY_RUN" = true ] && \
                dry_run_msg="\n\n[DRY RUN - No routes were actually \
changed]"
            send_email "Network Recovery: Primary Connection Restored" \
                "Primary interface ($PRIMARY_IF) is back online. Traffic has \
been switched back from backup interface ($BACKUP_IF).\n\nIPv4 Gateway: \
$PRIMARY_GW4\nIPv6 Gateway: $PRIMARY_GW6\nTime: $(date)${dry_run_msg}\n\n\
--- Stats ---\n$(get_stats_summary)"
            if [ "$DRY_RUN" != true ]; then
                set_state "primary"
            fi
        fi
    fi
}

# Parse arguments
DRY_RUN=false
SEND_TEST_EMAIL=false

for arg in "$@"; do
    case "$arg" in
        --dry-run|-d)
            DRY_RUN=true
            log "=== DRY RUN MODE: No routes will be changed, but \
emails will be sent ==="
            SEND_TEST_EMAIL=true
            ;;
        --test-email|-t)
            SEND_TEST_EMAIL=true
            ;;
        --stats)
            echo "=== Network Monitor Stats ==="
            echo "Current state: $(get_current_state)"
            echo ""
            get_stats_summary
            echo ""
            echo "=== Recent Events (last 20) ==="
            if [ -f "$LOG" ]; then
                tail -20 "$LOG" | while IFS=$'\t' read -r ts ev dt; do
                    printf "%-20s %-15s %s\n" "$ts" "$ev" "$dt"
                done
            else
                echo "(no log entries)"
            fi
            exit 0
            ;;
        --notify-start)
            log "Network Monitor service started"
            log_event "service_start" "Network Monitor service started"
            send_email "Network Monitor Started" \
                "Network Monitor service has been started.\n\nPrimary interface: \
$PRIMARY_IF ($PRIMARY_GW4 / $PRIMARY_GW6)\nBackup interface: $BACKUP_IF\n\
Time: $(date)\n\n--- Stats ---\n$(get_stats_summary)"
            exit 0
            ;;
        --notify-stop)
            log "Network Monitor service stopped"
            log_event "service_stop" "Network Monitor service stopped"
            send_email "Network Monitor Stopped" \
                "Network Monitor service has been stopped.\n\nMonitoring \
is no longer active.\nTime: $(date)\n\n--- Stats ---\n$(get_stats_summary)"
            exit 0
            ;;
        --help|-h)
            show_usage
            exit 0
            ;;
        *)
            echo "Error: Unknown option '$arg'"
            show_usage
            exit 1
            ;;
    esac
done

# Initialize logging
init_log

CURRENT_STATE=$(get_current_state)

# Check primary interface (IPv6 first, then IPv4)
if check_connectivity "$PRIMARY_IF" "$PRIMARY_GW4" "$PRIMARY_GW6" \
    "$TEST_HOST4" "$TEST_HOST6"; then
    CURRENT_METRIC=$(ip route show default dev "$PRIMARY_IF" \
        2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
    CURRENT_METRIC6=$(ip -6 route show default dev "$PRIMARY_IF" \
        2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
    
    if [ "$CURRENT_METRIC" != "100" ] || \
        [ "$CURRENT_METRIC6" != "100" ]; then
        adjust_routes false
    elif [ "$CURRENT_STATE" != "primary" ]; then
        # Routes correct, state changed - notify
        log_event "state_change" "$CURRENT_STATE->primary"
        update_stats_on_change "primary" "$CURRENT_STATE"
        if [ "$CURRENT_STATE" = "unknown" ]; then
            log "Primary interface ($PRIMARY_IF) active (initial)"
            send_email "Network Monitor: Primary Connection Active" \
                "Network Monitor started. Primary interface ($PRIMARY_IF) is \
active.\n\nIPv4 Gateway: $PRIMARY_GW4\nIPv6 Gateway: $PRIMARY_GW6\n\
Time: $(date)\n\n--- Stats ---\n$(get_stats_summary)"
        else
            log "Primary interface ($PRIMARY_IF) recovered"
            send_email "Network Recovery: Primary Connection Restored" \
                "Primary interface ($PRIMARY_IF) is back online.\n\nIPv4 Gateway: \
$PRIMARY_GW4\nIPv6 Gateway: $PRIMARY_GW6\nTime: $(date)\n\n\
--- Stats ---\n$(get_stats_summary)"
        fi
        set_state "primary"
    fi
else
    # Get backup gateway for IPv6
    BACKUP_GW6=$(ip -6 route show default dev "$BACKUP_IF" \
        2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
    
    # Check backup interface (IPv6 first, then IPv4)
    if check_connectivity "$BACKUP_IF" "" "$BACKUP_GW6" "$TEST_HOST4" \
        "$TEST_HOST6"; then
        CURRENT_METRIC=$(ip route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
        CURRENT_METRIC6=$(ip -6 route show default dev "$BACKUP_IF" \
            2>/dev/null | grep -oP "metric \K[0-9]+" || echo "999")
        
        if [ "$CURRENT_METRIC" != "50" ] || \
            [ "$CURRENT_METRIC6" != "50" ]; then
            adjust_routes true
        elif [ "$CURRENT_STATE" != "backup" ]; then
            # Routes already set, state changed - notify
            log_event "state_change" "$CURRENT_STATE->backup"
            update_stats_on_change "backup" "$CURRENT_STATE"
            BACKUP_GW4=$(ip route show default dev "$BACKUP_IF" \
                2>/dev/null | grep -oP "via \K[^ ]+" | head -1)
            if [ "$CURRENT_STATE" = "unknown" ]; then
                log "Backup interface ($BACKUP_IF) active (initial)"
                send_email "Network Monitor: Using Backup Connection" \
                    "Network Monitor started. Primary interface ($PRIMARY_IF) is \
down, using backup interface ($BACKUP_IF).\n\nIPv4 Gateway: ${BACKUP_GW4:-N/A}\n\
IPv6 Gateway: ${BACKUP_GW6:-N/A}\nTime: $(date)\n\n\
--- Stats ---\n$(get_stats_summary)"
            else
                log "Backup interface ($BACKUP_IF) now active"
                send_email "Network Failover: Using Backup Connection" \
                    "Primary interface ($PRIMARY_IF) is down. Backup interface \
($BACKUP_IF) is active.\n\nIPv4 Gateway: ${BACKUP_GW4:-N/A}\nIPv6 \
Gateway: ${BACKUP_GW6:-N/A}\nTime: $(date)\n\n\
--- Stats ---\n$(get_stats_summary)"
            fi
            set_state "backup"
        fi
    else
        log "Both interfaces appear to be down"
        log_event "connectivity_check" "Both interfaces down"
        if [ "$CURRENT_STATE" != "down" ]; then
            log_event "state_change" "$CURRENT_STATE->down"
            update_stats_on_change "down" "$CURRENT_STATE"
            dry_run_msg=""
            [ "$DRY_RUN" = true ] && \
                dry_run_msg="\n\n[DRY RUN - No routes were actually \
changed]"
            send_email "Network Alert: All Connections Down" \
                "Both primary interface ($PRIMARY_IF) and backup interface \
($BACKUP_IF) are down.\n\nNo internet connectivity available (IPv6 \
and IPv4).\nTime: $(date)${dry_run_msg}\n\n\
--- Stats ---\n$(get_stats_summary)"
            if [ "$DRY_RUN" != true ]; then
                set_state "down"
            fi
        fi
    fi
fi

# Send test email in dry-run mode if requested
if [ "$SEND_TEST_EMAIL" = true ] || [ "$DRY_RUN" = true ]; then
    if [ "$SEND_TEST_EMAIL" = true ] || \
        { [ "$CURRENT_STATE" = "primary" ] && \
        [ "$DRY_RUN" = true ]; }; then
        log "Sending test email..."
        send_email "Network Monitor Test Email" \
            "This is a test email from the network monitor script.\n\n\
Current State: $CURRENT_STATE\nPrimary interface: $PRIMARY_IF\nBackup interface: \
$BACKUP_IF\nTime: $(date)\n\n[This is a test - no actual changes \
were made]"
    fi
fi

if [ "$DRY_RUN" = true ]; then
    log "=== DRY RUN COMPLETE ==="
fi

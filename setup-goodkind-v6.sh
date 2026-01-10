#!/bin/bash

# --- CONFIGURATION ---
# Full IP constructed from your prefix + suffix
MY_V6_IP="3d06:bad:b01:240:b518:3b42:c93:f478"
MY_V6_PREFIXLEN="128"

# Route the whole /56 overlay to Proxmox
OVERLAY_NET="3d06:bad:b01:200::/56"

# The DNS for Proxmox WAN IP
GW_DNS="hypervisor6.suburban.goodkind.io"

CACHE_FILE="/var/db/goodkind-v6-gateway-cache"
LOGfile="/var/log/goodkind-v6.log"
EMAIL_RECIPIENT="alex@goodkind.io"
SEND_EMAIL_SCRIPT="/usr/local/bin/send-email"
# ---------------------

log() { echo "$(date): $1" >> "$LOGfile"; }

send_failure_email() {
    local reason="$1"
    if [ -x "$SEND_EMAIL_SCRIPT" ]; then
        "$SEND_EMAIL_SCRIPT" \
            -t "$EMAIL_RECIPIENT" \
            -s "[goodkind-v6] IPv6 setup failed on $(hostname)" \
            -m "$reason

Log file: $LOGfile" \
            -n "Goodkind IPv6" \
            -c "setup-goodkind-v6" \
            2>&1 || true
    fi
}

# 1. Detect Interface (Fast)
IFACE=$(ifconfig | grep -B 5 "inet 10.240" | grep "flags" | awk -F: '{print $1}' | head -1)

if [ -z "$IFACE" ]; then
    log "No 10.240 interface found. Exiting."
    exit 0
fi

# 2. Add the Static Alias (Always Safe, Instant)
log "Adding alias $MY_V6_IP to $IFACE"
ifconfig "$IFACE" inet6 add "$MY_V6_IP/$MY_V6_PREFIXLEN" alias 2>/dev/null

# 3. Apply Cached Gateway (Fast Path)
CACHED_GW=""
if [ -f "$CACHE_FILE" ]; then
    CACHED_GW=$(cat "$CACHE_FILE")
    if [ ! -z "$CACHED_GW" ]; then
        log "Cache hit. Applying cached gateway: $CACHED_GW"
        route add -inet6 "$OVERLAY_NET" "$CACHED_GW" >> "$LOGfile" 2>&1
    fi
fi

# 4. Asynchronous DNS Check & Update (Background)
(
    # Wait for network stack to settle
    sleep 5
    
    MAX_RETRIES=30
    COUNT=0
    NEW_GW=""

    # Retry loop to resolve DNS
    while [ -z "$NEW_GW" ] && [ $COUNT -lt $MAX_RETRIES ]; do
        NEW_GW=$(dig +short AAAA "$GW_DNS" | head -1)
        if [ -z "$NEW_GW" ]; then
            sleep 5
            ((COUNT++))
        fi
    done

    if [ -z "$NEW_GW" ]; then
        msg="Async: Failed to resolve gateway after retries for $GW_DNS. Giving up."
        log "$msg"
        send_failure_email "$msg"
        exit 1
    fi

    # Check if the resolved IP is different from what we cached/applied
    if [ "$NEW_GW" != "$CACHED_GW" ]; then
        log "Async: Gateway changed (Old: $CACHED_GW -> New: $NEW_GW). Updating route..."
        
        # Remove old route (blindly delete to be safe)
        route delete -inet6 "$OVERLAY_NET" >> "$LOGfile" 2>&1 || true
        
        # Add new route
        if ! route add -inet6 "$OVERLAY_NET" "$NEW_GW" >> "$LOGfile" 2>&1; then
            msg="Async: Failed to add IPv6 route $OVERLAY_NET via $NEW_GW"
            log "$msg"
            send_failure_email "$msg"
            exit 1
        fi
        
        # Update Cache
        echo "$NEW_GW" > "$CACHE_FILE"
        log "Async: Cache updated."
    else
        log "Async: Gateway matches cache. No changes needed."
    fi
) &

# 5. Exit Immediately (Unblocks Boot)
exit 0

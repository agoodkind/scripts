#!/usr/bin/env bash
# Shared email formatting functions
# Used by send-email and send-email-http

# System info functions (cross-platform: macOS + Linux)
is_macos() { [[ "$OSTYPE" == "darwin"* ]]; }

get_uptime() {
    if is_macos; then
        local boot_time days hours mins
        boot_time=$(sysctl -n kern.boottime | awk '{print $4}' | tr -d ',')
        local now=$(date +%s)
        local uptime_secs=$((now - boot_time))
        days=$((uptime_secs / 86400))
        hours=$(( (uptime_secs % 86400) / 3600 ))
        mins=$(( (uptime_secs % 3600) / 60 ))
        if ((days > 0)); then
            printf "%d days, %d hours, %d minutes" "$days" "$hours" "$mins"
        elif ((hours > 0)); then
            printf "%d hours, %d minutes" "$hours" "$mins"
        else
            printf "%d minutes" "$mins"
        fi
    else
        uptime -p 2>/dev/null | sed 's/up //' || uptime | awk '{print $3,$4}' | tr -d ','
    fi
}

get_load() {
    if is_macos; then
        sysctl -n vm.loadavg | awk '{print $2, $3, $4}'
    else
        uptime | awk -F'load average:' '{print $2}' | xargs
    fi
}

get_memory() {
    if is_macos; then
        local page_size=$(sysctl -n hw.pagesize)
        local pages_free=$(vm_stat | awk '/Pages free:/{gsub(/\./,"",$3); print $3}')
        local pages_active=$(vm_stat | awk '/Pages active:/{gsub(/\./,"",$3); print $3}')
        local pages_inactive=$(vm_stat | awk '/Pages inactive:/{gsub(/\./,"",$3); print $3}')
        local pages_wired=$(vm_stat | awk '/Pages wired down:/{gsub(/\./,"",$4); print $4}')
        local pages_compressed=$(vm_stat | awk '/Pages stored in compressor:/{gsub(/\./,"",$5); print $5}')
        local total_mem=$(sysctl -n hw.memsize)
        local used=$((( pages_active + pages_wired + pages_compressed ) * page_size))
        local used_gb=$(echo "scale=1; $used / 1073741824" | bc)
        local total_gb=$(echo "scale=1; $total_mem / 1073741824" | bc)
        printf "%sG/%sG" "$used_gb" "$total_gb"
    else
        free -h 2>/dev/null | awk '/Mem:/{printf "%s/%s",$3,$2}'
    fi
}

get_disk() { df -h / | awk 'NR==2{printf "%s/%s (%s)",$3,$2,$5}'; }

# Race multiple URLs in parallel; first to complete wins
fetch_race() {
    local opts=$1
    shift
    local urls=("$@")
    local tmps=() pids=()

    for url in "${urls[@]}"; do
        local t
        t=$(mktemp)
        tmps+=("$t")
        # shellcheck disable=SC2086
        curl $opts -sk --connect-timeout 3 --max-time 5 "$url" >"$t" \
            2>/dev/null &
        pids+=($!)
    done

    while true; do
        local running=0
        for i in "${!pids[@]}"; do
            [[ -z "${pids[$i]}" ]] && continue

            if kill -0 "${pids[$i]}" 2>/dev/null; then
                running=$((running + 1))
            else
                if [[ -s "${tmps[$i]}" ]]; then
                    tr -d '\r\n' <"${tmps[$i]}"
                    for j in "${!pids[@]}"; do
                        [[ -n "${pids[$j]}" ]] && kill "${pids[$j]}" \
                            2>/dev/null || true
                    done
                    rm -f "${tmps[@]}" 2>/dev/null
                    return 0
                fi
                pids[$i]=""
            fi
        done

        [[ $running -eq 0 ]] && break
        sleep 0.01
    done

    rm -f "${tmps[@]}" 2>/dev/null
    echo "N/A"
}

get_public_ip() {
    local ver="$1"
    fetch_race "-$ver" \
        "ifconfig.co" \
        "icanhazip.com" \
        "api.ipify.org" \
        "ifconfig.me"
}

get_isp() {
    fetch_race "" \
        "https://ifconfig.co/asn-org" \
        "https://ipinfo.io/org" \
        "http://ip-api.com/line/?fields=org"
}

get_ips() {
    local ver="$1"
    if is_macos; then
        if [[ "$ver" == "4" ]]; then
            ifconfig 2>/dev/null | awk '
                /^[a-z]/ { iface=$1; gsub(/:$/,"",iface) }
                iface!="lo0" && /inet / && !/127\.0\.0\.1/ {
                    print iface, $2
                }'
        else
            ifconfig 2>/dev/null | awk '
                /^[a-z]/ { iface=$1; gsub(/:$/,"",iface) }
                iface!="lo0" && /inet6 / && !/fe80/ && !/::1/ {
                    gsub(/%.*$/,"",$2); print iface, $2
                }'
        fi
    else
        ip -"$ver" -o addr 2>/dev/null | awk '
            $2!="lo" && $4!~/^fe80/ {gsub(/\/.*/,"",$4); print $2,$4}'
    fi
}

infer_caller() {
    local c
    c=$(ps -p "$PPID" -o comm= 2>/dev/null || true)

    if [[ "$c" =~ ^(bash|sh|zsh)$ ]]; then
        local last_arg
        last_arg=$(ps -p "$PPID" -o args= 2>/dev/null | awk '{print $NF}' || true)
        last_arg="${last_arg#"${last_arg%%[![:space:]]*}"}"
        last_arg="${last_arg%"${last_arg##*[![:space:]]}"}"
        if [ -n "$last_arg" ]; then
            c=$(basename "$last_arg")
        fi
    fi
    echo "${c:-unknown}"
}

# Render plain text email
render_plain() {
    local msg="$1"
    echo -e "$msg"
}

# Render HTML email with metadata footer
render_html() {
    local msg="$1"
    local caller="${2:-unknown}"
    local hostname="${3:-$(hostname)}"
    
    local preheader_text
    preheader_text=$(echo -e "$msg" | tr '\n' ' ')
    local padding=""
    for _ in {1..100}; do padding+="&nbsp;&zwnj;"; done
    
    cat <<EOF
<!DOCTYPE html><html><head><style>
body{font:14px -apple-system,BlinkMacSystemFont,Arial,sans-serif;margin:0}
.meta{margin-top:16px;padding-top:12px;border-top:1px solid rgba(128,128,128,0.2);
font-size:11px;opacity:0.6}
.meta table{border-collapse:collapse}
.meta td{padding:1px 0}
.meta .k{padding-right:12px;opacity:0.7}
</style></head><body>
<div style="display:none;font-size:1px;line-height:1px;max-height:0px;max-width:0px;opacity:0;overflow:hidden;mso-hide:all;font-family:sans-serif;">
${preheader_text}${padding}
</div>
<div>$(echo -e "$msg" | sed 's/$/<br>/')</div>
<div class="meta">
<table>
<tr><td class="k">Caller</td><td>$caller</td></tr>
<tr><td class="k">Time</td><td>$(date +'%Y-%m-%d %H:%M %Z')</td></tr>
<tr><td class="k">Host</td><td>$hostname</td></tr>
<tr><td class="k">Uptime</td><td>$(get_uptime)</td></tr>
<tr><td class="k">Load</td><td>$(get_load)</td></tr>
<tr><td class="k">Memory</td><td>$(get_memory)</td></tr>
<tr><td class="k">Disk</td><td>$(get_disk)</td></tr>
<tr><td class="k">Public IPv4</td><td>$(get_public_ip 4)</td></tr>
<tr><td class="k">Public IPv6</td><td>$(get_public_ip 6)</td></tr>
<tr><td class="k">ISP</td><td>$(get_isp)</td></tr>
$(get_ips 4 | while read -r i ip; do echo "<tr><td class=\"k\">$i</td><td>$ip</td></tr>"; done)
$(get_ips 6 | while read -r i ip; do echo "<tr><td class=\"k\">$i</td><td>$ip</td></tr>"; done)
</table>
</div>
</body></html>
EOF
}

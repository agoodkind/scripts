#!/usr/bin/env bash
# Compact email sender with system info (multipart: plain + HTML)
set -uo pipefail

HOSTNAME=$(hostname)
TO="" SUBJECT="" MSG="" FROM="" NAME="" CALLER=""

die() { echo "Error: $1" >&2; exit 1; }
usage() { echo "Usage: $0 -t TO -s SUBJ -m MSG [-n NAME] [-c CALLER]"; exit 1; }

# --- System Info ---
get_uptime() { uptime -p 2>/dev/null | sed 's/up //'; }
get_load()   { uptime | awk -F'load average:' '{print $2}' | xargs; }
get_memory() { free -h | awk '/Mem:/{printf "%s/%s",$3,$2}'; }
get_disk()   { df -h / | awk 'NR==2{printf "%s/%s (%s)",$3,$2,$5}'; }

get_ips() {
    ip -"$1" -o addr 2>/dev/null | awk '
        $2!="lo" && $4!~/^fe80/ {gsub(/\/.*/,"",$4); print $2,$4}'
}

infer_caller() {
    local c; c=$(ps -p $PPID -o comm= 2>/dev/null)
    [[ "$c" =~ ^(bash|sh|zsh)$ ]] && \
        c=$(ps -p $PPID -o args= | awk '{print $NF}' | xargs basename)
    echo "${c:-unknown}"
}

# --- Plain Text ---
render_plain() {
    echo -e "$MSG"
    echo ""
    echo "CALLER"
    printf "  %-8s %s\n" "Script:" "$CALLER"
    printf "  %-8s %s\n" "Time:" "$(date +'%Y-%m-%d %H:%M:%S %Z')"
    printf "  %-8s %s\n" "User:" "$(whoami)"
    echo ""
    echo "SYSTEM"
    printf "  %-8s %s\n" "Host:" "$HOSTNAME"
    printf "  %-8s %s\n" "Uptime:" "$(get_uptime)"
    printf "  %-8s %s\n" "Load:" "$(get_load)"
    printf "  %-8s %s\n" "Memory:" "$(get_memory)"
    printf "  %-8s %s\n" "Disk:" "$(get_disk)"
    echo ""
    echo "NETWORK"
    get_ips 4 | while read -r iface ip; do
        printf "  %-8s %s\n" "$iface:" "$ip"
    done
    get_ips 6 | while read -r iface ip; do
        printf "  %-8s %s\n" "$iface:" "$ip"
    done
}

# --- HTML (message prominent, metadata as footer) ---
render_html() {
    cat <<EOF
<!DOCTYPE html><html><head><style>
body{font:14px -apple-system,BlinkMacSystemFont,Arial,sans-serif;margin:0}
.meta{margin-top:16px;padding-top:12px;border-top:1px solid rgba(128,128,128,0.2);
font-size:11px;opacity:0.6}
.meta table{border-collapse:collapse}
.meta td{padding:1px 0}
.meta .k{padding-right:12px;opacity:0.7}
</style></head><body>
<div>$(echo -e "$MSG" | sed 's/$/<br>/')</div>
<div class="meta">
<table>
<tr><td class="k">Caller</td><td>$CALLER</td></tr>
<tr><td class="k">Time</td><td>$(date +'%Y-%m-%d %H:%M %Z')</td></tr>
<tr><td class="k">Host</td><td>$HOSTNAME</td></tr>
<tr><td class="k">Uptime</td><td>$(get_uptime)</td></tr>
<tr><td class="k">Load</td><td>$(get_load)</td></tr>
<tr><td class="k">Memory</td><td>$(get_memory)</td></tr>
<tr><td class="k">Disk</td><td>$(get_disk)</td></tr>
$(get_ips 4 | while read -r i ip; do echo "<tr><td class=\"k\">$i</td><td>$ip</td></tr>"; done)
$(get_ips 6 | while read -r i ip; do echo "<tr><td class=\"k\">$i</td><td>$ip</td></tr>"; done)
</table>
</div>
</body></html>
EOF
}

# --- Send ---
send_email() {
    local hdr="$NAME $HOSTNAME <$FROM>"
    local bnd
    bnd="----=_$(date +%s)_$$"
    {
        echo "From: $hdr"
        echo "To: $TO"
        echo "Subject: $SUBJECT"
        echo "MIME-Version: 1.0"
        echo "Content-Type: multipart/alternative; boundary=\"$bnd\""
        echo ""
        echo "--$bnd"
        echo "Content-Type: text/plain; charset=UTF-8"
        echo ""
        render_plain
        echo ""
        echo "--$bnd"
        echo "Content-Type: text/html; charset=UTF-8"
        echo ""
        render_html
        echo ""
        echo "--$bnd--"
    } | sendmail -t -f "$FROM"
}

# --- Main ---
while [[ $# -gt 0 ]]; do
    case $1 in
        -t) TO="$2"; shift 2 ;;
        -s) SUBJECT="$2"; shift 2 ;;
        -m) MSG="$2"; shift 2 ;;
        -f) FROM="$2"; shift 2 ;;
        -n) NAME="$2"; shift 2 ;;
        -c) CALLER="$2"; shift 2 ;;
        *)  usage ;;
    esac
done

[[ -z "$TO" || -z "$MSG" ]] && usage
[[ -z "$FROM" ]]   && FROM="${HOSTNAME}-mailer@goodkind.io"
[[ -z "$NAME" ]]   && NAME="$HOSTNAME"
[[ -z "$CALLER" ]] && CALLER=$(infer_caller)

if send_email; then
    echo "Email sent to $TO"
else
    die "Failed"
fi

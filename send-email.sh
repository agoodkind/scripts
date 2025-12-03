#!/usr/bin/env bash

# Simple email sender with system information

set -euo pipefail

# Globals
SENDER_NAME=""
RECIPIENT=""
SUBJECT=""
MESSAGE=""
FROM_ADDRESS=""
HOSTNAME=$(hostname)

show_usage() {
    cat << EOF
Usage: $0 -t <recipient> -s <subject> -m <message> [options]

Options:
  -t, --to      Recipient email address (required)
  -s, --subject Email subject (required)
  -m, --message Email message body (required)
  -f, --from    From email address (default: hostname-mailer@goodkind.io)
  -n, --name    Sender display name (default: hostname)
  -h, --help    Show this help message
EOF
}

die() { echo "Error: $1" >&2; exit 1; }

# Generate aligned key-value lines into array
# Usage: aligned_lines outarray labels[@] values[@]
aligned_lines() {
    local -n _out=$1 _labels=$2 _values=$3
    local max_w=0 i

    for l in "${_labels[@]}"; do
        (( ${#l} > max_w )) && max_w=${#l}
    done
    for i in "${!_labels[@]}"; do
        _out+=("$(printf "%-${max_w}s  %s" "${_labels[$i]}" "${_values[$i]}")")
    done
}

# Print two arrays side by side with gap
# Usage: print_side_by_side left[@] right[@] [gap]
print_side_by_side() {
    local -n _left=$1 _right=$2
    local gap=${3:-4}
    local max_left=0 i line_l line_r
    local max_rows=$(( ${#_left[@]} > ${#_right[@]} \
        ? ${#_left[@]} : ${#_right[@]} ))

    for line in "${_left[@]}"; do
        (( ${#line} > max_left )) && max_left=${#line}
    done

    for (( i=0; i<max_rows; i++ )); do
        line_l="${_left[$i]:-}"
        line_r="${_right[$i]:-}"
        printf "%-${max_left}s%*s%s\n" "$line_l" "$gap" "" "$line_r"
    done
}

get_uptime() {
    if uptime -p 2>/dev/null | grep -q .; then
        uptime -p | sed 's/up //'
    else
        uptime | sed 's/.*up //; s/,  *[0-9]* user.*//'
    fi
}

get_load() {
    uptime | awk -F'load average:' '{print $2}' | sed 's/^ *//'
}

get_memory() {
    if command -v free >/dev/null 2>&1; then
        free -h 2>/dev/null | awk '/^Mem:/ {printf "%s/%s", $3, $2}'
    elif [[ "$(uname)" == "Darwin" ]]; then
        local pages_active pages_wired mem_total mem_gb mem_used
        pages_active=$(vm_stat | awk '/Pages active/ {print $3}' | tr -d '.')
        pages_wired=$(vm_stat | awk '/Pages wired/ {print $4}' | tr -d '.')
        mem_total=$(sysctl -n hw.memsize 2>/dev/null)
        if [[ -n "$mem_total" ]]; then
            mem_gb=$(echo "scale=0; $mem_total / 1073741824" | bc)
            mem_used=$(echo "scale=1; \
                ($pages_active + $pages_wired) * 4096 / 1073741824" | bc 2>/dev/null)
            [[ -n "$mem_used" ]] && echo "${mem_used}G/${mem_gb}G"
        fi
    fi
}

get_disk() {
    df -h / 2>/dev/null | awk 'NR==2 {print $5 " (" $3 "/" $2 ")"}'
}

parse_ip_output() {
    local -n _l=$1 _v=$2
    local ver=$3 line iface addr

    while IFS= read -r line; do
        iface=$(echo "$line" | awk '{print $2}')
        addr=$(echo "$line" | awk '{print $4}' | sed 's/\/.*//')
        [[ "$iface" == lo* ]] && continue
        [[ "$ver" == "6" && "$addr" == fe80* ]] && continue
        _l+=("$iface"); _v+=("$addr")
    done < <(ip "-$ver" -o addr show 2>/dev/null)
}

# Check if ip command supports -o flag (Linux ip vs iproute2mac)
ip_supports_oneline() {
    ip -4 -o addr show &>/dev/null
}

parse_ifconfig_output() {
    local -n _l=$1 _v=$2
    local cur_iface="" line addr

    while IFS= read -r line; do
        if [[ "$line" =~ ^[a-z] ]]; then
            cur_iface=$(echo "$line" | awk '{print $1}' | tr -d ':')
        elif [[ "$cur_iface" == lo* ]]; then
            continue
        elif [[ "$line" =~ "inet " ]]; then
            _l+=("$cur_iface")
            _v+=("$(echo "$line" | awk '{print $2}')")
        elif [[ "$line" =~ "inet6 " ]]; then
            addr=$(echo "$line" | awk '{print $2}' | sed 's/%.*//')
            [[ "$addr" != fe80* ]] && { _l+=("$cur_iface"); _v+=("$addr"); }
        fi
    done < <(ifconfig 2>/dev/null)
}

get_network_ip() {
    local -n _labels=$1 _values=$2

    if command -v ip >/dev/null 2>&1 && ip_supports_oneline; then
        parse_ip_output _labels _values 4
        parse_ip_output _labels _values 6
    else
        parse_ifconfig_output _labels _values
    fi
}

generate_body() {
    echo -e "$MESSAGE"
    echo ""

    local -a labels values
    labels=("Host" "Date" "User" "Uptime" "Load")
    values=(
        "$HOSTNAME"
        "$(date '+%Y-%m-%d %H:%M:%S %Z')"
        "$(whoami)"
        "$(get_uptime)"
        "$(get_load)"
    )

    local mem disk
    mem=$(get_memory)
    [[ -n "$mem" ]] && { labels+=("Mem"); values+=("$mem"); }
    disk=$(get_disk)
    [[ -n "$disk" ]] && { labels+=("Disk"); values+=("$disk"); }

    local -a net_labels net_values
    get_network_ip net_labels net_values

    # Build lines for each column
    local -a sys_lines net_lines
    sys_lines=("-- System --")
    aligned_lines sys_lines labels values

    net_lines=("-- Network --")
    aligned_lines net_lines net_labels net_values

    print_side_by_side sys_lines net_lines
}

send_email() {
    local from_header="$SENDER_NAME $HOSTNAME <$FROM_ADDRESS>"

    if command -v sendmail >/dev/null 2>&1; then
        {
            echo "From: $from_header"
            echo "To: $RECIPIENT"
            echo "Subject: $SUBJECT"
            echo ""
            generate_body
        } | sendmail -t -f "$FROM_ADDRESS"
    elif command -v mail >/dev/null 2>&1; then
        generate_body | mail -s "$SUBJECT" -a "From: $from_header" "$RECIPIENT"
    else
        die "No mail command found (sendmail or mail)"
    fi
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -t|--to) RECIPIENT="$2"; shift 2 ;;
            -s|--subject) SUBJECT="$2"; shift 2 ;;
            -m|--message) MESSAGE="$2"; shift 2 ;;
            -f|--from) FROM_ADDRESS="$2"; shift 2 ;;
            -n|--name) SENDER_NAME="$2"; shift 2 ;;
            -c|--caller) shift 2 ;;
            -h|--help) show_usage; exit 0 ;;
            *) die "Unknown option $1" ;;
        esac
    done

    if [[ -z "$RECIPIENT" || -z "$MESSAGE" ]]; then
        show_usage
        die "Missing required arguments"
    fi
    [[ -z "$FROM_ADDRESS" ]] && FROM_ADDRESS="${HOSTNAME}-mailer@goodkind.io"
    [[ -z "$SENDER_NAME" ]] && SENDER_NAME="$HOSTNAME"
}

main() {
    parse_args "$@"
    send_email && echo "Email sent to $RECIPIENT" || die "Failed to send email"
}

main "$@"

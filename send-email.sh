#!/usr/bin/env bash

# Enhanced email sender with system information and decorated tables
# Usage: send-email.sh -t <recipient> -s <subject> -m <message> \
#   [-n <sender_name>] [-c <caller_info>]

# Default values
SENDER_NAME="Mini Mailer"
RECIPIENT=""
SUBJECT=""
MESSAGE=""
CALLER_INFO=""

# Function to display usage
show_usage() {
    echo "Usage: $0 -t <recipient> -s <subject> -m <message> \
[-n <sender_name>] [-c <caller_info>]"
    echo ""
    echo "Options:"
    echo "  -t, --to      Recipient email address (required)"
    echo "  -s, --subject Email subject (required)"
    echo "  -m, --message Email message body (required)"
    echo "  -n, --name    Sender display name (default: 'Mini Mailer')"
    echo "  -c, --caller  Caller information (script/process name)"
    echo "  -h, --help    Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 -t alex@goodkind.io -s 'xyz' -m 'hello'"
    echo "  $0 -t alex@goodkind.io -s 'Alert' -m 'Issue detected' \
-n 'Monitor' -c 'wan-monitor.sh'"
    exit 1
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--to)
            RECIPIENT="$2"
            shift 2
            ;;
        -s|--subject)
            SUBJECT="$2"
            shift 2
            ;;
        -m|--message)
            MESSAGE="$2"
            shift 2
            ;;
        -n|--name)
            SENDER_NAME="$2"
            shift 2
            ;;
        -c|--caller)
            CALLER_INFO="$2"
            shift 2
            ;;
        -h|--help)
            show_usage
            ;;
        *)
            echo "Error: Unknown option $1"
            show_usage
            ;;
    esac
done

# Validate required arguments
if [[ -z "$RECIPIENT" || -z "$MESSAGE" ]]; then
    echo "Error: Missing required arguments"
    echo ""
    show_usage
fi

# Basic email validation
if [[ ! "$RECIPIENT" =~ \
    ^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$ ]]; then
    echo "Error: Invalid email address format"
    exit 1
fi

# Get system information
get_sys_info() {
    local hostname=$(hostname 2>/dev/null || echo "unknown")
    local uptime=$(uptime -p 2>/dev/null | sed 's/up //' || \
        echo "unknown")
    local load=$(uptime | awk -F'load average:' '{print $2}' | \
        sed 's/^ *//' || echo "N/A")
    local mem_total=$(free -h 2>/dev/null | \
        awk '/^Mem:/ {print $2}' || echo "N/A")
    local mem_used=$(free -h 2>/dev/null | \
        awk '/^Mem:/ {print $3}' || echo "N/A")
    local disk_usage=$(df -h / 2>/dev/null | \
        awk 'NR==2 {print $5}' || echo "N/A")
    local ipv4=$(ip -4 addr show 2>/dev/null | \
        grep -oP 'inet \K[^/]+' | head -1 || echo "N/A")
    local ipv6=$(ip -6 addr show 2>/dev/null | \
        grep -oP 'inet6 \K[^/]+' | grep -v '^::1$' | \
        grep -v '^fe80' | head -1 || echo "N/A")
    
    echo "hostname|$hostname"
    echo "uptime|$uptime"
    echo "load|$load"
    echo "memory|${mem_used}/${mem_total}"
    echo "disk_usage|$disk_usage"
    echo "ipv4|$ipv4"
    echo "ipv6|$ipv6"
}

# Create decorated table
create_table() {
    local title="$1"
    shift
    local items=("$@")
    
    local max_key_len=0
    local max_val_len=0
    
    # Find max lengths
    for item in "${items[@]}"; do
        local key=$(echo "$item" | cut -d'|' -f1)
        local val=$(echo "$item" | cut -d'|' -f2-)
        [ ${#key} -gt "$max_key_len" ] && max_key_len=${#key}
        [ ${#val} -gt "$max_val_len" ] && max_val_len=${#val}
    done
    
    # Limit column widths
    [ "$max_key_len" -gt 20 ] && max_key_len=20
    [ "$max_val_len" -gt 50 ] && max_val_len=50
    
    local width=$((max_key_len + max_val_len + 7))
    [ $width -lt 40 ] && width=40
    [ $width -gt 88 ] && width=88
    
    # Header
    echo "┌$(printf '─%.0s' $(seq 1 $((width-2))))┐"
    printf "│ %-*s │\n" $((width-4)) "$title"
    echo "├$(printf '─%.0s' $(seq 1 $((width-2))))┤"
    
    # Rows
    for item in "${items[@]}"; do
        local key=$(echo "$item" | cut -d'|' -f1)
        local val=$(echo "$item" | cut -d'|' -f2-)
        printf "│ %-*s │ %-*s │\n" "$max_key_len" "$key" "$max_val_len" \
            "$val"
    done
    
    # Footer
    echo "└$(printf '─%.0s' $(seq 1 $((width-2))))┘"
}

# Build email body with tables
build_email_body() {
    local body=""
    
    # Caller information table
    if [ -n "$CALLER_INFO" ]; then
        local caller_items=(
            "script|$CALLER_INFO"
            "timestamp|$(date '+%Y-%m-%d %H:%M:%S %Z')"
            "user|$(whoami)"
            "pid|$$"
        )
        body+="$(create_table "Caller Information" \
            "${caller_items[@]}")"
        body+="\n\n"
    fi
    
    # System information table
    local sys_info=$(get_sys_info)
    local sys_items=()
    while IFS='|' read -r key val; do
        sys_items+=("$key|$val")
    done <<< "$sys_info"
    
    body+="$(create_table "System Information" "${sys_items[@]}")"
    body+="\n\n"
    
    # Original message
    body+="Message:\n"
    body+="$(printf '─%.0s' $(seq 1 50))\n"
    body+="$MESSAGE"
    
    echo -e "$body"
}

# Build the email
EMAIL_BODY=$(build_email_body)

# Send the email
if echo -e "From: $SENDER_NAME <mini-mailer@goodkind.io>\nTo: \
$RECIPIENT\nSubject: $SUBJECT\n\n$EMAIL_BODY" | \
    sendmail -f mini-mailer@goodkind.io "$RECIPIENT"; then
    echo "Email successfully sent to $RECIPIENT"
else
    echo "Error: Failed to send email"
    exit 1
fi

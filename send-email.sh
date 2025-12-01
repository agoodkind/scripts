#!/usr/bin/env bash

# Simple email sender with system information

SENDER_NAME=""
RECIPIENT=""
SUBJECT=""
MESSAGE=""

show_usage() {
    cat << EOF
Usage: $0 -t <recipient> -s <subject> -m <message> [-n <sender_name>]

Options:
  -t, --to      Recipient email address (required)
  -s, --subject Email subject (required)
  -m, --message Email message body (required)
  -n, --name    Sender display name (default: 'System Mailer')
  -h, --help    Show this help message
EOF
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--to) RECIPIENT="$2"; shift 2 ;;
        -s|--subject) SUBJECT="$2"; shift 2 ;;
        -m|--message) MESSAGE="$2"; shift 2 ;;
        -n|--name) SENDER_NAME="$2"; shift 2 ;;
        -c|--caller) shift 2 ;; # Accept but ignore for compatibility
        -h|--help) show_usage; exit 0 ;;
        *) echo "Error: Unknown option $1" >&2; show_usage; exit 1 ;;
    esac
done

# Validate
if [[ -z "$RECIPIENT" || -z "$MESSAGE" ]]; then
    echo "Error: Missing required arguments" >&2
    show_usage
    exit 1
fi

if [[ ! "$RECIPIENT" =~ ^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$ ]]; then
    echo "Error: Invalid email address" >&2
    exit 1
fi

# Build and send email
HOSTNAME=$(hostname)
FROM_ADDRESS="${HOSTNAME}-mailer@goodkind.io"
[ -z "$SENDER_NAME" ] && SENDER_NAME="$HOSTNAME"

(
    echo "From: $SENDER_NAME [$HOSTNAME] <$FROM_ADDRESS>"
    echo "To: $RECIPIENT"
    echo "Subject: $SUBJECT"
    echo ""
    
    # Message
    echo -e "$MESSAGE"
    echo ""
    
    # System info section
    echo "System Information"
    echo "=================="
    echo "Hostname:     $(hostname)"
    echo "Date:         $(date '+%Y-%m-%d %H:%M:%S %Z')"
    echo "User:         $(whoami)"
    echo "Uptime:       $(uptime -p 2>/dev/null | sed 's/up //' || uptime)"
    echo "Load Avg:     $(uptime | awk -F'load average:' '{print $2}' | \
        sed 's/^ *//')"
    
    # Memory
    if command -v free >/dev/null 2>&1; then
        mem=$(free -h 2>/dev/null | awk '/^Mem:/ {printf "%s / %s", \
            $3, $2}')
        [ -n "$mem" ] && echo "Memory:       $mem"
    fi
    
    # Disk
    disk=$(df -h / 2>/dev/null | awk 'NR==2 {print $5}')
    [ -n "$disk" ] && echo "Disk Usage:   $disk"
    
    # Network interfaces
    echo ""
    echo "Network Interfaces"
    echo "=================="
    
    # IPv4
    ip -4 -o addr show 2>/dev/null | awk '$2 !~ /^lo/ {
        iface = $2
        ip = $4
        sub(/\/.*/, "", ip)
        printf "%-12s  %s\n", iface, ip
    }'
    
    # IPv6 (skip link-local)
    ip -6 -o addr show 2>/dev/null | awk '$2 !~ /^lo/ && $4 !~ /^fe80/ {
        iface = $2
        ip = $4
        sub(/\/.*/, "", ip)
        printf "%-12s  %s\n", iface, ip
    }'
    
) | sendmail -f "$FROM_ADDRESS" "$RECIPIENT"

if [ $? -eq 0 ]; then
    echo "Email successfully sent to $RECIPIENT"
else
    echo "Error: Failed to send email" >&2
    exit 1
fi

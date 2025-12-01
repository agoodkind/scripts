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

# Infer caller information automatically
infer_caller() {
    # Try to get caller from parent process
    local ppid=$PPID
    local caller_name=""
    
    # Get parent process command
    if command -v ps >/dev/null 2>&1; then
        caller_name=$(ps -p "$ppid" -o comm= 2>/dev/null | head -1)
        # If it's bash/sh, try to get the actual script name
        if [[ "$caller_name" =~ ^(bash|sh)$ ]]; then
            # Try to get script name from parent's command line
            caller_name=$(ps -p "$ppid" -o args= 2>/dev/null | \
                awk '{for(i=1;i<=NF;i++){if($i!~/^-/ && \
$i~/\.(sh|bash)$/){print $i;exit}}if($NF!~/^-/){print $NF}}' | \
                xargs basename 2>/dev/null || echo "$caller_name")
        fi
    fi
    
    # Fallback: try to get from BASH_SOURCE if available
    if [ -z "$caller_name" ] && [ -n "${BASH_SOURCE[1]}" ]; then
        caller_name=$(basename "${BASH_SOURCE[1]}" 2>/dev/null)
    fi
    
    # Final fallback
    [ -z "$caller_name" ] && caller_name="unknown"
    
    echo "$caller_name"
}

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

# Auto-detect caller if not explicitly provided
if [ -z "$CALLER_INFO" ]; then
    CALLER_INFO=$(infer_caller)
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
    
    # Get ALL IP addresses for ALL interfaces
    local ipv4_entries=()
    local ipv6_entries=()
    
    # Parse ip addr show output using awk - filter out loopback/link-local
    while IFS= read -r entry; do
        local iface=$(echo "$entry" | cut -d':' -f1)
        local ip=$(echo "$entry" | cut -d':' -f2- | cut -d'/' -f1)
        
        # Determine if IPv4 or IPv6 and filter
        if [[ "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            # Skip loopback IPv4
            if [ "$ip" != "127.0.0.1" ]; then
                ipv4_entries+=("${iface}:${ip}")
            fi
        elif [[ "$ip" =~ : ]]; then
            # Skip loopback and link-local IPv6
            if [[ "$ip" != "::1" && "$ip" != fe80* ]]; then
                ipv6_entries+=("${iface}:${ip}")
            fi
        fi
    done < <(ip -o addr show 2>/dev/null | awk '{for(i=1;i<=NF;i++) \
{if($i=="inet" || $i=="inet6") {iface=$2; ip=$(i+1); \
gsub(/\/.*/, "", ip); print iface":"ip; break}}}' | \
        grep -v "^lo:" | grep -v "fe80:")
    
    # Format IP lists with line breaks for readability
    local ipv4_list=""
    if [ ${#ipv4_entries[@]} -eq 0 ]; then
        ipv4_list="N/A"
    else
        for entry in "${ipv4_entries[@]}"; do
            if [ -z "$ipv4_list" ]; then
                ipv4_list="$entry"
            else
                ipv4_list="${ipv4_list}\n                    ${entry}"
            fi
        done
    fi
    
    local ipv6_list=""
    if [ ${#ipv6_entries[@]} -eq 0 ]; then
        ipv6_list="N/A"
    else
        for entry in "${ipv6_entries[@]}"; do
            if [ -z "$ipv6_list" ]; then
                ipv6_list="$entry"
            else
                ipv6_list="${ipv6_list}\n                    ${entry}"
            fi
        done
    fi
    
    echo "hostname|$hostname"
    echo "uptime|$uptime"
    echo "load|$load"
    echo "memory|${mem_used}/${mem_total}"
    echo "disk_usage|$disk_usage"
    echo "ipv4|$ipv4_list"
    echo "ipv6|$ipv6_list"
}

# Create formatted table (plain text, aligned columns)
create_table() {
    local title="$1"
    shift
    local items=("$@")
    
    # Find max key length for alignment
    local max_key_len=0
    for item in "${items[@]}"; do
        local key=$(echo "$item" | cut -d'|' -f1)
        [ ${#key} -gt "$max_key_len" ] && max_key_len=${#key}
    done
    [ "$max_key_len" -lt 12 ] && max_key_len=12
    [ "$max_key_len" -gt 20 ] && max_key_len=20
    
    # Title
    echo "$title"
    echo "$(printf '=%.0s' $(seq 1 ${#title}))"
    echo ""
    
    # Table rows with aligned columns
    for item in "${items[@]}"; do
        local key=$(echo "$item" | cut -d'|' -f1)
        local val=$(echo "$item" | cut -d'|' -f2-)
        
        # Handle multi-line values (like IP addresses)
        if [[ "$val" =~ \\n ]]; then
            # First line
            local first_line=$(echo -e "$val" | head -1)
            printf "%-*s  %s\n" "$max_key_len" "$key" "$first_line"
            # Subsequent lines (indented to align with value column)
            while IFS= read -r line; do
                [ -n "$line" ] && printf "%-*s  %s\n" "$max_key_len" "" \
                    "$line"
            done < <(echo -e "$val" | tail -n +2)
        else
            printf "%-*s  %s\n" "$max_key_len" "$key" "$val"
        fi
    done
    echo ""
}

# Build email body with tables
build_email_body() {
    local body=""
    
    # Original message first
    body+="$MESSAGE"
    body+="\n\n"

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

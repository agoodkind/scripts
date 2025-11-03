#!/bin/bash

# Default values
SENDER_NAME="Mini Mailer"
RECIPIENT=""
SUBJECT=""
MESSAGE=""

# Function to display usage
show_usage() {
    echo "Usage: $0 -t <recipient> -s <subject> -m <message> [-n <sender_name>]"
    echo ""
    echo "Options:"
    echo "  -t, --to      Recipient email address (required)"
    echo "  -s, --subject Email subject (required)"
    echo "  -m, --message Email message body (required)"
    echo "  -n, --name    Sender display name (default: 'Mini Mailer')"
    echo "  -h, --help    Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 -t alex@goodkind.io -s 'xyz' -m 'hello'"
    echo "  $0 -t alex@goodkind.io -s 'Meeting' -m 'See you at 2pm' -n 'John Doe'"
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
if [[ ! "$RECIPIENT" =~ ^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$ ]]; then
    echo "Error: Invalid email address format"
    exit 1
fi

# Send the email
if echo -e "From: $SENDER_NAME <mini-mailer@goodkind.io>\nTo: $RECIPIENT\nSubject: $SUBJECT\n\n$MESSAGE" | sendmail -f mini-mailer@goodkind.io "$RECIPIENT"; then    
    echo "Email successfully sent to $RECIPIENT"
else
    echo "Error: Failed to send email"
    exit 1
fi


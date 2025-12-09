#!/bin/bash
# Proxmox hostname change script with IPv6 support, VM/CT shutdown, and automated post-reboot migration

set -e

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root"
    exit 1
fi

# Get current hostname
OLD_HOSTNAME=$(hostname)
echo "Current hostname: $OLD_HOSTNAME"

# Get new hostname
if [ -z "$1" ]; then
    read -p "Enter new hostname (FQDN, e.g., pve.example.com): " NEW_HOSTNAME
else
    NEW_HOSTNAME="$1"
fi

# Validate hostname format
if [[ ! "$NEW_HOSTNAME" =~ ^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$ ]]; then
    echo "Invalid hostname format"
    exit 1
fi

# Extract short hostname
SHORT_HOSTNAME=$(echo "$NEW_HOSTNAME" | cut -d'.' -f1)

# Get main IPv4 address (non-loopback)
MAIN_IPV4=$(ip -4 addr show | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | grep -v '^127\.' | head -n1)

# Get main IPv6 address (non-link-local, non-loopback)
MAIN_IPV6=$(ip -6 addr show scope global | grep -oP '(?<=inet6\s)[0-9a-f:]+' | grep -v '^::1' | grep -v '^fe80:' | head -n1)

echo "New hostname: $NEW_HOSTNAME"
echo "Short name: $SHORT_HOSTNAME"
echo "Main IPv4: $MAIN_IPV4"
echo "Main IPv6: $MAIN_IPV6"
echo ""

# Check for running VMs and containers
RUNNING_VMS=$(qm list 2>/dev/null | awk '$3 == "running" {print $1}' | xargs)
RUNNING_CTS=$(pct list 2>/dev/null | awk '$2 == "running" {print $1}' | xargs)

if [ -n "$RUNNING_VMS" ] || [ -n "$RUNNING_CTS" ]; then
    echo "Running VMs: ${RUNNING_VMS:-none}"
    echo "Running containers: ${RUNNING_CTS:-none}"
    echo ""
    read -p "Shutdown all running VMs and containers before reboot? (y/n): " SHUTDOWN_ALL
else
    SHUTDOWN_ALL="n"
fi

echo ""
echo "WARNING: You will lose connection when the system reboots."
echo "The migration will happen automatically after reboot."
echo "Check /var/log/pve-hostname-migrate.log after reconnecting."
echo ""
read -p "Continue with hostname change? (y/n): " CONFIRM

if [ "$CONFIRM" != "y" ]; then
    echo "Aborted"
    exit 0
fi

# Shutdown VMs and containers if requested
if [ "$SHUTDOWN_ALL" = "y" ]; then
    echo ""
    echo "Shutting down VMs and containers..."

    # Shutdown VMs
    if [ -n "$RUNNING_VMS" ]; then
        for vmid in $RUNNING_VMS; do
            echo "Shutting down VM $vmid..."
            qm shutdown "$vmid" &
        done
    fi

    # Shutdown containers
    if [ -n "$RUNNING_CTS" ]; then
        for ctid in $RUNNING_CTS; do
            echo "Shutting down container $ctid..."
            pct shutdown "$ctid" &
        done
    fi

    # Wait for shutdowns to complete (max 120 seconds)
    echo "Waiting for graceful shutdowns (max 120 seconds)..."
    sleep 10

    TIMEOUT=120
    ELAPSED=10
    while [ $ELAPSED -lt $TIMEOUT ]; do
        STILL_RUNNING_VMS=$(qm list 2>/dev/null | awk '$3 == "running" {print $1}' | xargs)
        STILL_RUNNING_CTS=$(pct list 2>/dev/null | awk '$2 == "running" {print $1}' | xargs)

        if [ -z "$STILL_RUNNING_VMS" ] && [ -z "$STILL_RUNNING_CTS" ]; then
            echo "All VMs and containers shut down successfully"
            break
        fi

        sleep 5
        ELAPSED=$((ELAPSED + 5))
    done

    # Force stop any remaining VMs/containers
    if [ -n "$STILL_RUNNING_VMS" ]; then
        echo "Force stopping remaining VMs: $STILL_RUNNING_VMS"
        for vmid in $STILL_RUNNING_VMS; do
            qm stop "$vmid"
        done
    fi

    if [ -n "$STILL_RUNNING_CTS" ]; then
        echo "Force stopping remaining containers: $STILL_RUNNING_CTS"
        for ctid in $STILL_RUNNING_CTS; do
            pct stop "$ctid"
        done
    fi
fi

# Backup files
echo ""
echo "Creating backups..."
cp /etc/hostname /etc/hostname.bak.$(date +%Y%m%d-%H%M%S)
cp /etc/hosts /etc/hosts.bak.$(date +%Y%m%d-%H%M%S)
[ -f /etc/postfix/main.cf ] && cp /etc/postfix/main.cf /etc/postfix/main.cf.bak.$(date +%Y%m%d-%H%M%S)

# Update /etc/hostname
echo "Updating /etc/hostname..."
echo "$NEW_HOSTNAME" > /etc/hostname

# Update /etc/hosts
echo "Updating /etc/hosts..."
sed -i "s/$OLD_HOSTNAME/$NEW_HOSTNAME/g" /etc/hosts

# Remove old IP mappings
if [ -n "$MAIN_IPV4" ]; then
    sed -i "/^$MAIN_IPV4/d" /etc/hosts
fi
if [ -n "$MAIN_IPV6" ]; then
    sed -i "/^$MAIN_IPV6/d" /etc/hosts
fi

# Add new IP mappings
if [ -n "$MAIN_IPV4" ]; then
    echo "$MAIN_IPV4 $NEW_HOSTNAME $SHORT_HOSTNAME" >> /etc/hosts
fi

if [ -n "$MAIN_IPV6" ]; then
    echo "$MAIN_IPV6 $NEW_HOSTNAME $SHORT_HOSTNAME" >> /etc/hosts
fi

# Update postfix if exists
if [ -f /etc/postfix/main.cf ]; then
    echo "Updating /etc/postfix/main.cf..."
    sed -i "s/myhostname = $OLD_HOSTNAME/myhostname = $NEW_HOSTNAME/" /etc/postfix/main.cf
fi

# Set hostname immediately
hostnamectl set-hostname "$NEW_HOSTNAME"

# Backup old node directory
if [ -d "/etc/pve/nodes/$OLD_HOSTNAME" ]; then
    echo "Backing up old node configuration..."
    cp -R "/etc/pve/nodes/$OLD_HOSTNAME" "/root/pve-node-backup-$OLD_HOSTNAME-$(date +%Y%m%d-%H%M%S)"
fi

# Create post-reboot migration script
POST_REBOOT_SCRIPT="/usr/local/bin/pve-hostname-migrate.sh"
cat > "$POST_REBOOT_SCRIPT" << 'EOFSCRIPT'
#!/bin/bash
# Post-reboot config migration script
# Generated by hostname change script

OLD_HOSTNAME="OLD_HOSTNAME_PLACEHOLDER"
NEW_HOSTNAME="NEW_HOSTNAME_PLACEHOLDER"

LOGFILE="/var/log/pve-hostname-migrate.log"

exec > >(tee -a "$LOGFILE") 2>&1

echo "=========================================="
echo "Proxmox Hostname Migration"
echo "$(date)"
echo "Old: $OLD_HOSTNAME -> New: $NEW_HOSTNAME"
echo "=========================================="

# Wait for cluster services to be ready
echo "Waiting for Proxmox cluster services..."
sleep 20

# Check if new node directory exists, wait up to 60 seconds
TIMEOUT=60
ELAPSED=0
while [ ! -d "/etc/pve/nodes/$NEW_HOSTNAME" ] && [ $ELAPSED -lt $TIMEOUT ]; do
    echo "Waiting for new node directory... ($ELAPSED/$TIMEOUT)"
    sleep 5
    ELAPSED=$((ELAPSED + 5))
done

if [ ! -d "/etc/pve/nodes/$NEW_HOSTNAME" ]; then
    echo "ERROR: New node directory /etc/pve/nodes/$NEW_HOSTNAME does not exist"
    echo "Manual intervention required. Check cluster status with 'pvecm status'"
    exit 1
fi

echo "Node directories:"
ls -la /etc/pve/nodes/
echo ""

# Shutdown any running VMs/containers before migration
echo "Checking for running VMs and containers..."
RUNNING_VMS=$(qm list 2>/dev/null | awk '$3 == "running" {print $1}' | xargs)
RUNNING_CTS=$(pct list 2>/dev/null | awk '$2 == "running" {print $1}' | xargs)

if [ -n "$RUNNING_VMS" ] || [ -n "$RUNNING_CTS" ]; then
    echo "Shutting down running guests before migration..."

    # Shutdown VMs
    if [ -n "$RUNNING_VMS" ]; then
        for vmid in $RUNNING_VMS; do
            echo "Shutting down VM $vmid..."
            qm shutdown "$vmid" --timeout 60 || qm stop "$vmid"
        done
    fi

    # Shutdown containers
    if [ -n "$RUNNING_CTS" ]; then
        for ctid in $RUNNING_CTS; do
            echo "Shutting down container $ctid..."
            pct shutdown "$ctid" --timeout 60 || pct stop "$ctid"
        done
    fi

    echo "Waiting for shutdowns to complete..."
    sleep 10
fi

# Migrate VM configs
if [ -d "/etc/pve/nodes/$OLD_HOSTNAME/qemu-server" ]; then
    echo "Migrating VM configurations..."
    mkdir -p "/etc/pve/nodes/$NEW_HOSTNAME/qemu-server"
    if ls /etc/pve/nodes/$OLD_HOSTNAME/qemu-server/*.conf 2>/dev/null; then
        cp -v /etc/pve/nodes/$OLD_HOSTNAME/qemu-server/*.conf /etc/pve/nodes/$NEW_HOSTNAME/qemu-server/
    else
        echo "No VM configs to migrate"
    fi
fi

# Migrate container configs
if [ -d "/etc/pve/nodes/$OLD_HOSTNAME/lxc" ]; then
    echo "Migrating container configurations..."
    mkdir -p "/etc/pve/nodes/$NEW_HOSTNAME/lxc"
    if ls /etc/pve/nodes/$OLD_HOSTNAME/lxc/*.conf 2>/dev/null; then
        cp -v /etc/pve/nodes/$OLD_HOSTNAME/lxc/*.conf /etc/pve/nodes/$NEW_HOSTNAME/lxc/
    else
        echo "No container configs to migrate"
    fi
fi

# Migrate RRD statistics for node
if [ -d "/var/lib/rrdcached/db/pve2-node/$OLD_HOSTNAME" ]; then
    echo "Migrating node RRD statistics..."
    cp -R /var/lib/rrdcached/db/pve2-node/$OLD_HOSTNAME /var/lib/rrdcached/db/pve2-node/$NEW_HOSTNAME
fi

# Migrate RRD statistics for storage
if [ -d "/var/lib/rrdcached/db/pve2-storage/$OLD_HOSTNAME" ]; then
    echo "Migrating storage RRD statistics..."
    cp -R /var/lib/rrdcached/db/pve2-storage/$OLD_HOSTNAME /var/lib/rrdcached/db/pve2-storage/$NEW_HOSTNAME
fi

echo ""
echo "=========================================="
echo "Migration completed successfully!"
echo "$(date)"
echo "=========================================="
echo ""
echo "Next steps:"
echo "1. Verify VMs/containers appear in web UI: https://$NEW_HOSTNAME:8006"
echo "2. Start your VMs and containers"
echo "3. Remove old node directory: rm -rf /etc/pve/nodes/$OLD_HOSTNAME"
echo "4. Disable this service: systemctl disable pve-hostname-migrate.service"
echo "5. Remove migration files:"
echo "   rm /etc/systemd/system/pve-hostname-migrate.service"
echo "   rm /usr/local/bin/pve-hostname-migrate.sh"
echo "   systemctl daemon-reload"

# Disable the service so it doesn't run again
systemctl disable pve-hostname-migrate.service

exit 0
EOFSCRIPT

# Replace placeholders in post-reboot script
sed -i "s/OLD_HOSTNAME_PLACEHOLDER/$OLD_HOSTNAME/" "$POST_REBOOT_SCRIPT"
sed -i "s/NEW_HOSTNAME_PLACEHOLDER/$NEW_HOSTNAME/" "$POST_REBOOT_SCRIPT"
chmod +x "$POST_REBOOT_SCRIPT"

# Create systemd service for post-reboot migration
cat > /etc/systemd/system/pve-hostname-migrate.service << EOF
[Unit]
Description=Proxmox Hostname Migration
After=pve-cluster.service
Wants=pve-cluster.service

[Service]
Type=oneshot
ExecStart=$POST_REBOOT_SCRIPT
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

# Enable the service
systemctl daemon-reload
systemctl enable pve-hostname-migrate.service

echo ""
echo "=========================================="
echo "Hostname change completed!"
echo "=========================================="
echo ""
echo "Post-reboot migration will run automatically."
echo "Check /var/log/pve-hostname-migrate.log after reboot."
echo ""
echo "The system will reboot in 10 seconds..."
echo "Press Ctrl+C to cancel."
sleep 10

reboot

#!/usr/bin/env bash
# Replicate ProxmoxVE container preparation steps
# Usage: ./prepare-container.sh CTID

CTID="${1}"

if [ -z "$CTID" ]; then
    echo "Usage: ./prepare-container.sh CTID"
    exit 1
fi

echo "Preparing container $CTID..."

echo "→ Removing Python EXTERNALLY-MANAGED blocks..."
pct exec $CTID -- rm -rf /usr/lib/python3.*/EXTERNALLY-MANAGED

echo "→ Disabling networkd wait service..."
pct exec $CTID -- systemctl disable -q --now systemd-networkd-wait-online.service

echo "→ Updating package lists..."
pct exec $CTID -- apt-get update

echo "→ Upgrading packages..."
pct exec $CTID -- apt-get -o Dpkg::Options::="--force-confold" -y dist-upgrade

echo "→ Installing essential packages..."
pct exec $CTID -- apt-get install -y neovim htop curl wget net-tools

echo "→ Setting 256-color terminal..."
pct exec $CTID -- bash -c 'grep -qxF "export TERM=\"xterm-256color\"" /root/.bashrc || echo "export TERM=\"xterm-256color\"" >> /root/.bashrc'

echo "→ Disabling default MOTD scripts..."
pct exec $CTID -- bash -c 'if [ -d /etc/update-motd.d/ ] && [ "$(ls -A /etc/update-motd.d/)" ]; then chmod -x /etc/update-motd.d/*; fi'

echo "→ Configuring auto-login..."
pct exec $CTID -- bash -c 'mkdir -p /etc/systemd/system/container-getty@1.service.d/ && cat > /etc/systemd/system/container-getty@1.service.d/override.conf << "EOF"
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear --keep-baud tty%I 115200,38400,9600 $TERM
EOF'

echo "→ Reloading systemd..."
pct exec $CTID -- systemctl daemon-reload

echo "→ Restarting getty service..."
pct exec $CTID -- systemctl restart container-getty@1.service

echo "✓ Container $CTID prepared successfully"
#!/usr/bin/env bash
# Replicate ProxmoxVE container preparation steps
# Usage: ./prepare-container.sh CTID

set -euo pipefail

CTID="${1}"

if [ -z "$CTID" ]; then
    echo "Usage: ./prepare-container.sh CTID"
    exit 1
fi

echo "🚀 Preparing container $CTID..."

echo "🌐 Generating and setting en_US.UTF-8 locale..."
pct exec $CTID -- bash -c 'apt-get install -y locales && locale-gen en_US.UTF-8 && update-locale LANG=en_US.UTF-8'
export_cmd='export LANG="en_US.UTF-8"\nexport LC_ALL="en_US.UTF-8"'
pct exec $CTID -- bash -c "grep -qxF 'export LANG=\"en_US.UTF-8\"' /root/.bashrc || echo 'export LANG=\"en_US.UTF-8\"' >> /root/.bashrc"
pct exec $CTID -- bash -c "grep -qxF 'export LC_ALL=\"en_US.UTF-8\"' /root/.bashrc || echo 'export LC_ALL=\"en_US.UTF-8\"' >> /root/.bashrc"

echo "🐍 Removing Python EXTERNALLY-MANAGED blocks..."
pct exec $CTID -- rm -rf /usr/lib/python3.*/EXTERNALLY-MANAGED

echo "🛑 Disabling networkd wait service..."
pct exec $CTID -- systemctl disable -q --now systemd-networkd-wait-online.service

echo "📦 Updating package lists..."
pct exec $CTID -- apt-get update

echo "⬆️  Upgrading packages..."
pct exec $CTID -- apt-get -o Dpkg::Options::="--force-confold" -y dist-upgrade

echo "🛠️  Installing essential packages..."
pct exec $CTID -- apt-get install -y neovim htop curl wget net-tools 

echo "⏰ Setting timezone to host timezone..."
HOST_TZ=$(timedatectl show -p Timezone --value 2>/dev/null || cat /etc/timezone 2>/dev/null || echo "UTC")
pct exec $CTID -- timedatectl set-timezone "$HOST_TZ"

echo "📝 Configuring rsyslog for local time..."
pct exec $CTID -- bash -c 'cat > /etc/rsyslog.d/50-default-local.conf << "EOF"
# Use local time instead of UTC
$ActionFileDefaultTemplate RSYSLOG_TraditionalFileFormat
$template TraditionalFormatWithLocalTime,"%timegenerated% %HOSTNAME% %syslogtag%%msg:::drop-last-lf%\n"
$ActionFileDefaultTemplate TraditionalFormatWithLocalTime
EOF'
pct exec $CTID -- systemctl restart rsyslog

echo "🎨 Setting 256-color terminal..."
pct exec $CTID -- bash -c 'grep -qxF "export TERM=\"xterm-256color\"" /root/.bashrc || echo "export TERM=\"xterm-256color\"" >> /root/.bashrc'

echo "🧹 Removing '[ -z \"$PS1\" ] && return' from /root/.bashrc if present..."
pct exec $CTID -- sed -i '/\[ -z \"\$PS1\" \] && return/d' /root/.bashrc

echo "🙈 Disabling default MOTD scripts..."
pct exec $CTID -- bash -c 'if [ -d /etc/update-motd.d/ ] && [ "$(ls -A /etc/update-motd.d/)" ]; then chmod -x /etc/update-motd.d/*; fi'

echo "🔑 Configuring auto-login..."
pct exec $CTID -- bash -c 'mkdir -p /etc/systemd/system/container-getty@1.service.d/ && cat > /etc/systemd/system/container-getty@1.service.d/override.conf << "EOF"
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear --keep-baud tty%I 115200,38400,9600 $TERM
EOF'

echo "⏳ Lowering systemctl shutdown timeout to 15 seconds..."
pct exec $CTID -- bash -c 'mkdir -p /etc/systemd/system.conf.d && echo -e "[Manager]\nDefaultTimeoutStopSec=15s" > /etc/systemd/system.conf.d/timeout.conf'

echo "🔄 Reloading systemd..."
pct exec $CTID -- systemctl daemon-reload

echo "♻️  Restarting getty service..."
pct exec $CTID -- systemctl restart container-getty@1.service

echo "🔁 Rebooting container to apply all changes..."
pct reboot $CTID

echo "✅ Container $CTID prepared successfully"
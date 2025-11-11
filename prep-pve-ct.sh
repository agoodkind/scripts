#!/usr/bin/env bash

set -euo pipefail

CTID="${1}"

if [ -z "$CTID" ]; then
    echo "Usage: ${0} CTID"
    exit 1
fi

function execute() {
    echo "Executing: $*"
    pct exec "$CTID" -- "$@"
}

echo "🚀 Preparing container $CTID..."

execute apt-get update

echo "🌐 Generating and setting en_US.UTF-8 locale..."
execute apt-get install -y locales
execute sed -i "s/^# *en_US.UTF-8/en_US.UTF-8/" /etc/locale.gen
execute locale-gen
execute update-locale LANG=en_US.UTF-8
execute bash -c "grep -qxF 'export LANG=\"en_US.UTF-8\"' /root/.bashrc || echo 'export LANG=\"en_US.UTF-8\"' >> /root/.bashrc"
execute bash -c "grep -qxF 'export LC_ALL=\"en_US.UTF-8\"' /root/.bashrc || echo 'export LC_ALL=\"en_US.UTF-8\"' >> /root/.bashrc"

echo "📦 Updating package lists..."
execute apt-get update

echo "⬆️  Upgrading packages..."
execute apt-get -y upgrade
execute apt-get -o Dpkg::Options::="--force-confold" -y dist-upgrade

echo "🛠️  Installing essential packages..."
execute apt-get -y install neovim htop curl wget net-tools gpg rsyslog

echo "🧹 Removing unnecessary packages..."
execute apt-get -y autoremove

echo "🐍 Removing Python EXTERNALLY-MANAGED blocks..."
execute rm -rf /usr/lib/python3.*/EXTERNALLY-MANAGED

echo "🛑 Disabling networkd wait service..."
execute systemctl disable -q --now systemd-networkd-wait-online.service

echo "⏰ Setting timezone to host timezone..."
HOST_TZ=$(timedatectl show -p Timezone --value 2>/dev/null || cat /etc/timezone 2>/dev/null || echo "UTC")
execute timedatectl set-timezone "$HOST_TZ"

echo "📝 Configuring rsyslog for local time..."
execute bash -c 'cat > /etc/rsyslog.d/50-default-local.conf << "EOF"
# Use local time instead of UTC
$ActionFileDefaultTemplate RSYSLOG_TraditionalFileFormat
$template TraditionalFormatWithLocalTime,"%timegenerated% %HOSTNAME% %syslogtag%%msg:::drop-last-lf%\n"
$ActionFileDefaultTemplate TraditionalFormatWithLocalTime
EOF'
execute systemctl restart rsyslog

echo "🎨 Setting 256-color terminal..."
execute bash -c 'grep -qxF "export TERM=\"xterm-256color\"" /root/.bashrc || echo "export TERM=\"xterm-256color\"" >> /root/.bashrc'

echo "🧹 Removing '[ -z \"\$PS1\" ] && return' from /root/.bashrc if present..."
execute bash -c 'sed -i "/\[ -z \"\$PS1\" \] && return/d" /root/.bashrc'

echo "🙈 Disabling default MOTD scripts..."
execute bash -c 'if [ -d /etc/update-motd.d/ ] && [ "$(ls -A /etc/update-motd.d/)" ]; then chmod -x /etc/update-motd.d/*; fi'

echo "🔑 Configuring auto-login..."
execute bash -c 'mkdir -p /etc/systemd/system/container-getty@1.service.d/ && cat > /etc/systemd/system/container-getty@1.service.d/override.conf << "EOF"
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear --keep-baud tty%I 115200,38400,9600 $TERM
EOF'

echo "⏳ Lowering systemctl shutdown timeout to 15 seconds..."
execute bash -c 'mkdir -p /etc/systemd/system.conf.d && echo -e "[Manager]\nDefaultTimeoutStopSec=15s" > /etc/systemd/system.conf.d/timeout.conf'

echo "🔄 Reloading systemd..."
execute systemctl daemon-reload

echo "♻️  Restarting getty service..."
execute systemctl restart container-getty@1.service

echo "🔁 Rebooting container to apply all changes..."
execute reboot

echo "✅ Container $CTID prepared successfully"
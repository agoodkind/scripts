#!/usr/bin/env bash
# Backup Proxmox host configuration to NAS

set -euo pipefail

BACKUP_DEST="agoodkind@nas.home.goodkind.io:\
/mnt/cold_storage/backups/vault/host"
SNAPSHOT_NAME="root-backup-$(date +%Y%m%d-%H%M%S)"
SNAPSHOT_SIZE="5G"
EMAIL_RECIPIENT="alex@goodkind.io"
SEND_EMAIL_SCRIPT="/opt/scripts/send-email"
LOG_FILE="/var/log/backup-vault-host.log"

# Email notification helper
send_email() {
    local subject="$1"
    local message="$2"
    
    if [ -f "$SEND_EMAIL_SCRIPT" ] && \
        [ -x "$SEND_EMAIL_SCRIPT" ]; then
        "$SEND_EMAIL_SCRIPT" -t "$EMAIL_RECIPIENT" -s "$subject" \
            -m "$message" -n "Vault Backup" 2>&1
    fi
}

# Log helper
log() {
    local msg
    msg="[$(date '+%Y-%m-%d %H:%M:%S')] $1"
    echo "$msg"
    echo "$msg" >> "$LOG_FILE"
}

log "Starting host backup..."

# Cleanup trap
cleanup() {
    if [ -n "${MOUNT_POINT:-}" ] && \
        mountpoint -q "$MOUNT_POINT" 2>/dev/null; then
        umount "$MOUNT_POINT" 2>/dev/null || true
        rmdir "$MOUNT_POINT" 2>/dev/null || true
    fi
    if [ -n "${SNAPSHOT_NAME:-}" ] && \
        lvs "/dev/pve/$SNAPSHOT_NAME" >/dev/null 2>&1; then
        lvremove -y "/dev/pve/$SNAPSHOT_NAME" 2>/dev/null || true
    fi
}

trap cleanup EXIT

# Create LVM snapshot
log "Creating snapshot: ${SNAPSHOT_NAME}"
lvcreate -L"${SNAPSHOT_SIZE}" -s -n "${SNAPSHOT_NAME}" /dev/pve/root

# Mount snapshot
MOUNT_POINT="/mnt/${SNAPSHOT_NAME}"
mkdir -p "${MOUNT_POINT}"
mount -o ro "/dev/pve/${SNAPSHOT_NAME}" "${MOUNT_POINT}"

# Backup to NAS
log "Backing up to NAS..."
set +e
rsync -aAXH --delete --stats \
  --exclude="/dev/*" --exclude="/proc/*" --exclude="/sys/*" \
  --exclude="/tmp/*" --exclude="/run/*" --exclude="/mnt/*" \
  --exclude="/media/*" --exclude="/lost+found" \
  --exclude="/var/lib/vz/*" \
  "${MOUNT_POINT}/" "${BACKUP_DEST}/" \
  > /tmp/rsync-output.txt 2>&1
RSYNC_EXIT=$?
set -e

if [ "$RSYNC_EXIT" -ne 0 ] && [ "$RSYNC_EXIT" -ne 24 ]; then
    log "ERROR: rsync failed with exit code $RSYNC_EXIT"
    cat /tmp/rsync-output.txt >> "$LOG_FILE"
    send_email "Vault Backup Failed" \
        "Host backup rsync failed (exit $RSYNC_EXIT)\n\n\
Time: $(date)\n\nCheck log: $LOG_FILE"
    exit 1
fi

# Extract stats
RSYNC_OUTPUT=$(cat /tmp/rsync-output.txt)
TOTAL_SIZE=$(echo "$RSYNC_OUTPUT" | \
    grep "Total file size:" | awk '{print $4, $5}' || echo "N/A")
TRANSFERRED=$(echo "$RSYNC_OUTPUT" | \
    grep "Total transferred file size:" | \
    awk '{print $5, $6}' || echo "N/A")
FILES=$(echo "$RSYNC_OUTPUT" | \
    grep "Number of regular files transferred:" | \
    awk '{print $6}' || echo "N/A")
rm -f /tmp/rsync-output.txt

# Cleanup
log "Cleaning up..."
umount "${MOUNT_POINT}"
rmdir "${MOUNT_POINT}"
lvremove -y "/dev/pve/${SNAPSHOT_NAME}"

log "Host backup completed successfully"

# Send success email
send_email "Vault Host Backup Completed" \
    "Proxmox host backup completed successfully.\n\n\
Snapshot: ${SNAPSHOT_NAME}\n\
Destination: cold_storage:/backups/vault/host\n\n\
Stats:\n\
- Total size: ${TOTAL_SIZE:-N/A}\n\
- Transferred: ${TRANSFERRED:-N/A}\n\
- Files: ${FILES:-N/A}\n\n\
Time: $(date)"


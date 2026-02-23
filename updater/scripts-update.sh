#!/usr/bin/env bash
set -e
SCRIPTS_DIR="$1"
echo "[scripts-update] Starting update of $SCRIPTS_DIR"
git config --system --add safe.directory "$SCRIPTS_DIR" 2>&1 | grep -v "safe.directory" || true
echo "[scripts-update] Fetching from origin"
git -C "$SCRIPTS_DIR" fetch origin
echo "[scripts-update] Resetting to origin/main"
git -C "$SCRIPTS_DIR" reset --hard origin/main
echo "[scripts-update] Updated to $(git -C "$SCRIPTS_DIR" rev-parse --short HEAD)"

#!/usr/bin/env bash
SCRIPTS_DIR="$1"
git config --system --add safe.directory "$SCRIPTS_DIR" 2>&1 | grep -v "safe.directory" || true
git -C "$SCRIPTS_DIR" fetch origin \
    && git -C "$SCRIPTS_DIR" reset --hard origin/main

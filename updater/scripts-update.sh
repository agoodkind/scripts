#!/usr/bin/env bash
SCRIPTS_DIR="$1"
git config --system --add safe.directory "$SCRIPTS_DIR" 2>/dev/null || true
git -C "$SCRIPTS_DIR" fetch origin \
    && git -C "$SCRIPTS_DIR" reset --hard origin/main

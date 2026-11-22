#!/usr/bin/env bash
# First-time setup for GameDrop: asks a few questions, writes .env, checks your
# Telegram bot, and can run the first collection cycle.
#
#   ./scripts/setup.sh
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
    cat >&2 <<'EOF'
GameDrop needs Go to run from source, and it is not installed yet.

  1. Open https://go.dev/dl/ and install the Windows/macOS/Linux package.
  2. Close this window, open a new one, and run this script again.
EOF
    exit 1
fi

exec go run ./cmd/gamedrop --setup "$@"

#!/usr/bin/env bash
# Run one GameDrop collection cycle from the repository root.
#
#   ./scripts/run.sh                 # uses .env (or real environment variables)
#   ENV_FILE_PATH=./other.env ./scripts/run.sh
#   ./scripts/run.sh --help          # flags are passed through to the binary
#
# Variables already present in the environment win over .env entries.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
    echo "gamedrop: Go is required (1.27+). Install it from https://go.dev/dl/" >&2
    exit 1
fi

go_version="$(go env GOVERSION 2>/dev/null || true)"
case "$go_version" in
    go1.2[7-9]*|go1.[3-9][0-9]*|go[2-9]*) ;;
    *) echo "gamedrop: warning: $go_version detected; Go 1.27+ is recommended" >&2 ;;
esac

env_file="${ENV_FILE_PATH:-.env}"
if [ ! -f "$env_file" ]; then
    echo "gamedrop: note: $env_file not found; using the current environment and defaults" >&2
fi

exec go run ./cmd/gamedrop "$@"

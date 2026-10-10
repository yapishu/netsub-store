#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p bin data
if [[ ! -f web/dist/index.html ]]; then
  npm --prefix web ci
  npm --prefix web run build
fi
go build -o bin/store ./cmd/store
if [[ ! -f data/preview.env ]]; then
  umask 077
  preview_key="$(bin/store encryption-key)"
  preview_hash="$(printf '%s\n' 'preview-password-only' | bin/store hash-password)"
  printf 'DATA_ENCRYPTION_KEY=%s\nADMIN_PASSWORD_HASH=%s\n' "$preview_key" "$preview_hash" > data/preview.env
fi
while IFS='=' read -r key value; do export "$key=$value"; done < data/preview.env
export DEMO=true LISTEN=127.0.0.1:8080 PUBLIC_ORIGIN=http://localhost:8080 DB_PATH=data/preview.db
printf 'Preview: http://localhost:8080 — admin password: preview-password-only\nPayments, emails, and spawning are disabled.\n'
exec bin/store

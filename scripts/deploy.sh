#!/usr/bin/env bash
# Build the current checkout and ship it to the `chat` host (see ~/.ssh/config).
# Usage: scripts/deploy.sh [host]   (default: chat). Secrets stay on the host;
# this never copies .env, cliproxyapi.txt or zai_config.json.
set -euo pipefail
HOST="${1:-chat}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST=/opt/elichat

cd "$ROOT"
echo "== building"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o build/elichat ./cmd/elichat
(cd frontend && npm run build >/dev/null)

echo "== syncing to $HOST:$DEST"
ssh "$HOST" "mkdir -p $DEST/frontend $DEST/ext"
rsync -az --delete frontend/dist/ "$HOST:$DEST/frontend/dist/"
rsync -az --delete ext/ "$HOST:$DEST/ext/"
rsync -az deploy/elichat.service "$HOST:/etc/systemd/system/elichat.service"
rsync -az deploy/nginx-estro.help.conf "$HOST:/etc/nginx/sites-enabled/estro.help.conf"
rsync -az build/elichat "$HOST:$DEST/elichat.new"

echo "== restarting"
ssh "$HOST" "set -e; mv $DEST/elichat.new $DEST/elichat; chmod 755 $DEST/elichat; systemctl daemon-reload; systemctl enable --quiet elichat; systemctl restart elichat; nginx -t >/dev/null 2>&1 && systemctl reload nginx; sleep 2; systemctl is-active elichat; curl -sf http://127.0.0.1:3000/api/health"
echo
echo "deployed $(git rev-parse --short HEAD)"

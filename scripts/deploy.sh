#!/usr/bin/env bash
# Deploy ferret to Hetzner: git pull + rebuild on server.
set -euo pipefail

SERVER="root@138.199.214.114"
REMOTE="/opt/ferret"

echo "→ pushing local commits..."
git push origin main

echo "→ deploying to $SERVER"
ssh "$SERVER" bash <<ENDSSH
set -euo pipefail
export PATH=\$PATH:/usr/local/go/bin
cd $REMOTE
git pull origin main
go build -o ferret ./cmd/ferret
echo "✓ built: \$(./ferret 2>&1 | head -1 || true)"
ENDSSH

echo "✓ deployed to $SERVER:$REMOTE"

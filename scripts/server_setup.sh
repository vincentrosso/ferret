#!/usr/bin/env bash
# One-time setup for ferret on Hetzner CX23 (Ubuntu 24.04).
# Run from your local machine:
#   ssh root@138.199.214.114 'bash -s' < scripts/server_setup.sh
set -euo pipefail

GO_VERSION="1.26.3"
REMOTE="/opt/ferret"
REPO="git@github.com:vincentrosso/ferret.git"

echo "=== installing Go $GO_VERSION ==="
wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
rm -rf /usr/local/go
tar -C /usr/local -xzf /tmp/go.tar.gz
rm /tmp/go.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
export PATH=$PATH:/usr/local/go/bin
go version

echo "=== installing Chromium dependencies ==="
apt-get update -q
apt-get install -y -q \
  libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 \
  libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 \
  libgbm1 libasound2 ca-certificates fonts-liberation wget

echo "=== generating deploy key ==="
if [ ! -f ~/.ssh/ferret_deploy ]; then
  ssh-keygen -t ed25519 -C "ferret-deploy@autoarb" -f ~/.ssh/ferret_deploy -N ""
fi
cat >> ~/.ssh/config <<'EOF'

Host github.com-ferret
  HostName github.com
  User git
  IdentityFile ~/.ssh/ferret_deploy
  StrictHostKeyChecking no
EOF

echo ""
echo ">>> Add this deploy key to https://github.com/vincentrosso/ferret/settings/keys <<<"
cat ~/.ssh/ferret_deploy.pub
echo ""
read -p "Press Enter after adding the deploy key..."

echo "=== cloning repo ==="
mkdir -p "$REMOTE"
cd "$REMOTE"
git init
git remote add origin "$(echo $REPO | sed 's/git@github.com:/git@github.com-ferret:/')"
git fetch origin
git reset --hard origin/main

echo "=== building binary ==="
go build -o ferret ./cmd/ferret
echo "✓ built"

echo "=== creating .env ==="
if [ ! -f .env ]; then
  cat > .env <<'ENVEOF'
COPART_EMAIL=vincentrosso@gmail.com
COPART_PASSWORD=
ANTHROPIC_API_KEY=
ENVEOF
  echo "Edit /opt/ferret/.env and fill in COPART_PASSWORD and ANTHROPIC_API_KEY"
fi

echo "=== setting up nginx for reports ==="
cat > /etc/nginx/sites-available/ferret-reports <<'NGINX'
server {
    listen 80;
    server_name autoarb.ndex.us;

    location /ferret/ {
        alias /opt/ferret/reports/;
        autoindex on;
        try_files $uri $uri/ /ferret/index.html;
    }
}
NGINX
ln -sf /etc/nginx/sites-available/ferret-reports /etc/nginx/sites-enabled/ferret-reports 2>/dev/null || true
nginx -t && systemctl reload nginx

echo "=== installing cron (5am PDT = 12:00 UTC) ==="
(crontab -l 2>/dev/null | grep -v ferret; echo "0 12 * * * /opt/ferret/scripts/daily_run.sh >> /opt/ferret/logs/cron.log 2>&1") | crontab -
crontab -l | grep ferret

echo "=== chmod scripts ==="
chmod +x /opt/ferret/scripts/*.sh

echo ""
echo "=== setup complete ==="
echo "  Binary:  /opt/ferret/ferret"
echo "  Reports: http://autoarb.ndex.us/ferret/"
echo "  Cron:    0 12 * * * (5am PDT)"
echo "  Logs:    /opt/ferret/logs/"
echo ""
echo "Run manually: /opt/ferret/scripts/daily_run.sh"

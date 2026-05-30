#!/usr/bin/env bash
# Full daily pipeline: search → detail → analyze → report
# Runs at 5am PDT via cron on the Hetzner server.
set -euo pipefail

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

set -a; source .env; set +a

DATE=$(date +%Y-%m-%d)
mkdir -p logs reports data/images data/raw

LOG="logs/$DATE.log"
exec > >(tee -a "$LOG") 2>&1

echo "=== ferret daily run $DATE $(date +%H:%M:%S) ==="

echo "--- 1/4 search (next 5 days, Toyota/Honda/Lexus, hail) ---"
./ferret copart search -out lots-ranked.json

echo "--- 2/4 details + images ---"
./ferret copart detail -from lots-ranked.json -workers 2 -data data -images=true

echo "--- 3/4 damage analysis ---"
./ferret copart analyze -in lots-ranked.json -data data -out lots-analyzed.json

echo "--- 4/4 report ---"
./ferret copart report -in lots-analyzed.json -out-dir reports -top 10

# Regenerate index page listing all reports
python3 -c "
import os, glob
reports = sorted(glob.glob('reports/*.html'), reverse=True)
links = '\n'.join(f'<li><a href=\"{os.path.basename(r)}\">{os.path.basename(r)}</a></li>' for r in reports)
open('reports/index.html','w').write(f'<!DOCTYPE html><html><head><meta charset=UTF-8><title>Hail Arb Reports</title><style>body{{font-family:sans-serif;padding:24px;background:#0f172a;color:#e2e8f0}}a{{color:#3b82f6}}li{{margin:6px 0}}</style></head><body><h2>Hail Arb Reports</h2><ul>{links}</ul></body></html>')
print('index updated')
"

echo "=== done $(date +%H:%M:%S) ==="

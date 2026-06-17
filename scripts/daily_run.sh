#!/usr/bin/env bash
# Full daily pipeline: search → detail → analyze → value → report
# Runs at 5am PDT via cron on the Hetzner server.
set -euo pipefail

# Group-writable output so the www-data web service (lookup "Analyze", etc.)
# can also write data/raw + data/images. Pairs with setgid on those dirs.
umask 002

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

set -a; source .env; set +a

AUTOARB_DIR="${AUTOARB_DIR:-/opt/autoarb}"
PYTHON="${AUTOARB_PYTHON:-python3}"
RUN="$PYTHON $AUTOARB_DIR/run_scrape.py"
export SCRAPE_TRIGGER=cron

DATE=$(date +%Y-%m-%d)
mkdir -p logs reports data/images data/raw

LOG="logs/$DATE.log"
exec > >(tee -a "$LOG") 2>&1

echo "=== ferret daily run $DATE $(date +%H:%M:%S) ==="

echo "--- 1/4 search (next 5 days, Toyota/Honda/Lexus, hail) ---"
$RUN ferret_copart_search

echo "--- 1b bulk sales-data download → light-ingest nationwide hail inventory ---"
# One CSV (~136k rows) → ranked hail list (lots-salesdata.json) → light rows in
# `lots`. Detail/vision enrich stays gated (top-10% / check-page), NOT auto-run here.
$RUN ferret_copart_sales_data \
  && $PYTHON "$AUTOARB_DIR/ingest_salesdata.py" --file "$DIR/lots-salesdata.json" \
  || echo "  (sales-data step soft-failed — continuing)"

echo "--- 2/4 details + images ---"
$RUN ferret_copart_detail

echo "--- 3/5 damage analysis ---"
$RUN ferret_copart_analyze

echo "--- 4/5 valuations → history (Craigslist, free) ---"
# Free CL comps for every analyzed lot, saved to the valuations table for
# historical comparison. Marketcheck stays manual to protect the 500/mo cap.
$PYTHON "$AUTOARB_DIR/value_lots.py" \
    --lots-file "$DIR/lots-analyzed.json" \
    --data-dir "$DIR/data" \
    --source cl || echo "  (valuation step soft-failed — continuing)"

echo "--- 5/5 report ---"
$RUN ferret_copart_report

# Regenerate index page listing all reports
python3 -c "
import os, glob
reports = sorted(glob.glob('reports/*.html'), reverse=True)
links = '\n'.join(f'<li><a href=\"{os.path.basename(r)}\">{os.path.basename(r)}</a></li>' for r in reports)
open('reports/index.html','w').write(f'<!DOCTYPE html><html><head><meta charset=UTF-8><title>Hail Arb Reports</title><style>body{{font-family:sans-serif;padding:24px;background:#0f172a;color:#e2e8f0}}a{{color:#3b82f6}}li{{margin:6px 0}}</style></head><body><h2>Hail Arb Reports</h2><ul>{links}</ul></body></html>')
print('index updated')
"

# Publish reports to the web root (FastAPI + nginx serve /reports from /var/www/autoarb)
cp -f reports/*.html /var/www/autoarb/ 2>/dev/null && echo 'reports published to web root'

echo "=== done $(date +%H:%M:%S) ==="

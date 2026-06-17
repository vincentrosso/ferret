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

echo "--- 1c enrich TODAY's sales-data auctions → deals auto-watch (blocking) ---"
# Today-only (watches resolve sale-day only). Full enrich → server auto-watches
# the BID/WATCH deals for the fleet. Blocks so it never overlaps the detail step
# below (the box fits one enrich at a time).
$PYTHON "$AUTOARB_DIR/salesdata_enrich.py" --file "$DIR/lots-salesdata.json" \
  || echo "  (sales-data enrich soft-failed — continuing)"

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

# Regenerate the daily-report index (clean dated YYYY-MM-DD.html list) via gen_index.py.
# This is the report listing for the FastAPI /reports/ mount and /ferret/ — NOT the
# site landing page. The landing page at / is the autoarb app shell (index.html,
# deployed from the autoarb repo); daily reports live under /ferret + /reports.
DATES=$(ls reports/2[0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9].html 2>/dev/null | sed 's#.*/##;s#.html##' | tr '\n' ' ')
python3 "$AUTOARB_DIR/gen_index.py" "$DATES" > reports/index.html \
  && echo "reports index updated ($(echo $DATES | wc -w) reports)"

# Publish dated reports + lot pages to the web root, but NEVER the app landing page.
# index.html at the web root is the autoarb app shell — the old `cp -f reports/*.html`
# globbed reports/index.html over it and broke the landing page every morning
# (fixed 2026-06-17). Exclude index.html from the copy.
for f in reports/*.html; do
  [ "$(basename "$f")" = "index.html" ] && continue
  cp -f "$f" /var/www/autoarb/ 2>/dev/null
done
echo 'reports published to web root (app index.html preserved)'

echo "=== done $(date +%H:%M:%S) ==="

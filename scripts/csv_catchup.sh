#!/usr/bin/env bash
# csv_catchup.sh — re-pull the Copart bulk sales CSV when the 5am-PT daily run missed it.
#
# WHY (2026-11-11): daily_run step 1b failed all 3 attempts on 11-10 AND 11-11
# ("Download CSV file" button not found), so sales_history re-read a 49.6h-old CSV and
# the pipeline canary went red. A manual run at 00:57 UTC the same day downloaded
# 145,810 rows first try: the export is FLAKY IN THE 5am-PT SLOT, not dead. daily_run's
# three retries sit 2 min apart inside that same bad window, so they fail together.
#
# This runs hourly from cron and is a no-op unless the CSV is older than STALE_HOURS.
# When it is stale it redoes 1b (download + light ingest), 1b' (sales_history) and
# 1c (enrich today's auctions → auto-watch), i.e. everything the missed CSV starved.
# It never overlaps daily_run: it takes the SAME lock, non-blocking, and skips if held.
export TZ=America/Los_Angeles
exec 9>/tmp/daily_run.lock
flock -n 9 || { echo "$(date +%F\ %T) csv_catchup: daily_run holds the lock — skipping"; exit 0; }
set -euo pipefail
umask 002
DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"
AUTOARB_DIR="${AUTOARB_DIR:-/opt/autoarb}"
set -a
[ -f "$AUTOARB_DIR/.env" ] && source "$AUTOARB_DIR/.env"
[ -f .env ] && source .env
set +a
PYTHON="${AUTOARB_PYTHON:-python3}"
RUN="$PYTHON $AUTOARB_DIR/run_scrape.py"
export SCRAPE_TRIGGER=cron
STALE_HOURS="${STALE_HOURS:-20}"
CSV="$DIR/data/copart-salesdata.csv"

age_h=999
[ -f "$CSV" ] && age_h=$(( ( $(date +%s) - $(stat -c %Y "$CSV") ) / 3600 ))
if [ "$age_h" -lt "$STALE_HOURS" ]; then
  exit 0   # fresh — the normal case, stay silent
fi

mkdir -p logs
LOG="logs/$(date +%Y-%m-%d).log"
exec > >(tee -a "$LOG") 2>&1
echo "=== csv_catchup $(date +%F\ %T) — CSV is ${age_h}h old (>= ${STALE_HOURS}h) ==="

"$DIR/ferret" copart check >/dev/null 2>&1 || {
  echo "  session dead — leaving it to session_keepalive; skipping this hour"; exit 0; }

if ! $RUN ferret_copart_sales_data; then
  echo "  (csv_catchup: download failed — will retry next hour)"; exit 0
fi
$PYTHON "$AUTOARB_DIR/ingest_salesdata.py" --file "$DIR/lots-salesdata.json" \
  || echo "  (salesdata ingest soft-failed — continuing)"
$PYTHON "$AUTOARB_DIR/ingest_sales_history.py" --csv "$CSV" \
  || echo "  (sales_history ingest soft-failed — continuing)"
$PYTHON "$AUTOARB_DIR/salesdata_enrich.py" --file "$DIR/lots-salesdata.json" \
  || echo "  (sales-data enrich soft-failed — continuing)"
echo "=== csv_catchup done $(date +%T) ==="

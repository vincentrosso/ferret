#!/usr/bin/env bash
# Full daily pipeline: search → detail → analyze → value → report
# Runs at 5am PDT via cron on the Hetzner server.
set -euo pipefail

# Group-writable output so the www-data web service (lookup "Analyze", etc.)
# can also write data/raw + data/images. Pairs with setgid on those dirs.
umask 002

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

AUTOARB_DIR="${AUTOARB_DIR:-/opt/autoarb}"
# Source the autoarb .env FIRST as a base (DATABASE_URL, OPENROUTER_API_KEY, …) so
# every shell-out substep — ingest_salesdata.py, salesdata_enrich.py, value_lots.py —
# sees them, then ferret's own .env on top so ferret-specific vars win. This makes
# DATABASE_URL durable instead of requiring a hand-added copy in /opt/ferret/.env
# (the silent KeyError:'DATABASE_URL' that soft-failed the sales-data ingest).
set -a
[ -f "$AUTOARB_DIR/.env" ] && source "$AUTOARB_DIR/.env"
[ -f .env ] && source .env
set +a
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

echo "--- 1b' full sales-data CSV → sales_history (universal spec record, all yards) ---"
# Layer (a) of record-all-auctions: every lot in every sale (~142k rows, not
# hail-filtered) into sales_history, the repo-of-everything. Free (CSV already pulled
# above), additive, ~22s. Realized hammers stay in `hammers`, joined on ltrim.
$PYTHON "$AUTOARB_DIR/ingest_sales_history.py" --csv "$DIR/data/copart-salesdata.csv" \
  || echo "  (sales_history ingest soft-failed — continuing)"

echo "--- 1c enrich TODAY's sales-data auctions → deals auto-watch (blocking) ---"
# Today-only (watches resolve sale-day only). Full enrich → server auto-watches
# the BID/WATCH deals for the fleet. Blocks so it never overlaps the detail step
# below (the box fits one enrich at a time).
$PYTHON "$AUTOARB_DIR/salesdata_enrich.py" --file "$DIR/lots-salesdata.json" \
  || echo "  (sales-data enrich soft-failed — continuing)"

echo "--- 1d today's auction directory (EVERY lane: /public/data/todaysAuctions) ---"
# Authoritative list of every sale running today (live + later) → todays_auctions.
# The watch-coverage feed so the fleet stops missing lanes.
/opt/ferret/ferret copart todays-auctions -cookies /opt/ferret/data/copart-session.json \
  -out "$DIR/todays-auctions.json" \
  && $PYTHON "$AUTOARB_DIR/ingest_auctions.py" --file "$DIR/todays-auctions.json" \
  || echo "  (todays-auctions soft-failed — continuing)"

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

echo "--- 6/6 enrich UPCOMING deals (future days) → verdicts + values + watches ---"
# Runs LAST (after the report, so it isn't delayed): enrich every upcoming-day deal
# in the sales-data dump so each carries a verdict/value (and future-day watches get
# flagged for the fleet). Today's deals were already done in step 1c; this is the
# rest. Already-enriched lots are skipped server-side (ENRICH_TTL_HOURS) so re-runs
# only do new/stale work. Blocking + last → never overlaps another enrich.
$PYTHON "$AUTOARB_DIR/salesdata_enrich.py" --file "$DIR/lots-salesdata.json" \
    --future-only --through-days 14 --max 2000 --timeout-min 360 \
    || echo "  (upcoming-deals enrich soft-failed — continuing)"

echo "--- 6b pre-sale VISION on the top upcoming deals (real repair before the hammer) ---"
# The upcoming enrich above runs vision OFF (default repair $1800), which can flatter
# a heavily-damaged lot (the after-the-fact F-250/Colorado lesson). Run the AI-box
# damage read on the top-N upcoming candidates so their margins rest on a MEASURED
# repair before the sale. Idempotent (reuses each lot's existing vision read), and
# vision is serialized server-side, so it only measures NEW candidates each day.
$PYTHON "$AUTOARB_DIR/vision_pass.py" --min "${VISION_MIN:-1000}" --max "${VISION_MAX:-25000}" \
    --n "${VISION_N:-15}" --days "${VISION_DAYS:-5}" --timeout-min 120 \
    || echo "  (pre-sale vision pass soft-failed — continuing)"

echo "--- refresh hammer_machine_value (captures deals-board values, from the machine) ---"
# Materialize the vehicle_value-machine value per captured (sold) lot so the
# captures.html "deals" board ranks by the machine's coverage, not the sparse
# valuations table. Local lookups, runs in seconds. Picks up the day's new captures.
$PYTHON "$AUTOARB_DIR/build_hammer_values.py" \
    || echo "  (hammer-value refresh soft-failed — continuing)"

echo "=== done $(date +%H:%M:%S) ==="

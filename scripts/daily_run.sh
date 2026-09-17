#!/usr/bin/env bash
# Full daily pipeline: search → detail → analyze → value → report → enrich upcoming
# → pre-sale vision → refresh machine values.
# Runs at 5am PT via cron. The cron fires at 12:00/13:00 UTC gated to "PT hour == 05"
# because this box ignores CRON_TZ; DST-proof (PDT@12, PST@13). NOTE: 5am PT is the
# intended time — do NOT "fix" it back to 1am.
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

# ── step 0: do not run blind ────────────────────────────────────────────────
# `ferret copart check` is a 0.02s local cookie read. The session TTL is ~9h and drifts
# ~9h/day, so its expiry walks into this run's start window roughly every third day —
# on 09-15 it expired at 11:06 UTC and the keepalive caught it at 11:25, 35 minutes
# before this script started. Had that re-login failed both attempts, the ENTIRE run
# would have executed logged out: CSV download fails, bid_eligibility comes back NULL,
# details hit "detail panel not found". That is the 08-31 shape exactly. One cheap check
# converts a silent blind day into a loud one, and a re-login here costs ~40s.
echo "--- 0/8 session check (never run blind) ---"
if ! "$DIR/ferret" copart check >/dev/null 2>&1; then
  echo "  session is dead — re-logging in before the run"
  "$DIR/ferret" copart login -headless -proxy "${SALESHISTORY_PROXY:-}" >/dev/null 2>&1 \
    && chown www-data:www-data "$DIR/data/copart-session.json" 2>/dev/null
  "$DIR/ferret" copart check >/dev/null 2>&1 \
    && echo "  re-login OK" \
    || echo "  ⚠ STILL LOGGED OUT — this run will produce little; every step below is suspect"
else
  echo "  session live"
fi

echo "--- 1/4 search (next 5 days, Toyota/Honda/Lexus, hail) ---"
$RUN ferret_copart_search || echo "  (ferret_copart_search soft-failed — continuing)"

echo "--- 1b bulk sales-data download → light-ingest nationwide hail inventory ---"
# One CSV (~136k rows) → ranked hail list (lots-salesdata.json) → light rows in
# `lots`. Detail/vision enrich stays gated (top-10% / check-page), NOT auto-run here.
# RETRY, because this step going quiet is how the machine goes blind. The download is
# FLAKY, not dead: it failed on 08-23, 08-25 and again on the 12:00 UTC cron run of
# 09-15 ("sales-data page did not render … context deadline exceeded", ~40s), while a
# manual run at 03:09 UTC the same morning rendered the same page in 14s. The 5am-PT
# slot in particular cannot get Copart's export page up. One soft-failed attempt then
# leaves ingest_sales_history re-reading YESTERDAY's CSV and logging a healthy row
# count — the exact 14-day silent outage. Three spaced attempts cost minutes; a missed
# CSV costs a day of deals and says nothing.
csv_ok=0
for attempt in 1 2 3; do
  if $RUN ferret_copart_sales_data; then csv_ok=1; break; fi
  echo "  (sales-data attempt $attempt failed)"
  [ "$attempt" -lt 3 ] && sleep 120
done
if [ "$csv_ok" = 1 ]; then
  $PYTHON "$AUTOARB_DIR/ingest_salesdata.py" --file "$DIR/lots-salesdata.json" \
    || echo "  (salesdata ingest soft-failed — continuing)"
else
  echo "  (sales-data FAILED all 3 attempts — downstream will run on a STALE CSV)"
fi

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
# Soft-fail like every other step: under `set -e` an unguarded non-zero here aborts the
# WHOLE pipeline, so one crashed lot costs the day's analysis, valuations, report AND
# upcoming enrich (2026-08-17: a rod panic at 12:33Z did exactly that — the run had
# already banked dozens of details, and all of it went unused). Details are the ONE step
# whose partial output is still fully usable downstream.
$RUN ferret_copart_detail \
  || echo "  (detail step soft-failed — continuing with whatever details landed)"

echo "--- 3/5 damage analysis ---"
$RUN ferret_copart_analyze || echo "  (ferret_copart_analyze soft-failed — continuing)"

echo "--- 4/5 valuations → history (Craigslist, free) ---"
# Free CL comps for every analyzed lot, saved to the valuations table for
# historical comparison. Marketcheck stays manual to protect the 500/mo cap.
$PYTHON "$AUTOARB_DIR/value_lots.py" \
    --lots-file "$DIR/lots-analyzed.json" \
    --data-dir "$DIR/data" \
    --source cl || echo "  (valuation step soft-failed — continuing)"

echo "--- 5/5 report ---"
$RUN ferret_copart_report || echo "  (ferret_copart_report soft-failed — continuing)"

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
# N covers the actionable BID cohort (~50 deals), not just the top 15 — the top-20
# yesterday-test (2026-06-30) showed the est repair lies on ~25% of cosmetic-labeled
# lots (collision hiding behind a "minor dent" label), so leaving 2/3 of the board on
# the prior shipped bad margins. Idempotency keeps the steady-state cost to the day's
# NEW deals only; the 120-min timeout fits a ~50-lot cold start on the CPU AI box.
$PYTHON "$AUTOARB_DIR/vision_pass.py" --min "${VISION_MIN:-1000}" --max "${VISION_MAX:-25000}" \
    --n "${VISION_N:-60}" --days "${VISION_DAYS:-7}" --timeout-min 180 \
    || echo "  (pre-sale vision pass soft-failed — continuing)"

echo "--- refresh hammer_machine_value (captures deals-board values, from the machine) ---"
# Materialize the vehicle_value-machine value per captured (sold) lot so the
# captures.html "deals" board ranks by the machine's coverage, not the sparse
# valuations table. Local lookups, runs in seconds. Picks up the day's new captures.
$PYTHON "$AUTOARB_DIR/build_hammer_values.py" \
    || echo "  (hammer-value refresh soft-failed — continuing)"

echo "--- warm page caches (so the morning's first page loads are instant w/ fresh data) ---"
# /api/warm pre-computes every cached default view (buys/upcoming/day/captures-deals/
# backtest) in the background — fire-and-forget, completes in ~30s.
curl -s --max-time 30 "http://localhost:8000/api/warm" >/dev/null \
    && echo "  (cache warm triggered)" || echo "  (cache warm soft-failed)"

echo "--- 7a mail canary (proves alerts/digests can still actually be sent) ---"
# Runs BEFORE the sarah email so a dead relay is on the record as the reason the mail
# didn't arrive, rather than a mystery. Roundtrip mode: sends a tokened message to
# ourselves and IMAP-polls until it lands, then reaps it — auth alone can be perfectly
# healthy while mail is silently dropped or DMARC-rejected.
#
# This exists because in 2026 every autoarb email failed for FIVE WEEKS and nothing
# surfaced it: notify.send_mail logs and returns False rather than raising, so a dead
# credential produces no alert BY CONSTRUCTION — the alerting channel cannot alert you
# that the alerting channel is down. Result goes to the DB, then /api/health/mail, then
# a banner on every page via nav.js. Never mail an alert about mail.
$PYTHON "$AUTOARB_DIR/mail_canary.py" --mode roundtrip \
    || echo "  (mail canary soft-failed — continuing)"

echo "--- 7/8 Sarah's RAV4 keeper board (autoarb.ndex.us/sarah) + email ---"
# A PERSONAL daily-driver screen, not the arb model: cosmetic damage only, has to run
# and drive, has to be titleable (cert-of-destruction is a hard kill), any colour but
# black or red. Repair cost and resale margin are deliberately absent — this car gets
# driven, not flipped. See memory feedback_keeper_vs_arb_criteria.
#
# It detail-scrapes its own handful of candidates first: bid eligibility is written
# ONLY by the detail scrape (the bulk CSV never carries it), so without that the
# can-bid gate would be pure decoration. Runs LAST, after the upcoming enrich, so it
# reads the freshest inventory. Mails only when something NEW turned up.
#
# Soft-failed like every other step — a personal board must never be able to kill the
# pipeline. (See the 2026-08-17 lesson: the ONE unguarded step cost a whole day.)
$PYTHON "$AUTOARB_DIR/sarah_page.py" \
    --out /var/www/autoarb/sarah/index.html \
    --email "${SARAH_EMAIL:-pmbou@hotmail.com,vincentrosso@gmail.com}" \
    || echo "  (sarah board soft-failed — continuing)"

echo "--- 8/8 Thomas's Camaro SS board (autoarb.ndex.us/thomas) + email ---"
# The second personal keeper screen, sharing keeper_common.py with Sarah's. Same
# inverted criteria (drives, titleable, cosmetic damage; repair and resale margin
# deliberately absent) with two differences that matter:
#
#   * V8 ONLY. Vincent's call, 2026-09-15, overriding the V6 recommendation. Trim is
#     not a column anywhere — model_group reads "CAMARO" for a base turbo-four and a
#     ZL1 alike — so the SS gate is a VIN decode inside thomas_page.py.
#   * It goes to ORANGE COUNTY, CA, not Stillwater, so the transport bands run the
#     expensive direction and CA's smog + revived-salvage rules apply.
#
# Like Sarah's, it detail-scrapes its own shortlist first: that is the only source of
# bid eligibility AND of the descriptive title, where "Title Absent" hides — a clean
# title can be absent, and those are exactly the ones that look best.
#
# Soft-failed like every other step — a personal board must never kill the pipeline.
# (See the 2026-08-17 lesson: the ONE unguarded step cost a whole day.)
$PYTHON "$AUTOARB_DIR/thomas_page.py" \
    --out /var/www/autoarb/thomas/index.html \
    --email "${THOMAS_EMAIL:-vincentrosso@gmail.com}" \
    || echo "  (thomas board soft-failed — continuing)"

echo "--- 8a Facebook Marketplace tracker (saved searches → price cuts / sold / gone) ---"
# Scrapes through the residential proxy with a session BORN on that proxy (Facebook kills a
# session on its first use from another IP). If the tracker reports the session dead (exit 3),
# sign in once headless from FB_EMAIL/FB_PASSWORD in /opt/ferret/.env and retry — ONCE: a
# checkpoint needs a phone approval, and hammering it is how an account gets locked.
fb_track() { ( cd "$AUTOARB_DIR" && FERRET_BIN=/opt/ferret/ferret FB_PROXY="${SALESHISTORY_PROXY:-}" $PYTHON marketplace_track.py ); }
fb_rc=0; fb_track || fb_rc=$?
if [ "$fb_rc" = 3 ]; then
  echo "  fb session dead — one headless re-login through the proxy"
  if ( cd /opt/ferret && ./ferret fb login -auto -proxy "${SALESHISTORY_PROXY:-}" ); then
    fb_track || echo "  (marketplace tracker soft-failed after re-login — continuing)"
  else
    echo "  (fb re-login failed — checkpoint? approve on phone; continuing)"
  fi
elif [ "$fb_rc" != 0 ]; then
  echo "  (marketplace tracker soft-failed — continuing)"
fi

# Canary LAST, so it grades the run that just finished. It also runs from
# nightly_review.sh, but that fires at 04:00/05:00 UTC — EIGHT HOURS BEFORE this run —
# so it has only ever measured the previous day. Had it run here on 09-15 it would have
# caught the failed CSV download at 12:32 UTC with zero latency instead of the next
# night. Soft-failed like every sibling: a watchdog must never be able to kill the run.
echo "--- 8/8 pipeline canary (did this run actually produce anything?) ---"
$PYTHON "$AUTOARB_DIR/pipeline_canary.py" \
  || echo "  (pipeline canary soft-failed — continuing)"

echo "=== done $(date +%H:%M:%S) ==="

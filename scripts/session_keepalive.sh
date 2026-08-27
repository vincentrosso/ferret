#!/usr/bin/env bash
# session_keepalive.sh — keep ferret's Copart member session alive.
#
# Copart member sessions run ~8h (measured off g2usersessionid, 2026-08-27 — the
# "~4-6h" this file used to claim was stale). When ferret's lapses, the watchlist
# URL-search, value lookups and the daily run all silently fall back to logged-out
# (public-only) behaviour, so bid_eligibility comes back NULL and can't-bid lots
# render as biddable. This re-logs-in ONLY when the session is actually gone —
# Incapsula rate-limits BURST logins (~7 in 6 min got us walled on 2026-06-15).
#
# ⚠️ THE CHECK USED TO LIE. `ferret copart check` scored the session by loading
# /dashboard and asserting a DOM element — through a residential proxy, against an
# Angular SPA Incapsula may interstitial. It false-negatived ~20 TIMES A DAY
# (17-25/day every day through August) and burned a full headless login each time,
# while the session cookie on disk still had 7+ hours to live. The logins were pure
# cost and actively self-harming: burst logins are the thing that walls us. Fixed in
# ferret — the check is COOKIE-FIRST now (g2usersessionid expiry + the member
# cookie), which is the rule Login() already stated and IsLoggedIn ignored.
#
# The cookie can't see a SERVER-side logout, so one `-deep` probe every 6h keeps
# that gap bounded. Everything else is a local file read: no browser, no proxy.
#
# Cron: hourly. flock prevents overlap with a slow (cooldown-retrying) run.
set -u
cd /opt/ferret || exit 1
set -a; source .env 2>/dev/null; set +a
LOG=/opt/ferret/logs/keepalive.log
PROXY="${SALESHISTORY_PROXY:-}"
ts(){ date -u +"%Y-%m-%dT%H:%M:%SZ"; }
say(){ echo "[$(ts)] $*" >> "$LOG"; }

# This script runs as ROOT (root crontab), but the autoarb FastAPI service — which
# shells out to ferret for every detail/enrich scrape — runs as www-data. ferret's
# login writes data/copart-session.json mode 600 root:root, so www-data then can't
# read it ("permission denied: copart-session.json") and every service-side scrape
# runs LOGGED OUT: no account-specific data renders, so bid_eligibility ("Can't bid")
# comes back empty and can't-bid lots show as biddable. Hand the session file to
# www-data after every write so the service can actually use the logged-in session.
fix_session_perms(){
  [ -f data/copart-session.json ] || return 0
  chown www-data:www-data data/copart-session.json 2>/dev/null || true
  chmod 660 data/copart-session.json 2>/dev/null || true
}

exec 9>/tmp/ferret_keepalive.lock
flock -n 9 || { say "another keepalive run in progress — skip"; exit 0; }

# One real network probe every 6h (00/06/12/18 UTC) catches a server-side logout the
# cookie jar is blind to; the other 5 runs each hour are a local file read.
DEEP=""
case "$(date -u +%H)" in 00|06|12|18) DEEP="-deep" ;; esac

if ./ferret copart check $DEEP -proxy "$PROXY" 2>/dev/null | grep -q "session is valid"; then
  say "session valid${DEEP:+ (deep probe)} — no action"
  fix_session_perms
  exit 0
fi

# Two attempts, long cooldown — NOT three short ones. On a grouchy-Incapsula day
# (2026-07-21: 8/17 hourly re-logins failed on attempt 1) the 3x100s shape was the
# worst of both worlds: it knocked three times inside five minutes — which is the
# burst pattern that walls us — then gave up and went dark for the rest of the hour.
# Nearly every recovery in the log is "attempt 1 fails, attempt 2 succeeds ~2.5min
# later", so it's the COOLDOWN doing the work, not the attempt count. Fewer knocks,
# spaced further apart. Worst case here is ~13 min, still well inside the hourly cron.
ATTEMPTS=2
COOLDOWN=300
say "session expired — attempting re-login"
for attempt in $(seq 1 $ATTEMPTS); do
  timeout 230 ./ferret copart login -headless -proxy "$PROXY" >/dev/null 2>&1
  fix_session_perms
  if ./ferret copart check -deep -proxy "$PROXY" 2>/dev/null | grep -q "session is valid"; then
    say "re-login OK (attempt $attempt)"
    exit 0
  fi
  if [ "$attempt" -lt "$ATTEMPTS" ]; then
    say "re-login attempt $attempt failed — cooldown ${COOLDOWN}s (Incapsula?)"
    sleep "$COOLDOWN"
  else
    say "re-login attempt $attempt failed"
  fi
done
say "re-login FAILED after $ATTEMPTS attempts — will retry next cron"
exit 1

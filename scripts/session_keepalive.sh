#!/usr/bin/env bash
# session_keepalive.sh — keep ferret's Copart member session alive.
#
# Copart member sessions die after ~4-6h; when ferret's lapses, the watchlist
# URL-search, value lookups, and the daily run all silently fall back to
# logged-out (public-only) behaviour. This checks the session through the
# residential proxy and re-logs-in ONLY when it's actually expired — minimising
# Incapsula exposure, since Incapsula rate-limits BURST logins (a spaced single
# attempt every few hours is fine; ~7 in 6 min got us walled on 2026-06-15).
#
# Cron: hourly. flock prevents overlap with a slow (cooldown-retrying) run.
set -u
cd /opt/ferret || exit 1
set -a; source .env 2>/dev/null; set +a
LOG=/opt/ferret/logs/keepalive.log
PROXY="${SALESHISTORY_PROXY:-}"
ts(){ date -u +"%Y-%m-%dT%H:%M:%SZ"; }
say(){ echo "[$(ts)] $*" >> "$LOG"; }

exec 9>/tmp/ferret_keepalive.lock
flock -n 9 || { say "another keepalive run in progress — skip"; exit 0; }

if ./ferret copart check -proxy "$PROXY" 2>/dev/null | grep -q "session is valid"; then
  say "session valid — no action"
  exit 0
fi

say "session expired — attempting re-login"
for attempt in 1 2 3; do
  timeout 230 ./ferret copart login -headless -proxy "$PROXY" >/dev/null 2>&1
  if ./ferret copart check -proxy "$PROXY" 2>/dev/null | grep -q "session is valid"; then
    say "re-login OK (attempt $attempt)"
    exit 0
  fi
  say "re-login attempt $attempt failed — cooldown 100s (Incapsula?)"
  sleep 100
done
say "re-login FAILED after 3 attempts — will retry next cron"
exit 1

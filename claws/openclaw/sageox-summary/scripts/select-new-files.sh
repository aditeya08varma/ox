#!/usr/bin/env bash
# select-new-files.sh — print distilled-daily filenames that fall in the last
# 24h window (UTC date prefix) and have NOT yet been included in a prior
# summary run.
#
# Usage: select-new-files.sh <team_daily_dir> <team_id> <state_file>
#
# Arguments:
#   <team_daily_dir>  Absolute path to <team_context_dir>/memory/daily/
#   <team_id>         Team id key used inside the state file's .teams map
#   <state_file>      Absolute path to sageox-summary-state.json (need not
#                     exist — a missing file is treated as empty state)
#
# Stdout: one basename per line, sorted ascending. Empty if nothing new.
# Stderr: single-line warnings (e.g. malformed state file).
# Exit:
#   0 — success (empty output is not a failure)
#   2 — usage error (wrong arg count)
#   3 — internal error (required tool missing)

set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $(basename "$0") <team_daily_dir> <team_id> <state_file>" >&2
  exit 2
fi

TEAM_DAILY_DIR="$1"
TEAM_ID="$2"
STATE_FILE="$3"

command -v jq >/dev/null 2>&1 || { echo "error: jq is required" >&2; exit 3; }

# BSD (macOS) uses -v; GNU (Linux) uses -d. Try BSD first.
TODAY_UTC="$(date -u +%Y-%m-%d)"
YESTERDAY_UTC="$(date -u -v-1d +%Y-%m-%d 2>/dev/null \
  || date -u -d 'yesterday' +%Y-%m-%d)"

FILENAME_RE='^[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f-]+\.md$'

# Candidate set: basenames whose date prefix is TODAY_UTC or YESTERDAY_UTC
# and whose full shape matches the distill-output regex. A missing daily
# dir is not fatal — the caller skips the team when output is empty.
CANDIDATES=""
if [ -d "$TEAM_DAILY_DIR" ]; then
  CANDIDATES="$(
    ( cd "$TEAM_DAILY_DIR" && ls -1 2>/dev/null ) \
      | grep -E "^(${TODAY_UTC}|${YESTERDAY_UTC})-[0-9a-f-]+\.md$" \
      | sort || true
  )"
fi

# Already-summarized set for this team.
# Missing state file → empty (first run, normal).
# Malformed state file → empty + stderr warning (Step 6 will rewrite it).
ALREADY_INCLUDED=""
if [ -f "$STATE_FILE" ]; then
  if ! jq -e . "$STATE_FILE" >/dev/null 2>&1; then
    echo "warning: $STATE_FILE was unreadable, starting from empty state" >&2
  else
    ALREADY_INCLUDED="$(
      jq -r --arg tid "$TEAM_ID" \
        '.teams[$tid].included_files[]? // empty' "$STATE_FILE" \
      | grep -E "$FILENAME_RE" \
      | sort -u || true
    )"
  fi
fi

# new_files = CANDIDATES − ALREADY_INCLUDED
# Both sides are already sorted and regex-validated. Write each side to a
# temp file with empty lines stripped, then let `comm -23` do the set
# difference. Using temp files avoids BSD awk's rejection of multi-line
# values in `-v var=value`.
CAND_TMP="$(mktemp)"
INCL_TMP="$(mktemp)"
trap 'rm -f "$CAND_TMP" "$INCL_TMP" 2>/dev/null' EXIT

printf '%s\n' "$CANDIDATES"       | grep -v '^$' > "$CAND_TMP" || true
printf '%s\n' "$ALREADY_INCLUDED" | grep -v '^$' > "$INCL_TMP" || true

comm -23 "$CAND_TMP" "$INCL_TMP"

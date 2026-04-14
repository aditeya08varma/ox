#!/usr/bin/env bash
# update-ox.sh — non-fatal auto-update for the git install method, and
# the "is ox installed at all?" check for every invocation of this skill.
#
# Reads ~/.openclaw/memory/sageox-ox-install.json. If the install method
# is git with auto_update enabled, runs git pull + make install from the
# recorded clone path. Falls back silently to the existing binary on any
# failure (validation, network, build) — a bad update must never block
# a working invocation.
#
# Usage: update-ox.sh
#
# Stdout: nothing on success
# Stderr: one-line warnings on failure modes; the "needs install" signal
# Exit:
#   0 — ox is ready, agent should proceed to next prerequisite
#   2 — install state file is missing; agent must read
#       references/INSTALL.md and run the interactive install flow
#       before continuing

set -euo pipefail

STATE_FILE="$HOME/.openclaw/memory/sageox-ox-install.json"

if [ ! -f "$STATE_FILE" ]; then
  echo "ox install state not configured — run interactive install flow from references/INSTALL.md" >&2
  exit 2
fi

# Without jq we can't read the state file, but ox itself may still be
# on PATH from a prior install. Don't fail — let the caller verify.
if ! command -v jq >/dev/null 2>&1; then
  echo "warning: jq not found; cannot read $STATE_FILE; skipping update check" >&2
  exit 0
fi

INSTALL_METHOD="$(jq -r '.install_method // empty' "$STATE_FILE" 2>/dev/null || true)"
if [ -z "$INSTALL_METHOD" ]; then
  echo "warning: $STATE_FILE has no install_method; skipping update check" >&2
  exit 0
fi

# Curl install: nothing to update on a per-run basis. The user re-runs
# the interactive flow to bump the pinned release.
if [ "$INSTALL_METHOD" = "curl" ]; then
  exit 0
fi

if [ "$INSTALL_METHOD" != "git" ]; then
  echo "warning: unknown install_method '$INSTALL_METHOD' in $STATE_FILE; skipping update check" >&2
  exit 0
fi

AUTO_UPDATE="$(jq -r '.auto_update // false' "$STATE_FILE")"
if [ "$AUTO_UPDATE" != "true" ]; then
  exit 0
fi

CLONE_PATH="$(jq -r '.clone_path // empty' "$STATE_FILE")"
if [ -z "$CLONE_PATH" ]; then
  echo "warning: $STATE_FILE missing clone_path; skipping auto-update" >&2
  exit 0
fi

# Re-validate the clone path. The state file is user-writable and may
# have been edited externally between runs — never trust persisted
# values without re-checking. Validation failure is non-fatal: skip the
# update and fall back to the existing ox binary.
case "$CLONE_PATH" in
  "$HOME"/*) ;;
  *)
    echo "warning: clone_path not under \$HOME; skipping auto-update: $CLONE_PATH" >&2
    exit 0
    ;;
esac
case "$CLONE_PATH" in
  *..*)
    echo "warning: clone_path contains '..'; skipping auto-update: $CLONE_PATH" >&2
    exit 0
    ;;
esac
for ch in ';' '$' '`' '|' '&' '<' '>' '(' ')' '{' '}' '*' '?' '[' ']' '!' '\'; do
  case "$CLONE_PATH" in
    *"$ch"*)
      echo "warning: clone_path contains shell metacharacter; skipping auto-update" >&2
      exit 0
      ;;
  esac
done
case "$CLONE_PATH" in
  *"
"*)
    echo "warning: clone_path contains a newline; skipping auto-update" >&2
    exit 0
    ;;
esac
if [ ! -d "$CLONE_PATH/.git" ]; then
  echo "warning: clone_path missing or not a git repo; skipping auto-update: $CLONE_PATH" >&2
  exit 0
fi

# Run the update. Non-fatal on failure — fall back to existing binary.
# Capture output so we can show useful tail-of-log on failure without
# polluting stdout on the success path.
LOG="$(mktemp "${TMPDIR:-/tmp}/ox-update.XXXXXXXX")"
trap 'rm -f "$LOG" 2>/dev/null' EXIT
if ! ( cd "$CLONE_PATH" && git pull --ff-only && make build && make install ) > "$LOG" 2>&1; then
  echo "warning: ox auto-update failed; continuing with existing binary" >&2
  echo "--- last 10 lines of update log ---" >&2
  tail -10 "$LOG" >&2
fi

exit 0

#!/usr/bin/env bash
# update-ox.sh — non-fatal auto-update for the git install method, and
# the "is ox installed at all?" check for every invocation of this skill.
#
# Reads ~/.openclaw/memory/sageox-ox-install.json. If the install method
# is git with auto_update enabled, resolves the recorded clone_path to
# its physical location, verifies ownership, and runs git pull + make
# install there. Falls back silently to the existing binary on any
# failure (validation, network, build) — a bad update must never block
# a working invocation.
#
# Before returning `0`, always re-confirms that `ox` is actually on the
# subprocess PATH. A stale state file + a missing PATH entry is a common
# failure mode on the curl install path and on git installs whose
# ~/.openclaw/.env was lost, and reporting "ready" in either case would
# just push the failure to the next `ox status` call.
#
# Usage: update-ox.sh
#
# Stdout: nothing on success
# Stderr: one-line warnings on failure modes; the "needs install" signal
# Exit:
#   0 — ox is ready, agent should proceed to next prerequisite
#   2 — ox is not usable (no state file, or state says installed but
#       'ox' is not on PATH); agent must read references/INSTALL.md and
#       run the interactive install flow before continuing

set -euo pipefail

STATE_FILE="$HOME/.openclaw/memory/sageox-ox-install.json"

if [ ! -f "$STATE_FILE" ]; then
  echo "ox install state not configured — run interactive install flow from references/INSTALL.md" >&2
  exit 2
fi

# Resolve $HOME to its physical path once — used by the symlink-escape
# check below. If $HOME itself is under a symlink (e.g. /Users/foo →
# /Volumes/Data/Users/foo), the real clone_path must still resolve under
# the resolved $HOME, not the textual one.
REAL_HOME="$(cd "$HOME" && pwd -P)"

# Wrap the whole git-mode update path in a function so that early-exit
# on any validation failure falls THROUGH to the final command -v ox
# readiness gate at the bottom of the script. Using `return` here (not
# `exit 0`) is what makes the gate load-bearing.
try_git_update() {
  local clone_path real_clone_path ch log
  clone_path="$(jq -r '.clone_path // empty' "$STATE_FILE")"
  if [ -z "$clone_path" ]; then
    echo "warning: $STATE_FILE missing clone_path; skipping auto-update" >&2
    return
  fi

  # Text-level validation. Cheap to run; catches obvious hand-edits
  # before we go near the filesystem.
  case "$clone_path" in
    "$HOME"/*) ;;
    *)
      echo "warning: clone_path not under \$HOME; skipping auto-update: $clone_path" >&2
      return
      ;;
  esac
  case "$clone_path" in
    *..*)
      echo "warning: clone_path contains '..'; skipping auto-update: $clone_path" >&2
      return
      ;;
  esac
  for ch in ';' '$' '`' '|' '&' '<' '>' '(' ')' '{' '}' '*' '?' '[' ']' '!' '"' '\'; do
    case "$clone_path" in
      *"$ch"*)
        echo "warning: clone_path contains shell metacharacter; skipping auto-update" >&2
        return
        ;;
    esac
  done
  case "$clone_path" in
    *"
"*)
      echo "warning: clone_path contains a newline; skipping auto-update" >&2
      return
      ;;
  esac

  # Physical path resolution. `cd && pwd -P` is the portable way to
  # follow every symlink — works on BSD/macOS and GNU/Linux without
  # depending on `readlink -f` or `realpath`, neither of which are
  # guaranteed on older macOS. If the path doesn't exist or we can't
  # enter it, treat the recorded state as stale and skip the update.
  local real_clone_path
  if ! real_clone_path="$(cd "$clone_path" 2>/dev/null && pwd -P)"; then
    echo "warning: clone_path does not exist or is inaccessible; skipping auto-update: $clone_path" >&2
    return
  fi

  # Resolved clone path must still be under the resolved $HOME — this
  # is the check that catches `~/link → /tmp/foo` symlink escapes.
  case "$real_clone_path" in
    "$REAL_HOME"/*) ;;
    *)
      echo "warning: clone_path escapes \$HOME via symlink; skipping auto-update: $real_clone_path" >&2
      return
      ;;
  esac

  # Ownership check. `test -O` returns true iff the file is owned by
  # the effective user ID — POSIX, portable across BSD and GNU. This
  # blocks the case where a symlink inside $HOME points to a directory
  # owned by root or another user (shared /opt, /var, etc.).
  if [ ! -O "$real_clone_path" ]; then
    echo "warning: clone_path not owned by current user; skipping auto-update: $real_clone_path" >&2
    return
  fi

  if [ ! -d "$real_clone_path/.git" ]; then
    echo "warning: resolved clone_path missing .git subdirectory; skipping auto-update: $real_clone_path" >&2
    return
  fi

  # Run the update from the RESOLVED path. Non-fatal on failure — fall
  # back to the existing binary. Capture output so we can show a useful
  # tail-of-log on failure without polluting stdout on the success path.
  log="$(mktemp "${TMPDIR:-/tmp}/ox-update.XXXXXXXX")"
  trap 'rm -f "$log" 2>/dev/null' RETURN
  if ! ( cd "$real_clone_path" && git pull --ff-only && make build && make install ) > "$log" 2>&1; then
    echo "warning: ox auto-update failed; continuing with existing binary" >&2
    echo "--- last 10 lines of update log ---" >&2
    tail -10 "$log" >&2
  fi
}

# Without jq we can't read the state file, but ox itself may still be
# on PATH from a prior install. Don't fail — let the final readiness
# gate decide.
if ! command -v jq >/dev/null 2>&1; then
  echo "warning: jq not found; cannot read $STATE_FILE; skipping update check" >&2
else
  INSTALL_METHOD="$(jq -r '.install_method // empty' "$STATE_FILE" 2>/dev/null || true)"
  if [ -z "$INSTALL_METHOD" ]; then
    echo "warning: $STATE_FILE has no install_method; skipping update check" >&2
  elif [ "$INSTALL_METHOD" = "curl" ]; then
    : # Nothing to update on a per-run basis. The user re-runs the
      # interactive flow to bump the pinned release.
  elif [ "$INSTALL_METHOD" = "git" ]; then
    AUTO_UPDATE="$(jq -r '.auto_update // false' "$STATE_FILE")"
    if [ "$AUTO_UPDATE" = "true" ]; then
      try_git_update
    fi
  else
    echo "warning: unknown install_method '$INSTALL_METHOD' in $STATE_FILE; skipping update check" >&2
  fi
fi

# Final readiness gate. Whatever install method the state file records,
# `ox` must actually be on the subprocess PATH for the rest of the
# skill to function. A stale state file + a misconfigured ~/.openclaw/.env
# is a common failure mode that used to only surface on the next
# `ox status` call — make it surface here instead, with actionable
# next-step guidance.
if ! command -v ox >/dev/null 2>&1; then
  echo "error: 'ox' is not on PATH (install state: ${INSTALL_METHOD:-unknown})" >&2
  echo "fix: re-read references/INSTALL.md to re-run install or update ~/.openclaw/.env" >&2
  exit 2
fi
exit 0

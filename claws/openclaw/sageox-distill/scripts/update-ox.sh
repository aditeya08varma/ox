#!/usr/bin/env bash
# update-ox.sh — "is ox installed?" readiness gate for every invocation
# of this skill.
#
# Reads ~/.openclaw/memory/sageox-ox-install.json to confirm the install
# state exists, then verifies `ox` is actually on the subprocess PATH.
# The curl install path has no per-run update — users re-run
# install-ox-curl.sh to bump the pinned release.
#
# Usage: update-ox.sh
#
# Stdout: nothing on success
# Stderr: one-line "needs install" or "not on PATH" signal
# Exit:
#   0 — ox is ready, agent should proceed to the next prerequisite
#   2 — ox is not usable (no state file, or state file records ox as
#       installed but it isn't on PATH); agent must read
#       references/INSTALL.md and run the install flow before continuing

set -euo pipefail

STATE_FILE="$HOME/.openclaw/memory/sageox-ox-install.json"

if [ ! -f "$STATE_FILE" ]; then
  echo "ox install state not configured — run install flow from references/INSTALL.md" >&2
  exit 2
fi

# Readiness gate. `ox` must actually be on the subprocess PATH for the
# rest of the skill to function. A stale state file + a misconfigured
# ~/.openclaw/.env is a common failure mode that used to only surface on
# the next `ox status` call — make it surface here instead, with
# actionable next-step guidance.
if ! command -v ox >/dev/null 2>&1; then
  echo "error: 'ox' is not on PATH" >&2
  echo "fix: re-run references/INSTALL.md, or add \$HOME/.local/bin to PATH in ~/.openclaw/.env" >&2
  exit 2
fi
exit 0

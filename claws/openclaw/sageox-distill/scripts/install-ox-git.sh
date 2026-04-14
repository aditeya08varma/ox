#!/usr/bin/env bash
# install-ox-git.sh — clone sageox/ox to a user-chosen path, build and
# install via `make install`, detect Go's bin/install dirs for PATH
# guidance, and persist the chosen install method to the OpenClaw
# memory file.
#
# Usage: install-ox-git.sh <clone_path> <auto_update>
#
# Arguments:
#   <clone_path>   Absolute path under $HOME where the repo will live.
#                  The agent has already prompted the user and validated
#                  this against the Path validation rules in SKILL.md
#                  § 3 — this script re-checks defensively.
#   <auto_update>  Literal string "true" or "false" — whether to run
#                  git pull + make install on every future invocation.
#
# Stdout: human-readable progress (clone, build, detected paths)
# Stderr: errors and PATH-configuration guidance if ox is not on this
#         subprocess's PATH after install
# Exit:
#   0 — success, ox installed and memory file written
#   2 — usage error (wrong arg count or bad arg value)
#   3 — internal error (Go missing or too old, build failed)

set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $(basename "$0") <clone_path> <auto_update>" >&2
  exit 2
fi

CLONE_PATH="$1"
AUTO_UPDATE="$2"

case "$AUTO_UPDATE" in
  true|false) ;;
  *)
    echo "error: <auto_update> must be 'true' or 'false', got '$AUTO_UPDATE'" >&2
    exit 2
    ;;
esac

# Defensive path re-validation. The agent has already validated this
# against the full Path validation rules in SKILL.md § 3 and re-prompted
# on failure; this script only guards against being called with bad
# input from a buggy caller or a hand-edited script invocation.
case "$CLONE_PATH" in
  "$HOME"/*) ;;
  *)
    echo "error: clone path must be absolute and under \$HOME: $CLONE_PATH" >&2
    exit 2
    ;;
esac
case "$CLONE_PATH" in
  *..*)
    echo "error: clone path must not contain '..': $CLONE_PATH" >&2
    exit 2
    ;;
esac
for ch in ';' '$' '`' '|' '&' '<' '>' '(' ')' '{' '}' '*' '?' '[' ']' '!' '\'; do
  case "$CLONE_PATH" in
    *"$ch"*)
      echo "error: clone path contains shell metacharacter: $CLONE_PATH" >&2
      exit 2
      ;;
  esac
done
case "$CLONE_PATH" in
  *"
"*)
    echo "error: clone path contains a newline: $CLONE_PATH" >&2
    exit 2
    ;;
esac

# Verify Go ≥ 1.24. The agent prose handles the user-facing "go install"
# prompt; this is the executable check.
if ! command -v go >/dev/null 2>&1; then
  echo "error: go is required (≥ 1.24) but is not on PATH" >&2
  exit 3
fi
GO_VERSION="$(go version | awk '{print $3}' | sed 's/^go//')"
GO_MAJOR="${GO_VERSION%%.*}"
GO_REST="${GO_VERSION#*.}"
GO_MINOR="${GO_REST%%.*}"
# Strip any trailing -beta / -rc suffix from the minor.
GO_MINOR="${GO_MINOR%%[^0-9]*}"
if [ -z "$GO_MAJOR" ] || [ -z "$GO_MINOR" ]; then
  echo "error: could not parse go version: $GO_VERSION" >&2
  exit 3
fi
if [ "$GO_MAJOR" -lt 1 ] || { [ "$GO_MAJOR" -eq 1 ] && [ "$GO_MINOR" -lt 24 ]; }; then
  echo "error: go ≥ 1.24 required, found $GO_VERSION" >&2
  exit 3
fi

mkdir -p "$(dirname "$CLONE_PATH")"
if [ ! -d "$CLONE_PATH/.git" ]; then
  git clone https://github.com/sageox/ox.git "$CLONE_PATH"
fi

# Build and install in a subshell so the cd doesn't leak.
( cd "$CLONE_PATH" && make build && make install )

# Detect Go's paths for the PATH guidance below.
GO_BIN_DIR="$(dirname "$(command -v go)")"
GO_INSTALL_DIR="$(go env GOBIN)"
[ -z "$GO_INSTALL_DIR" ] && GO_INSTALL_DIR="$HOME/go/bin"

# Persist the memory file.
mkdir -p "$HOME/.openclaw/memory"
INSTALLED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cat > "$HOME/.openclaw/memory/sageox-ox-install.json" <<EOF
{
  "install_method": "git",
  "clone_path": "$CLONE_PATH",
  "auto_update": $AUTO_UPDATE,
  "go_bin_dir": "$GO_BIN_DIR",
  "go_install_dir": "$GO_INSTALL_DIR",
  "installed_at": "$INSTALLED_AT"
}
EOF

# Verify ox is reachable from this subprocess. If not, print copy-paste
# guidance for ~/.openclaw/.env as a stderr warning — the install itself
# succeeded, but the user has follow-up to do.
if ! command -v ox >/dev/null 2>&1; then
  cat >&2 <<EOF
warning: ox built and installed, but is not on this subprocess's PATH.
Add this line to ~/.openclaw/.env and restart the skill:

  PATH=$GO_BIN_DIR:/usr/local/bin:/usr/bin:/bin:$GO_INSTALL_DIR

OpenClaw loads .env into the skill subprocess, so the updated PATH
will take effect on the next invocation.
EOF
fi

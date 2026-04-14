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
#                  § 3 — this script re-checks defensively, then
#                  physically resolves the parent directory (via
#                  `cd && pwd -P`) to reject symlink escapes before
#                  touching the filesystem.
#   <auto_update>  Literal string "true" or "false" — whether to run
#                  git pull + make install on every future invocation.
#
# Stdout: human-readable progress (clone, build, detected paths)
# Stderr: errors and PATH-configuration guidance if ox is not on this
#         subprocess's PATH after install
# Exit:
#   0 — success, ox installed and memory file written
#   2 — usage error (wrong arg count, bad arg value, path validation
#       failure, or symlink escape)
#   3 — internal error (Go or jq missing or too old, build failed)

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

# Defensive text-level path re-validation. The agent has already
# validated this against the full Path validation rules in SKILL.md § 3
# and re-prompted on failure; this script only guards against being
# called with bad input from a buggy caller or a hand-edited invocation.
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
for ch in ';' '$' '`' '|' '&' '<' '>' '(' ')' '{' '}' '*' '?' '[' ']' '!' '"' '\'; do
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

# Required tools. `jq` is used below to emit the install state as JSON
# (a heredoc would mis-encode any filesystem path containing `"` or
# `\`); `go` must be ≥ 1.24 for `make build`.
if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required but is not on PATH" >&2
  exit 3
fi

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

# Physical path resolution. The textual `case "$HOME/*"` check above
# only validates the prefix string — a path like `$HOME/link` where
# `link → /tmp/foo` would pass that check while the real clone target
# sits outside $HOME. Resolve the path physically and verify ownership
# before we create or write anything.
#
# `cd && pwd -P` is the portable resolver — works on BSD/macOS and
# GNU/Linux with no `readlink -f` / `realpath` dependency.
#
# Critically, we must validate BEFORE `mkdir -p` runs. A symlinked
# ancestor would otherwise let `mkdir -p` create directories in a
# foreign tree BEFORE the physical check has a chance to reject the
# path. Walk up from PARENT_DIR to the deepest existing ancestor,
# resolve IT, and validate the resolved location there — anything
# mkdir creates beneath a validated ancestor is guaranteed to stay
# inside it, because mkdir never creates symlinks.
REAL_HOME="$(cd "$HOME" && pwd -P)"
PARENT_DIR="$(dirname "$CLONE_PATH")"

ANCESTOR="$PARENT_DIR"
while [ ! -d "$ANCESTOR" ]; do
  NEXT="$(dirname "$ANCESTOR")"
  if [ "$NEXT" = "$ANCESTOR" ]; then
    echo "error: no existing ancestor found for parent directory: $PARENT_DIR" >&2
    exit 2
  fi
  ANCESTOR="$NEXT"
done
REAL_ANCESTOR="$(cd "$ANCESTOR" && pwd -P)"
case "$REAL_ANCESTOR" in
  "$REAL_HOME"|"$REAL_HOME"/*) ;;
  *)
    echo "error: ancestor directory escapes \$HOME via symlink: $REAL_ANCESTOR" >&2
    exit 2
    ;;
esac
if [ ! -O "$REAL_ANCESTOR" ]; then
  echo "error: ancestor directory not owned by current user: $REAL_ANCESTOR" >&2
  exit 2
fi

# Safe to create missing parents now. mkdir -p creates plain
# directories under REAL_ANCESTOR, which we've already confirmed is
# under $HOME and owned by the user.
mkdir -p "$PARENT_DIR"
REAL_PARENT="$(cd "$PARENT_DIR" && pwd -P)"

# Reassemble the canonical clone path from the resolved parent + the
# basename. We use this canonical form everywhere below (clone target,
# build subshell, state file) so update-ox.sh re-reads a symlink-free
# path on every future run.
REAL_CLONE_PATH="$REAL_PARENT/$(basename "$CLONE_PATH")"

# Reject a symlinked final path component. A symlink at REAL_CLONE_PATH
# could point at a user-owned sageox/ox checkout anywhere on disk — the
# `test -O` and `git -C` checks below both follow symlinks, so they'd
# both pass, and `make install` would run in the escaped tree while
# the state file recorded only the alias. Policy: the clone target is
# either a plain directory we create, or an existing plain directory
# we adopt. Never a symlink. Users who want to reorganize their clone
# should move it and re-run the interactive install.
if [ -L "$REAL_CLONE_PATH" ]; then
  echo "error: clone target must not be a symlink: $REAL_CLONE_PATH" >&2
  exit 2
fi

if [ ! -d "$REAL_CLONE_PATH/.git" ]; then
  git clone https://github.com/sageox/ox.git "$REAL_CLONE_PATH"
fi

# Defense-in-depth canonicalization. The ancestor validation above
# guarantees REAL_PARENT is physical, and the -L check immediately
# above rejects a symlinked leaf, so `cd && pwd -P` here should be a
# no-op — but run it anyway and re-check against $REAL_HOME so that
# any residual aliasing (e.g., a future change that adds a
# code path that doesn't go through the walk-up) still fails closed.
REAL_CLONE_PATH="$(cd "$REAL_CLONE_PATH" && pwd -P)"
case "$REAL_CLONE_PATH" in
  "$REAL_HOME"/*) ;;
  *)
    echo "error: clone target escapes \$HOME via symlink: $REAL_CLONE_PATH" >&2
    exit 2
    ;;
esac

# After clone (or existing-dir detection), the target itself must be
# owned by the current user. `git clone` inherits parent-directory
# ownership by default, but this catches the case where someone
# pre-created the target as a directory owned by another user (shared
# /opt, /var, etc.).
if [ ! -O "$REAL_CLONE_PATH" ]; then
  echo "error: clone target not owned by current user: $REAL_CLONE_PATH" >&2
  exit 2
fi

# Defense-in-depth: confirm this is actually a sageox/ox checkout
# before running its Makefile. If the caller pointed us at a
# pre-existing user-owned git repo that happens to live at
# REAL_CLONE_PATH, the existing-dir branch above would skip `git
# clone` and run THAT repo's `make install` — a code-execution
# footgun. A fresh clone a few lines up guarantees the origin is
# correct, but the existing-dir branch and "switch ox install method"
# re-runs do not.
#
# Accept the three canonical origin forms: HTTPS, scp-style SSH, and
# URI-style SSH (with and without the `.git` suffix). `git remote
# get-url` emits exactly what's stored in config — it does NOT
# normalize between scp-style and URI-style — so the allow-list has
# to cover the variants users actually configure.
ORIGIN_URL="$(git -C "$REAL_CLONE_PATH" remote get-url origin 2>/dev/null || true)"
case "$ORIGIN_URL" in
  https://github.com/sageox/ox|https://github.com/sageox/ox.git) ;;
  git@github.com:sageox/ox|git@github.com:sageox/ox.git) ;;
  ssh://git@github.com/sageox/ox|ssh://git@github.com/sageox/ox.git) ;;
  *)
    echo "error: $REAL_CLONE_PATH is not a sageox/ox checkout (origin: ${ORIGIN_URL:-<none>})" >&2
    exit 2
    ;;
esac

# Build and install in a subshell so the cd doesn't leak.
( cd "$REAL_CLONE_PATH" && make build && make install )

# Detect Go's paths for the PATH guidance below.
GO_BIN_DIR="$(dirname "$(command -v go)")"
GO_INSTALL_DIR="$(go env GOBIN)"
[ -z "$GO_INSTALL_DIR" ] && GO_INSTALL_DIR="$HOME/go/bin"

# Persist the memory file via `jq -n` so that path values containing
# `"` or `\` (or any other JSON-special character) cannot produce
# malformed state that `update-ox.sh` then fails to parse. --argjson
# for auto_update lets us pass the literal true/false through without
# re-quoting.
mkdir -p "$HOME/.openclaw/memory"
INSTALLED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq -n \
  --arg install_method "git" \
  --arg clone_path "$REAL_CLONE_PATH" \
  --argjson auto_update "$AUTO_UPDATE" \
  --arg go_bin_dir "$GO_BIN_DIR" \
  --arg go_install_dir "$GO_INSTALL_DIR" \
  --arg installed_at "$INSTALLED_AT" \
  '{
    install_method: $install_method,
    clone_path: $clone_path,
    auto_update: $auto_update,
    go_bin_dir: $go_bin_dir,
    go_install_dir: $go_install_dir,
    installed_at: $installed_at
  }' > "$HOME/.openclaw/memory/sageox-ox-install.json"

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

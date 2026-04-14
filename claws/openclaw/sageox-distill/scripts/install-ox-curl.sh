#!/usr/bin/env bash
# install-ox-curl.sh — download and run the official ox install script
# from a pinned release tag, then persist the chosen install method to
# the OpenClaw memory file.
#
# Usage: install-ox-curl.sh
#
# Stdout: human-readable progress (download URL, head/tail preview,
#         install script output)
# Stderr: errors
# Exit:
#   0 — success, ox installed and memory file written
#   3 — internal error (curl missing, install script failed, ox not on
#       PATH after install)
#
# The pinned release tag below makes this script reproducible: future
# upstream commits to install.sh on main can't change what this skill
# fetches. Bump OX_INSTALL_REF when a newer sageox/ox release ships.

set -euo pipefail

OX_INSTALL_REF="v0.6.1"
INSTALL_URL="https://raw.githubusercontent.com/sageox/ox/${OX_INSTALL_REF}/scripts/install.sh"
SOURCE_BLOB="https://github.com/sageox/ox/blob/${OX_INSTALL_REF}/scripts/install.sh"

command -v curl >/dev/null 2>&1 || { echo "error: curl is required" >&2; exit 3; }

# Use the template form for mktemp — portable across GNU (Linux) and BSD (macOS).
INSTALL_SCRIPT="$(mktemp "${TMPDIR:-/tmp}/ox-install.XXXXXXXX")"
trap 'rm -f "$INSTALL_SCRIPT" 2>/dev/null' EXIT

# -f makes curl fail on HTTP errors instead of executing an HTML error page;
# --max-time bounds a stalled connection.
curl -fsSL --max-time 60 "$INSTALL_URL" -o "$INSTALL_SCRIPT"

# Surface what is about to run so the user can sanity-check it.
echo "Downloaded ox install script:"
echo "  Source: $SOURCE_BLOB"
ls -lh "$INSTALL_SCRIPT"
echo "--- first 5 lines ---"
head -5 "$INSTALL_SCRIPT"
echo "--- last 5 lines ---"
tail -5 "$INSTALL_SCRIPT"

# Execute.
bash "$INSTALL_SCRIPT"

# Verify the result before we persist anything.
if ! command -v ox >/dev/null 2>&1; then
  echo "error: ox install script ran but 'ox' is not on PATH" >&2
  exit 3
fi

# Persist the install method.
mkdir -p "$HOME/.openclaw/memory"
INSTALLED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cat > "$HOME/.openclaw/memory/sageox-ox-install.json" <<EOF
{
  "install_method": "curl",
  "ox_install_ref": "$OX_INSTALL_REF",
  "installed_at": "$INSTALLED_AT"
}
EOF

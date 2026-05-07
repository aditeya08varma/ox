#!/usr/bin/env bash
# deploy-to-vps.sh — rsync the sageox skill to the VPS for testing.
# NOT published to ClawHub (excluded via .clawhubignore).
set -euo pipefail

VPS_KEY="$HOME/.ssh/ox-bot.pem"
VPS_HOST="ubuntu@35.164.224.156"
SKILL_DIR="~/.openclaw/workspace/skills/sageox"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

rsync -avz --delete \
  -e "ssh -i $VPS_KEY" \
  --exclude deploy-to-vps.sh \
  --exclude README.md \
  "$SCRIPT_DIR/" \
  "$VPS_HOST:$SKILL_DIR/"

echo "deployed to $VPS_HOST:$SKILL_DIR"

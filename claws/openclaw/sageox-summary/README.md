# sageox-summary

📰 **Generate a Slack-ready cross-team summary of the last 24 hours from SageOx distilled activity.**

`sageox-summary` reads the daily summary files that `ox distill` produces
for each team, feeds them to `claude -p`, and returns a structured
Slack-formatted digest organized by what shipped, what's blocked, what
the team learned, and what's next.

Pairs with [`sageox-distill`](../sageox-distill/) — distill writes the
daily source files; this skill synthesizes them. You can use either
independently, but together they form a complete pipeline from raw repo
activity → daily team digests → unified readout.

## Install

```bash
clawhub install sageox-summary
```

Then complete the one-time environment setup in
[`claws/openclaw/README.md` § Environment setup](../README.md#environment-setup) —
specifically:

- Configure `ANTHROPIC_API_KEY` for the skill via either per-skill
  `apiKey` in `~/.openclaw/openclaw.json` (recommended; lets you use a
  separate key from your host agent) or via shell env. **Mind the
  precedence rule** documented in the link above.
- (If you choose to build `ox` from git source) extend `PATH` in
  `~/.openclaw/.env` so the skill subprocess can find `go` and the built
  `ox` binary

## Use

Once installed, ask your OpenClaw agent things like:

- "Give me the daily SageOx summary."
- "Switch ox install method." (re-runs the curl-vs-git setup)
- "Update ox now." (forces a git pull + rebuild on the git install path)

## What it does

1. **Loads the repo manifest** from `~/.openclaw/memory/sageox-distill-repos.json`
   (shared with [`sageox-distill`](../sageox-distill/)).
2. **Computes the team context directories** from the SageOx endpoint
   slug.
3. **Selects new daily files** for the last 24 hours by UTC date prefix,
   via the bundled `scripts/select-new-files.sh`. The window covers
   both today and yesterday's date-prefixed files to cleanly handle the
   day boundary, then filters out anything already included in a prior
   summary via `~/.openclaw/memory/sageox-summary-state.json`. If
   nothing is new, the skill prints one line and exits without calling
   Claude.
4. **Builds a prompt** from `assets/SUMMARIZE.md`, substituting the
   per-team list of new files to read.
5. **Runs `claude -p`** with read-only access to each team's context dir.
   `ANTHROPIC_API_KEY` is supplied by OpenClaw's per-skill `apiKey`
   injection (or by your shell, if configured that way) — see
   [Environment setup](../README.md#environment-setup).
6. **Updates the summary state** on success via
   `scripts/update-state.sh` — atomically merges the newly-summarized
   basenames into each team's `included_files`, prunes stale entries
   outside the window, and persists the file so the next run picks up
   where this one left off.
7. **Returns the summary** — already formatted for Slack mrkdwn.

The output is structured into four sections:

- *Where we are* — progress relative to goals + what shipped
- *What's getting in the way* — blockers, friction, unresolved issues
- *What we've learned* — techniques, discoveries, decisions
- *What's next* — queued work and follow-ups

## Requirements

- OS: macOS or Linux
- Binaries: `ox`, `claude`, `jq`
- Env: `ANTHROPIC_API_KEY` (supplied via per-skill `apiKey` config or
  shell env — see [Environment setup](../README.md#environment-setup))
- A SageOx account with at least one distilled team (run
  [`sageox-distill`](../sageox-distill/) first)

The skill will walk you through installing any missing pieces on first
run, including an interactive choice for how to install `ox` itself
(curl-pinned-release or git-clone-and-build).

This skill does **not** require `claude login` — it provides an explicit
API key on every invocation.

## Links

- **Repo:** <https://github.com/sageox/ox>
- **SageOx:** <https://sageox.ai>
- **Pair skill:** [`sageox-distill`](../sageox-distill/)
- **Publishing this skill:** [`../PUBLISHING.md`](../PUBLISHING.md)

# sageox-summary

📰 **Generate a Slack-ready cross-team summary of the last 24 hours from SageOx distilled activity.**

`sageox-summary` enumerates each team's recent distilled entries via
`ox distill history`, inlines their content into a prompt for `claude -p`,
and returns a structured Slack-formatted digest organized by what
shipped, what's blocked, what the team learned, and what's next.

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
- Ensure `$HOME/.local/bin` is on `PATH` for the skill subprocess (the
  `ox` install helper lands binaries there) — add
  `PATH=$HOME/.local/bin:$PATH` to `~/.openclaw/.env` if needed.

## Use

Once installed, ask your OpenClaw agent things like:

- "Give me the daily SageOx summary."
- "Reinstall ox." (re-runs the pinned-release install)

## What it does

1. **Loads the repo manifest** from `~/.openclaw/memory/sageox-distill-repos.json`
   (shared with [`sageox-distill`](../sageox-distill/)) and collects
   the unique `team_id` values.
2. **Selects new distilled entries** for the last 24 hours by asking
   `ox distill history list --team <tid> --since 24h --layer daily`,
   via the bundled `scripts/select-new-entries.sh`. `ox` auto-expands
   the window around the UTC day boundary so nothing falls through the
   cracks at midnight. The script then subtracts anything already
   included in a prior summary via
   `~/.openclaw/memory/sageox-summary-state.json`. If nothing is new,
   the skill prints one line and exits without calling Claude.
3. **Fetches entry content** in a single `ox distill history show`
   call per team, which emits the full markdown for every new id
   separated by `<!-- entry: <id> -->` headers.
4. **Builds a prompt** from `assets/SUMMARIZE.md`, inlining each team's
   entry content directly into the prompt — no filesystem access
   needed.
5. **Runs `claude -p`** with no `--add-dir` and no tool allowances.
   `ANTHROPIC_API_KEY` is supplied by OpenClaw's per-skill `apiKey`
   injection (or by your shell, if configured that way) — see
   [Environment setup](../README.md#environment-setup).
6. **Updates the summary state** on success via
   `scripts/update-state.sh` — atomically merges the newly-summarized
   entry ids into each team's `included_ids`, prunes entries older
   than yesterday UTC, and persists the file so the next run picks up
   where this one left off.
7. **Returns the summary** — already formatted for Slack mrkdwn.

The output is structured into four sections:

- *Where we are* — progress relative to goals + what shipped
- *What's getting in the way* — blockers, friction, unresolved issues
- *What we've learned* — techniques, discoveries, decisions
- *What's next* — queued work and follow-ups

## Requirements

- OS: macOS or Linux
- Binaries: `ox` with `ox distill history` support (landed in
  [PR #507](https://github.com/sageox/ox/pull/507)), `claude`, `jq`
- Env: `ANTHROPIC_API_KEY` (supplied via per-skill `apiKey` config or
  shell env — see [Environment setup](../README.md#environment-setup))
- A SageOx account with at least one distilled team (run
  [`sageox-distill`](../sageox-distill/) first)

The skill will walk you through installing any missing pieces on first
run, including `ox` itself via a pinned-release curl install.

This skill does **not** require `claude login` — it provides an explicit
API key on every invocation.

## Links

- **Repo:** <https://github.com/sageox/ox>
- **SageOx:** <https://sageox.ai>
- **Pair skill:** [`sageox-distill`](../sageox-distill/)
- **Publishing this skill:** [`../PUBLISHING.md`](../PUBLISHING.md)

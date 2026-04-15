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

Then complete the one-time host setup documented in
[`claws/openclaw/README.md`](../README.md):

- **Authenticate `claude`** — either run `claude login` (Pro/Max OAuth)
  or export `ANTHROPIC_API_KEY` in the shell that launches OpenClaw.
  `claude -p` inherits its own credentials; the skill no longer accepts
  a per-skill `apiKey`.
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
   `claude` uses whatever credentials it already has — either
   `claude login` OAuth or `ANTHROPIC_API_KEY` from the launching
   shell. See [`../README.md`](../README.md).
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
- `claude` authenticated via `claude login` or `ANTHROPIC_API_KEY` in
  the launching shell — see [`../README.md`](../README.md)
- A SageOx account with at least one distilled team (run
  [`sageox-distill`](../sageox-distill/) first)

The skill will walk you through installing any missing pieces on first
run, including `ox` itself via a pinned-release curl install.

## Links

- **Repo:** <https://github.com/sageox/ox>
- **SageOx:** <https://sageox.ai>
- **Pair skill:** [`sageox-distill`](../sageox-distill/)
- **Publishing this skill:** [`../PUBLISHING.md`](../PUBLISHING.md)

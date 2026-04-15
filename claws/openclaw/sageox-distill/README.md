# sageox-distill

🔬 **Sync, index, and distill team activity across SageOx-enabled repositories.**

`sageox-distill` keeps a team's SageOx knowledge base current. Given a list
of repos, it groups them by team, syncs each team's context, indexes recent
GitHub PRs and issues, and runs `ox distill` to produce daily summary files
that capture what the team built and decided in the last day.

Pairs with [`sageox-summary`](../sageox-summary/) — distill writes the daily
source files; summary synthesizes them into a Slack-ready cross-team digest.
You can use either independently, but together they form a complete pipeline
from raw repo activity → daily team digests → unified readout.

## Install

```bash
clawhub install sageox-distill
```

Then complete the one-time host setup documented in
[`claws/openclaw/README.md`](../README.md):

- **Authenticate `claude`** — either run `claude login` (Pro/Max OAuth)
  or export `ANTHROPIC_API_KEY` in the shell that launches OpenClaw.
  `ox distill` shells out to `claude` and inherits its credentials.
  The skill no longer accepts a per-skill `apiKey`.
- Ensure `$HOME/.local/bin` is on `PATH` for the skill subprocess (the
  `ox` install helper lands binaries there) — add
  `PATH=$HOME/.local/bin:$PATH` to `~/.openclaw/.env` if needed.

## Use

Once installed, ask your OpenClaw agent things like:

- "Distill all my SageOx repos."
- "Add `~/src/sageox/ox` to the distill manifest."
- "Show me the distill repos."
- "Reinstall ox." (re-runs the pinned-release install)

## What it does

1. **Loads the repo manifest** from `~/.openclaw/memory/sageox-distill-repos.json`
   (or builds it interactively on first run).
2. **Syncs each team's context** via `ox sync --all-teams`.
3. **Indexes GitHub activity** for each repo via `ox index github`.
4. **Waits for the SageOx daemon** to finish processing.
5. **Distills each team** via `ox distill --sync --layer daily`.

`ox distill` shells out to `claude` for LLM calls and inherits whatever
credentials `claude` has — either an OAuth session from `claude login`
or `ANTHROPIC_API_KEY` from the shell that launches OpenClaw. See
[`claws/openclaw/README.md`](../README.md) for the full setup.

## Requirements

- OS: macOS or Linux
- Binaries: `ox`, `git`, `gh`, `jq`, `claude`
- `claude` authenticated via `claude login` or `ANTHROPIC_API_KEY` in
  the launching shell — see [`../README.md`](../README.md)
- A SageOx account (sign up at <https://sageox.ai>)
- A GitHub account with `gh` authenticated

The skill will walk you through installing any missing pieces on first
run, including `ox` itself via a pinned-release curl install.

## Links

- **Repo:** <https://github.com/sageox/ox>
- **SageOx:** <https://sageox.ai>
- **Pair skill:** [`sageox-summary`](../sageox-summary/)
- **Publishing this skill:** [`../PUBLISHING.md`](../PUBLISHING.md)

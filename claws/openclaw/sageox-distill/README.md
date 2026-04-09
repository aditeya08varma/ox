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

- "Distill all my SageOx repos."
- "Add `~/src/sageox/ox` to the distill manifest."
- "Schedule distillation every 4 hours."
- "Show me the distill repos."
- "Switch ox install method." (re-runs the curl-vs-git setup)
- "Update ox now." (forces a git pull + rebuild on the git install path)

## What it does

1. **Loads the repo manifest** from `~/.openclaw/memory/sageox-distill-repos.json`
   (or builds it interactively on first run).
2. **Syncs each team's context** via `ox sync --all-teams`.
3. **Indexes GitHub activity** for each repo via `ox index github`.
4. **Waits for the SageOx daemon** to finish processing.
5. **Distills each team** via `ox distill --sync --layer daily`.

`ox distill` reads `ANTHROPIC_API_KEY` from its process environment.
OpenClaw can supply that key per-skill (recommended, via the `apiKey`
field in `~/.openclaw/openclaw.json`) or you can use a shell-level
`ANTHROPIC_API_KEY` shared with your host agent. See
[Environment setup](../README.md#environment-setup) for details and the
critical precedence rule.

## Requirements

- OS: macOS or Linux
- Binaries: `ox`, `git`, `gh`
- Env: `ANTHROPIC_API_KEY` (supplied via per-skill `apiKey` config or
  shell env — see [Environment setup](../README.md#environment-setup))
- A SageOx account (sign up at <https://sageox.ai>)
- A GitHub account with `gh` authenticated

The skill will walk you through installing any missing pieces on first
run, including an interactive choice for how to install `ox` itself
(curl-pinned-release or git-clone-and-build).

## Links

- **Repo:** <https://github.com/sageox/ox>
- **SageOx:** <https://sageox.ai>
- **Pair skill:** [`sageox-summary`](../sageox-summary/)
- **Publishing this skill:** [`../PUBLISHING.md`](../PUBLISHING.md)

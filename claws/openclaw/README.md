# OpenClaw skills

Skills in this directory are published from the `ox` repo to
[ClawHub](https://clawhub.ai), the public skill registry for
[OpenClaw](https://openclaw.ai).

## Skills

| Slug | Emoji | What it does |
|---|---|---|
| [`sageox`](sageox/) | 🐂 | Complete toolkit for SageOx team knowledge: query, coworkers, distill, summary, glance, catchup, import/export, and repo manifest management. |

The earlier `sageox-distill` and `sageox-summary` skills have been folded
into this single `sageox` skill. Consumers who previously installed them
should switch to `clawhub install sageox`.

## Install (consumers)

```bash
clawhub install sageox
```

The skill requires `claude` to be installed and authenticated, and
possibly a `PATH=` line in `~/.openclaw/.env` if `$HOME/.local/bin` is
not on your default `PATH`. See below.

## Claude credentials

The skill requires the `claude` CLI for LLM calls — `ox distill` and the
summary capability both shell out to `claude`. The skill does **not**
accept a per-skill `apiKey` — earlier versions tried this via OpenClaw's
`apiKey` injection, but the mechanism is unreliable and has been removed.

Authenticate `claude` once on the host, using either:

- **`claude login`** — Pro/Max OAuth, stored under `~/.claude/`. No
  API key needed and recommended if you already have a Claude.ai
  subscription.
- **`ANTHROPIC_API_KEY=sk-ant-...`** exported in the shell (or
  `~/.openclaw/.env`) that launches OpenClaw. `claude` and `ox
  distill` both read it from the process environment.

If neither is configured, the skill stops at its prerequisite check and
tells you which one to set up.

## PATH (required if `$HOME/.local/bin` is not already on PATH)

The `ox` install flow shipped with the skill lands binaries in
`$HOME/.local/bin`. Some distros (notably stock macOS and some minimal
Linux images) do not include that directory on the default `PATH`. If
the install helper warns to stderr that `$HOME/.local/bin` is not on
`PATH`, add the following line to `~/.openclaw/.env`:

```sh
PATH=$HOME/.local/bin:$PATH
```

OpenClaw loads `.env` into the skill subprocess, so the updated `PATH`
takes effect on the next invocation.

## Publishing (maintainers)

See [PUBLISHING.md](PUBLISHING.md).

## Links

- [OpenClaw docs](https://docs.openclaw.ai)
- [ClawHub](https://clawhub.ai)
- [SageOx](https://sageox.ai)
- [`ox` repo](https://github.com/sageox/ox)

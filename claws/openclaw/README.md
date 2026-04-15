# OpenClaw skills

Skills in this directory are published from the `ox` repo to
[ClawHub](https://clawhub.ai), the public skill registry for
[OpenClaw](https://openclaw.ai).

## Skills

| Slug | Emoji | What it does |
|---|---|---|
| [`sageox-distill`](sageox-distill/) | 🔬 | Sync team contexts, index GitHub activity, and run `ox distill` across multiple SageOx repos. |
| [`sageox-summary`](sageox-summary/) | 📰 | Generate a Slack-ready cross-team summary of the last 24 hours from distilled daily files. |

The two skills pair: **distill** writes the source material, **summary**
synthesizes it. You can use either independently, but using both gives you
an end-to-end pipeline from raw repo activity → daily team digests → a
unified cross-team readout.

## Install (consumers)

```bash
clawhub install sageox-distill
clawhub install sageox-summary
```

Both skills require `claude` to be installed and authenticated, and
possibly a `PATH=` line in `~/.openclaw/.env` if `$HOME/.local/bin` is
not on your default `PATH`. See below.

## Claude credentials

Both skills require the `claude` CLI for LLM calls — `sageox-summary`
shells out to `claude -p` directly, and `sageox-distill` runs `ox
distill`, which itself shells out to `claude`. The skills do **not**
accept a per-skill `apiKey` — earlier versions tried this via
OpenClaw's `apiKey` injection, but the mechanism is unreliable and has
been removed.

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

The `ox` install flow shipped with the SageOx skills lands binaries in
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

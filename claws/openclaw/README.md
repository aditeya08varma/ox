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

Both skills run in OpenClaw and require a one-time setup of
`~/.openclaw/.env` — see [Environment setup](#environment-setup) below.

## Environment setup

Both skills declare `ANTHROPIC_API_KEY` in their `requires.env` and
`primaryEnv`. OpenClaw can supply that key to each skill in two ways —
pick whichever matches your setup.

### Anthropic API key

#### Recommended: per-skill config in `~/.openclaw/openclaw.json`

This lets you use a **different** Anthropic key for SageOx skills than for
your host agent (different account, different rate limits, different
billing). OpenClaw injects the key into the skill's process environment for
the duration of the run, then reverts it.

```json5
{
  skills: {
    entries: {
      "sageox-distill": {
        apiKey: "sk-ant-..."
      },
      "sageox-summary": {
        apiKey: "sk-ant-..."
      }
    }
  }
}
```

If you don't want to keep the key in plaintext JSON, use a SecretRef
instead and store the actual value in your shell or `~/.openclaw/.env`:

```json5
{
  skills: {
    entries: {
      "sageox-distill": {
        apiKey: { source: "env", provider: "default", id: "MY_SAGEOX_KEY" }
      }
    }
  }
}
```

```sh
# in ~/.openclaw/.env or your shell rc
MY_SAGEOX_KEY=sk-ant-...
```

See [`docs.openclaw.ai/tools/skills-config`](https://docs.openclaw.ai/tools/skills-config)
for the full schema.

#### ⚠️ Critical precedence rule

OpenClaw injects the per-skill `apiKey` **only if `ANTHROPIC_API_KEY` is
not already set in its process environment** (see `env-overrides.ts` in
the openclaw repo). If your login shell exports `ANTHROPIC_API_KEY` (e.g.,
from `~/.zshrc`), the host value passes through to the skill and **the
per-skill `apiKey` is silently ignored.**

To use Option A (separate keys), verify before launching OpenClaw:

```sh
env | grep ANTHROPIC_API_KEY    # should print nothing
```

If it prints a value, find where it's exported (`~/.zshrc`, `~/.bashrc`,
`~/.profile`, your terminal app's env settings) and remove it.

#### Fallback: shared shell env

If you're fine with your host agent and SageOx skills sharing one
Anthropic key, you can skip the per-skill config entirely and just have
`ANTHROPIC_API_KEY` set in your shell or `~/.openclaw/.env`:

```sh
ANTHROPIC_API_KEY=sk-ant-...
```

The skills pick it up either way.

#### Sandboxed runs (Docker)

Per-skill `apiKey` does **not** apply when OpenClaw runs skills inside a
sandbox — `process.env` is not inherited into the container. For sandboxed
sessions, configure the key under `agents.defaults.sandbox.docker.env`
(or per-agent `agents.list[].sandbox.docker.env`) instead:

```json5
{
  agents: {
    defaults: {
      sandbox: {
        docker: {
          env: { ANTHROPIC_API_KEY: "sk-ant-..." }
        }
      }
    }
  }
}
```

### PATH (required if `$HOME/.local/bin` is not already on PATH)

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

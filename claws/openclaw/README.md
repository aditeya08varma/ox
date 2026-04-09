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

### PATH (required if you installed `ox` from git)

If you chose the **git clone + build** option for `ox` (see the install
flow in each skill's `SKILL.md`), you must add a `PATH=` line that
includes both the Go toolchain and Go's install-output directory.

Absolute paths only — `.env` files do not reliably expand `$PATH`.

```sh
# macOS (Homebrew Go):
PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/Users/you/go/bin

# macOS (Go from go.dev):
# PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/Users/you/go/bin

# Linux (apt golang-go):
# PATH=/usr/lib/go-1.24/bin:/usr/local/bin:/usr/bin:/bin:/home/you/go/bin

# Linux (Go from go.dev):
# PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/home/you/go/bin
```

To find your paths:

```sh
which go              # Go toolchain dir (parent of the go binary)
go env GOBIN          # Go install-output dir; fallback: $HOME/go/bin
```

If you chose the **curl install** option for `ox`, you do **not** need a
PATH line — `ox` installs to `/usr/local/bin` (Linux) or
`/opt/homebrew/bin` / `/usr/local/bin` (macOS), which are normally on PATH
already.

## Publishing (maintainers)

See [PUBLISHING.md](PUBLISHING.md).

## Links

- [OpenClaw docs](https://docs.openclaw.ai)
- [ClawHub](https://clawhub.ai)
- [SageOx](https://sageox.ai)
- [`ox` repo](https://github.com/sageox/ox)

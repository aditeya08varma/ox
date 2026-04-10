# Publishing OpenClaw skills to ClawHub

This directory's skills are published to [ClawHub](https://clawhub.ai) via
the `clawhub` CLI. This doc is the operator runbook.

## One-time setup

Install the `clawhub` CLI (see
[openclaw/clawhub](https://github.com/openclaw/clawhub)), then authenticate:

```bash
clawhub login                     # browser flow
# or for headless / CI:
clawhub login --token clh_xxx
clawhub whoami                    # verify
```

The token is stored in `~/Library/Application Support/clawhub/config.json`
on macOS; override via `CLAWHUB_CONFIG_PATH`.

**GitHub requirement:** your GitHub account must be at least one week old
to publish.

## State maintained in this repo

| Where | What | Who updates |
|---|---|---|
| `<skill>/SKILL.md` → `version:` | Source of truth for the next publish | Manually, or auto-bumped by `clawhub sync --bump` |
| `<skill>/.clawhubignore` | Files to exclude from the published bundle | Manually |

**Not in this repo:**

- Auth tokens — `clawhub login` writes them to the user's config dir.
- `.clawhub/lock.json` — that's *consumer-side install state*, not
  publisher state. Do not commit it.
- `.clawhub/origin.json` — same.

## Publishing workflow

### Preview (dry run)

Always do this first — it shows the plan without uploading anything.

```bash
clawhub sync --root claws/openclaw --dry-run
```

### Publish all changed skills

```bash
clawhub sync --root claws/openclaw --all \
  --bump patch \
  --changelog "Describe the change for this release"
```

`sync` computes a fingerprint from each skill's local files and only
publishes the ones that changed since the last known version on the
registry. `--all` skips the interactive confirmation for each skill.

### Publish a single skill explicitly

```bash
clawhub skill publish claws/openclaw/sageox-distill \
  --version 0.2.0 \
  --tags latest \
  --changelog "Describe the change"
```

Use this when you need exact control over the version (e.g., a minor or
major bump rather than the default patch).

## Versioning rules

All skills follow semver. The rules match how [`ox` itself is versioned](../../CLAUDE.md#versioning),
scoped per skill:

| Bump | When |
|---|---|
| patch | Prose tweaks, prereq fixes, new platform support that doesn't change the contract |
| minor | New user-visible behavior (new commands the skill runs, new memory keys, new config fields) |
| major | Breaking changes to the `~/.openclaw/.env` contract, memory file schema, or skill invocation model |

The first publish of each skill is `0.1.0`.

## Slug reservation

Slugs are global on ClawHub (`^[a-z0-9][a-z0-9-]*$`). This repo owns:

- `sageox-distill`
- `sageox-summary`

Derived automatically from the skill folder name. Once claimed, renames
are possible via `clawhub skill rename <old> <new>` (the old slug becomes
a redirect).

## What gets uploaded

Only text-based files. Server-side limits:

- Bundle ≤ 50 MB
- Each file must be in the text-file allowlist (Markdown, JSON, YAML,
  TOML, JS, TS, SVG, plus anything with a `text/*` MIME type)

Each skill folder's `.clawhubignore` excludes `README.md` and any other
repo-facing files that shouldn't ship to consumers.

**Do not add `SKILL.md` to `.clawhubignore`.** The publish would fail
server-side without it. The local linter (`clawhub-skill-lint`) will
flag any `.clawhubignore` entry that lists `SKILL.md` as a critical
finding before the bad bundle ever leaves your machine.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `account too new` | GitHub account < 1 week old; wait it out. |
| `metadata mismatch` warning | The skill references an env var or binary not declared under `requires.env` / `requires.bins`. Fix the frontmatter. |
| `bundle too large` | Something got included it shouldn't have. Add it to `.clawhubignore`. |
| `fingerprint matches existing version` | Nothing changed since the last publish. Either make a real change or manually bump `version:` in `SKILL.md`. |

## References

- [ClawHub skill format](https://github.com/openclaw/clawhub/blob/main/docs/skill-format.md)
- [ClawHub CLI reference](https://github.com/openclaw/clawhub/blob/main/docs/cli.md)
- [ClawHub quickstart](https://github.com/openclaw/clawhub/blob/main/docs/quickstart.md)

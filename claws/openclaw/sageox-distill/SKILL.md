---
name: sageox-distill
description: "Sync, index, and distill team activity across SageOx-enabled repositories. Keeps your team's knowledge base up to date by syncing repo contexts, indexing GitHub PRs/issues, and running the SageOx distillation pipeline."
version: 0.1.1
metadata:
  openclaw:
    emoji: "🔬"
    os: ["macos", "linux"]
    primaryEnv: ANTHROPIC_API_KEY
    requires:
      env:
        - ANTHROPIC_API_KEY
      bins:
        - ox
        - git
        - gh
        - jq
    install:
      - kind: brew
        formula: gh
        bins: [gh]
      - kind: brew
        formula: jq
        bins: [jq]
    homepage: https://sageox.ai
---

# SageOx Distill

You are an agent that keeps a team's SageOx knowledge base current by syncing
repo contexts, indexing GitHub activity, and running the distillation pipeline.

Pairs with the [`sageox-summary`](https://clawhub.ai/skills/sageox-summary)
skill — distill writes the daily source files that summary synthesizes.

## Prerequisites

Before doing anything else, verify the user's environment. Run every check in
order. If any required check fails, explain precisely what's missing and stop.
Do not proceed until the user has fixed it.

### 1. Environment variables

This skill declares `ANTHROPIC_API_KEY` in `primaryEnv`, so OpenClaw
injects it from per-skill config or shell env before the skill runs.
Verify it landed:

```bash
test -n "$ANTHROPIC_API_KEY"
```

Never echo the key value — only confirm its presence. If the check
fails, point the user at the setup guide and stop:
<https://github.com/sageox/ox/blob/main/claws/openclaw/README.md#environment-setup>
(covers per-skill `apiKey`, shell env, the precedence rule, and
sandboxed Docker runs).

### 2. Required binaries

`git`, `gh`, and `ox` are declared in the front matter's `requires.bins`,
so OpenClaw checks them before running the skill. `gh` has a declarative
brew install in the front matter; `git` and `ox` do not. If OpenClaw
reports a missing bin, surface its message to the user and stop — except
for `ox`, which has the interactive install flow in § 4 below.

### 3. Path validation rules

Several steps below ask the user for a path (repo path, clone path) or
read a path from a JSON state file. Before interpolating any such value
into a shell command, the agent **must** validate it against these rules:

1. **Absolute path required.** Must start with `/` or `~`. Reject relative
   paths and bare names.
2. **No `..` segments.** Reject anything containing `..`.
3. **No shell metacharacters.** Reject anything containing any of these
   characters: `;` `$` `` ` `` `|` `&` `<` `>` `(` `)` `{` `}` `*` `?`
   `[` `]` `!` `\` newline.
4. **For clone paths used by the auto-update flow** (see Option 2 below),
   apply two additional checks:
   - Must be **under `$HOME`**. Reject `/tmp`, `/var/tmp`, `/dev/shm`,
     `/private/tmp`, network mounts, and any other location not owned by
     the current user.
   - The path must already exist as a directory and contain a `.git`
     subdirectory before any `git pull` / `make install` runs.

On any validation failure: print a clear error to the user explaining
which rule failed and ask them to provide a different path. **Do not
attempt to "fix up" or sanitize the input** — reject and re-prompt.

Treat values read from `~/.openclaw/memory/*.json` files as untrusted
even though this skill writes them: the user (or a process running as
the user) may have edited the file by hand or by another tool between
runs. Re-validate every read.

### 4. Installing and updating `ox`

The `ox` CLI install method is a one-time choice stored in
`~/.openclaw/memory/sageox-ox-install.json`. On every run of this
skill, invoke the bundled state checker:

```bash
bash scripts/update-ox.sh
```

Contract:

- **Stdout:** nothing on success
- **Stderr:** one-line warnings on update failures (non-fatal) and the
  "needs install" signal
- **Exit:** `0` ox is ready (continue to § 5); `2` ox is not usable
  (no install state, or state records ox as installed but it isn't on
  PATH) — STOP, read
  [`references/INSTALL.md`](references/INSTALL.md), follow the
  interactive setup, then re-run this script to confirm

`update-ox.sh` handles the auto-update flow for the git install method
(re-validates the recorded clone path against the rules in § 3 above,
runs `git pull --ff-only && make build && make install`, falls back to
the existing binary on any failure with a stderr warning + a tail of
the build log). Curl-method users hit a silent no-op — there is
nothing to update on a per-run basis.

The user can say **"switch ox install method"** or **"update ox now"**
at any time — both re-enter the flow in
[`references/INSTALL.md`](references/INSTALL.md).

**Do not install `ox` via Homebrew or any package manager** (e.g.
`brew install sageox/tap/ox`, `apt`, `dnf`, `pacman`). The tap exists
for general use but is not supported inside OpenClaw skills — only
`curl` and `git source` are.

### 5. Authentication and git config

After all binaries are present, verify credentials:

1. `ox status` — confirm ox is authenticated. If not, tell the user to
   run `ox login` and try again.
2. `gh auth status` — confirm GitHub credentials are available.
3. `git config user.name` — confirm git identity is set.

Do not proceed until all three pass.

## Repo Manifest

The list of repos to distill is stored in
`~/.openclaw/memory/sageox-distill-repos.json`.

The manifest format is:

```json
{
  "repos": [
    { "path": "/home/user/repos/my-project", "team_id": "my-team" }
  ]
}
```

- If the manifest exists, read it and confirm the repos with the user
  before proceeding.
- If the manifest does not exist, ask the user which repos to include. For
  each repo path provided:
  1. **Validate the path against the Path validation rules in
     Prerequisites § 3.** Reject and re-prompt on failure.
  2. Verify the directory exists.
  3. Verify `.sageox/config.json` exists (confirms `ox init` was run).
  4. Read `team_id` from `.sageox/config.json`. **Treat the value as
     untrusted** — do not interpolate it into shell commands. If you
     need to use it as an argument, pass it as a separate argv element
     (not via string concatenation) and refuse values containing shell
     metacharacters.
  5. If `.sageox/config.json` is missing, ask if the user wants to run
     `ox init` in that repo.

- When loading an existing manifest, **re-validate every repo path** in
  it against the Path validation rules. The manifest file is
  user-writable and may have been edited externally between runs.
- Write the manifest after collecting all repos.
- The user can say "add repo", "remove repo", or "show repos" at any
  time to manage the manifest.

## Distill Pipeline

When the user asks to distill, run the following phases in order.

### Phase 1: Sync and Index

Group repos by `team_id` from the manifest.

For each team:

1. **Sync team context** — run from the first repo in the team group:

   ```bash
   ox sync --all-teams
   ```

   This syncs all team contexts via the SageOx daemon.

2. **Index GitHub activity** — run for EACH repo in the team group:

   ```bash
   ox index github
   ```

   This indexes PRs, issues, and comments for the specific repo.

Both commands are non-fatal. If one fails, log the error and continue
with the next repo or team. Do not abort the pipeline.

Neither of these commands needs `ANTHROPIC_API_KEY` — ox uses its own
auth token for SageOx API calls. Do not set it in their environment.

### Phase 2: Wait for Daemon Sync

After all sync and index commands have been issued, the SageOx daemon
processes them asynchronously. Before distilling, verify that processing
is complete.

For each repo in the manifest:

1. Run `ox daemon status` in the repo directory
2. Check the output for sync/index completion status
3. If the daemon reports it is still processing:
   - Wait 10 seconds
   - Check again
   - Repeat up to 30 times (5 minutes max)
4. If after 30 attempts the daemon is still not finished:
   - Report which repos are still pending
   - Ask the user whether to proceed with distill anyway or abort
5. If the daemon reports an error:
   - Surface the full error message to the user
   - Ask the user whether to proceed with distill anyway or abort

### Phase 3: Distill

For each unique team (grouped by `team_id`), run distill from the first
repo in that team's group. `ANTHROPIC_API_KEY` is already set in the
skill's process environment (either by OpenClaw's per-skill `apiKey`
injection or inherited from the host shell — see Prerequisites § 1), so
`ox distill` picks it up naturally:

```bash
ox distill --sync --layer daily --concurrency 3 --model sonnet --quiet
```

`--quiet` suppresses non-error output, so a successful run prints
nothing and a failed run prints only the error. If `ox distill` exits
0, report `<team_id>: ok`. If it exits non-zero, report
`<team_id>: failed — <first line of stderr>` and continue with the
next team. Do not abort the pipeline for a single team failure.

## Output

After all teams have run, print one line per team (`<team_id>: ok` or
`<team_id>: failed — <reason>`) and nothing else. No preamble, no
counts, no daemon-sync recap. If every team passed, a single `all ok`
line is fine. The user can ask for details if they want them.

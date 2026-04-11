---
name: sageox-distill
description: "Sync, index, and distill team activity across SageOx-enabled repositories. Keeps your team's knowledge base up to date by syncing repo contexts, indexing GitHub PRs/issues, and running the SageOx distillation pipeline."
version: 0.1.0
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
    install:
      - kind: brew
        formula: gh
        bins: [gh]
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

Verify `ANTHROPIC_API_KEY` is set and non-empty in the skill's process
environment:

```bash
test -n "$ANTHROPIC_API_KEY"
```

This skill declares `ANTHROPIC_API_KEY` in `requires.env` and `primaryEnv`,
which means OpenClaw will inject it from per-skill config when configured.
Never echo the key value — only confirm its presence.

If the var is missing, tell the user one of the following options:

**Option A — Per-skill config in `~/.openclaw/openclaw.json` (recommended).**
Lets the user use a different Anthropic key for this skill than for their
host agent:

```json5
{
  skills: {
    entries: {
      "sageox-distill": {
        apiKey: "sk-ant-..."
        // or with a SecretRef:
        // apiKey: { source: "env", provider: "default", id: "MY_SAGEOX_KEY" }
      }
    }
  }
}
```

OpenClaw injects this as `ANTHROPIC_API_KEY` into the skill's process
environment for the duration of the run, then reverts it. Subprocesses
spawned by the skill (`ox distill`, etc.) inherit it naturally.

**Option B — Shell env / `~/.openclaw/.env`.** If the user is fine with the
host agent and this skill sharing one Anthropic key, just export
`ANTHROPIC_API_KEY` from the shell or add it to `~/.openclaw/.env`:

```sh
ANTHROPIC_API_KEY=sk-ant-...
```

**⚠️ Critical precedence rule:** OpenClaw injects the per-skill `apiKey`
**only if `ANTHROPIC_API_KEY` is not already set** in its process
environment. If the user's login shell exports it (e.g., from `~/.zshrc`),
the host value passes through and the per-skill key is silently ignored.
To verify before launching OpenClaw: `env | grep ANTHROPIC_API_KEY` should
print nothing if Option A is in use.

**Sandboxed runs (Docker).** Per-skill `apiKey` does NOT apply inside the
sandbox — `process.env` is not inherited. For sandboxed sessions, configure
`agents.defaults.sandbox.docker.env.ANTHROPIC_API_KEY` instead.

### 2. Detect the operating system

```bash
uname -s
```

- `Darwin` → macOS
- `Linux` → Linux

Use the detected OS to choose install commands for any missing tools below.

### 3. Required binaries

Check each required binary is on the skill subprocess PATH:

```bash
command -v git
command -v gh
command -v ox
```

If any are missing, install them using the OS-appropriate commands in the
next section.

### 4. Installing missing tools

#### Installing `git`

- **macOS:** `brew install git` (Homebrew) or use the Xcode Command Line
  Tools: `xcode-select --install`
- **Linux (Debian/Ubuntu):** `sudo apt-get update && sudo apt-get install -y git`
- **Linux (Fedora/RHEL):** `sudo dnf install -y git`
- **Linux (Arch):** `sudo pacman -S --noconfirm git`
- **Linux (Alpine):** `sudo apk add git`

#### Installing `gh` (GitHub CLI)

- **macOS:** `brew install gh`
- **Linux (Debian/Ubuntu):** follow the apt instructions at
  <https://github.com/cli/cli/blob/trunk/docs/install_linux.md>
- **Linux (Fedora/RHEL):** `sudo dnf install -y gh`
- **Linux (Arch):** `sudo pacman -S --noconfirm github-cli`
- **Linux (Alpine):** `sudo apk add github-cli`

After install, the user runs `gh auth login` once.

#### Path validation rules

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

#### Installing `ox` — interactive setup

The `ox` CLI install method is a one-time choice stored in
`~/.openclaw/memory/sageox-ox-install.json`. Check that file first:

```bash
cat ~/.openclaw/memory/sageox-ox-install.json 2>/dev/null
```

**If the file exists:** read `install_method` and (for the git method)
`clone_path` + `auto_update`. If `install_method == "git"` and
`auto_update == true`, update ox before proceeding (see "Updating ox from
git" below).

**If the file does not exist:** STOP. This is a one-time decision that
gets persisted to disk and affects every future run of this skill — the
user must own it. You **MUST** ask the user which method they want and
**wait for their response**. Do not pick a default. Do not guess. Do not
run any install commands yet. Present both options verbatim, then stop
and wait:

> How do you want to install the `ox` CLI? Pick one — I won't choose for
> you, because this gets saved to
> `~/.openclaw/memory/sageox-ox-install.json` and reused every run.
>
> 1. **curl install** — runs the official install script from a pinned
>    release tag. Fastest, no build toolchain needed. Lands in
>    `/usr/local/bin` (Linux) or `/opt/homebrew/bin` or `/usr/local/bin`
>    (macOS). No auto-update; you'll re-run this flow to upgrade.
> 2. **Build from git source** — clones `github.com/sageox/ox` to a
>    directory you choose and builds with `make install`. Gives you
>    latest `main`, and optionally auto-updates on every run. Requires
>    Go ≥ 1.24.
>
> Reply `1` or `2`.

**Blocking rule:** do not proceed until the user replies with `1` or
`2`. If they say "you choose", "whatever", or try to skip, ask again and
explain that this is a persisted choice. Once they answer, save it to
the memory file.

**Do not install `ox` via Homebrew or any package manager** (e.g.
`brew install sageox/tap/ox`, `apt`, `dnf`, `pacman`). The tap exists
for general use but is not supported inside OpenClaw skills — only
`curl` and `git source` are. These two options work the same on macOS
and Linux; pick based on whether the user wants a pinned release or
`main` with optional auto-update, **not** based on their operating
system.

##### Option 1: curl install

Download and run the ox install script. The agent should print the source
URL and a head/tail preview of the downloaded script before executing it,
so the user can sanity-check what is about to run:

The URL is **pinned to a specific release tag** (not `main`) so the install
path is reproducible and can't be silently changed by an upstream commit. Bump
`OX_INSTALL_REF` when a newer release of `sageox/ox` is available.

```bash
# Pinned release tag. Bump this when a newer sageox/ox release is published.
OX_INSTALL_REF="v0.6.1"

# Download to a temp file. -f makes curl fail on HTTP errors instead of
# executing an HTML error page; --max-time bounds a stalled connection.
# Use the template form for mktemp — portable across GNU (Linux) and BSD (macOS).
INSTALL_SCRIPT="$(mktemp "${TMPDIR:-/tmp}/ox-install.XXXXXXXX")"
curl -fsSL --max-time 60 \
  "https://raw.githubusercontent.com/sageox/ox/${OX_INSTALL_REF}/scripts/install.sh" \
  -o "$INSTALL_SCRIPT"

# Surface what is about to run.
echo "Downloaded ox install script:"
echo "  Source: https://github.com/sageox/ox/blob/${OX_INSTALL_REF}/scripts/install.sh"
ls -lh "$INSTALL_SCRIPT"
echo "--- first 5 lines ---"
head -5 "$INSTALL_SCRIPT"
echo "--- last 5 lines ---"
tail -5 "$INSTALL_SCRIPT"

# Execute and clean up.
bash "$INSTALL_SCRIPT"
rm -f "$INSTALL_SCRIPT"
```

After it completes, verify `command -v ox` succeeds. Then persist:

```bash
mkdir -p ~/.openclaw/memory
cat > ~/.openclaw/memory/sageox-ox-install.json <<EOF
{
  "install_method": "curl",
  "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
```

`ox` lands in a standard system dir (`/usr/local/bin` on Linux,
`/opt/homebrew/bin` or `/usr/local/bin` on macOS) that is normally already
on PATH. If `command -v ox` still fails after install, ask the user to
check their shell PATH.

##### Option 2: build from git source

1. Verify Go ≥ 1.24 is installed:

   ```bash
   command -v go && go version
   ```

   If missing, print OS-appropriate install commands and stop until the
   user installs Go:

   - **macOS:** `brew install go` or download from <https://go.dev/dl/>
   - **Linux (Debian/Ubuntu):** `sudo apt-get install -y golang-go`
     (verify the version; use <https://go.dev/dl/> if the distro package
     is older than 1.24)
   - **Linux (Fedora/RHEL):** `sudo dnf install -y golang`
   - **Linux (Arch):** `sudo pacman -S --noconfirm go`

2. Ask the user for a clone path. Default: `$HOME/src/sageox/ox`.
   **Validate** the input against the Path validation rules above,
   including the additional rule that the path must be under `$HOME`.
   Re-prompt on failure.

   ⚠️ **The clone path becomes a privileged location.** If auto-update is
   enabled, this skill will run `make build && make install` from it on
   every invocation. Anyone with write access to the clone path can run
   arbitrary code in the user's environment. Strongly recommend a
   personal directory under `$HOME` (the default `$HOME/src/sageox/ox`
   is a good choice). Refuse anything in `/tmp`, `/var/tmp`, `/dev/shm`,
   shared filesystems, or world-writable directories — these checks are
   already part of the validation rules; do not bypass them.

3. Ask the user whether to auto-update from git on every run (yes/no).
   Default: yes.

4. Clone and build:

   ```bash
   CLONE_PATH="<user-chosen path>"
   mkdir -p "$(dirname "$CLONE_PATH")"
   if [ ! -d "$CLONE_PATH/.git" ]; then
     git clone https://github.com/sageox/ox.git "$CLONE_PATH"
   fi
   (cd "$CLONE_PATH" && make build && make install)
   ```

5. Detect Go's paths so the user can configure PATH in `~/.openclaw/.env`:

   ```bash
   GO_BIN_DIR="$(dirname "$(command -v go)")"
   GO_INSTALL_DIR="$(go env GOBIN)"
   [ -z "$GO_INSTALL_DIR" ] && GO_INSTALL_DIR="$HOME/go/bin"
   ```

6. Persist the memory file:

   ```bash
   mkdir -p ~/.openclaw/memory
   cat > ~/.openclaw/memory/sageox-ox-install.json <<EOF
   {
     "install_method": "git",
     "clone_path": "$CLONE_PATH",
     "auto_update": true,
     "go_bin_dir": "$GO_BIN_DIR",
     "go_install_dir": "$GO_INSTALL_DIR",
     "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   }
   EOF
   ```

7. Verify `ox` is on the current subprocess PATH:

   ```bash
   command -v ox
   ```

   If it is **not** on PATH, print a copy-pasteable block tailored to the
   user's detected paths and tell them to add it to `~/.openclaw/.env`:

   ```
   Add this to ~/.openclaw/.env:

   PATH=<GO_BIN_DIR>:/usr/local/bin:/usr/bin:/bin:<GO_INSTALL_DIR>
   ```

   Stop and ask the user to confirm once they've updated `.env` and
   restarted the skill. OpenClaw loads `.env` into the skill subprocess,
   so an updated PATH takes effect on the next invocation.

##### Updating `ox` from git (auto-update flow)

On every subsequent run, if the memory file says
`install_method == "git"` and `auto_update == true`:

1. Read `clone_path` from `~/.openclaw/memory/sageox-ox-install.json`.
2. **Re-validate** it against the Path validation rules in Prerequisites
   § 4, including the additional clone-path checks (must be under
   `$HOME`, must exist, must contain a `.git` subdirectory). The memory
   file is user-writable and may have been edited externally between
   runs — never trust persisted values without re-validation.
3. **If validation fails**, log a clear error naming the failing rule,
   skip the auto-update entirely, and fall back to the existing `ox`
   binary on PATH. Do not proceed with `cd` / `git pull` / `make install`.
4. If validation passes, run:

   ```bash
   (cd "$CLONE_PATH" && git pull --ff-only && make build && make install) || {
     echo "ox auto-update failed; continuing with existing binary" >&2
   }
   ```

Auto-update failures (validation or build) are non-fatal — fall back to
the existing binary and surface the error to the user.

The user can say **"switch ox install method"** or **"update ox now"** at
any time to re-run the interactive flow.

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
     Prerequisites § 4.** Reject and re-prompt on failure.
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

When the user asks to distill (or when triggered by a cron job), run the
following phases in order.

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
ox distill --sync --layer daily --concurrency 3 --model sonnet
```

Report the output of each distill run to the user. Include the team ID
and which repo was used as the working directory.

If a distill fails, report the error and continue with the next team.
Do not abort the pipeline for a single team failure.

## Scheduling

When the user asks to schedule distillation:

1. Create a cron job that runs the full distill pipeline (phases 1-3) at
   the requested frequency.
2. Suggest a default of every 4 hours if the user doesn't specify.
3. Confirm the schedule with the user before creating it.

Common schedules:

- Every 4 hours: `0 */4 * * *`
- Three times daily (morning, midday, evening): `0 8,12,18 * * *`
- Every 2 hours during work hours: `0 */2 9-17 * * 1-5`

The cron job must source `~/.openclaw/.env` before running (cron has a
minimal environment by default). A typical crontab line:

```cron
0 */4 * * * set -a; . $HOME/.openclaw/.env; set +a; openclaw run sageox-distill
```

Adjust the invocation to match however OpenClaw runs skills on the
user's system.

## Output

After each distill run, provide a brief status summary:

- How many teams were processed
- How many repos were synced and indexed
- Any errors or warnings encountered
- Whether the daemon sync completed or timed out

Keep the output concise. The user can ask for details if needed.

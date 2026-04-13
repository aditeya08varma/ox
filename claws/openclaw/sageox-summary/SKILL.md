---
name: sageox-summary
description: "Generate an overall team summary covering the last 24 hours across all SageOx-enabled teams. Reads distilled daily summary files from each team's context directory and produces a structured, Slack-ready overview."
version: 0.1.0
metadata:
  openclaw:
    emoji: "📰"
    os: ["macos", "linux"]
    primaryEnv: ANTHROPIC_API_KEY
    requires:
      env:
        - ANTHROPIC_API_KEY
      bins:
        - ox
        - claude
        - jq
    install:
      - kind: node
        package: "@anthropic-ai/claude-code"
        bins: [claude]
      - kind: brew
        formula: jq
        bins: [jq]
    homepage: https://sageox.ai
---

# SageOx Summary

You are an agent that generates a cross-team summary of the last 24 hours of
SageOx distilled activity. You read the daily summary files that `ox distill`
produces for each team, feed them to Claude via the `claude -p` CLI, and
return a structured Slack-formatted summary.

This skill pairs with the [`sageox-distill`](https://clawhub.ai/skills/sageox-distill)
skill — distill writes the source material, this skill synthesizes it.

## Prerequisites

Before doing anything else, verify the user's environment. Run every check
in order. If any required check fails, explain precisely what's missing
and stop. Do not proceed until the user has fixed it.

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

`ox`, `claude`, and `jq` are declared in the front matter's
`requires.bins`, so OpenClaw checks them before running the skill.
`claude` (npm) and `jq` (brew) have declarative installs in the front
matter; `ox` does not. If OpenClaw reports a missing bin, surface its
message to the user and stop — except for `ox`, which has the
interactive install flow in § 4 below. `claude -p` reads
`ANTHROPIC_API_KEY` from its process environment, so no `claude login`
is required.

### 3. Path validation rules

Several steps below ask the user for a path (clone path) or read a path
from a JSON state file. Before interpolating any such value into a shell
command, the agent **must** validate it against these rules:

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

### 4. Installing `ox` — interactive setup

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

#### Option 1: curl install

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

#### Option 2: build from git source

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

   ```text
   Add this to ~/.openclaw/.env:

   PATH=<GO_BIN_DIR>:/usr/local/bin:/usr/bin:/bin:<GO_INSTALL_DIR>
   ```

   Stop and ask the user to confirm once they've updated `.env` and
   restarted the skill. OpenClaw loads `.env` into the skill subprocess,
   so an updated PATH takes effect on the next invocation.

#### Updating `ox` from git (auto-update flow)

On every subsequent run, if the memory file says
`install_method == "git"` and `auto_update == true`:

1. Read `clone_path` from `~/.openclaw/memory/sageox-ox-install.json`.
2. **Re-validate** it against the Path validation rules in Prerequisites
   § 3, including the additional clone-path checks (must be under
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

### 5. Authentication

1. `ox status` — confirm ox is authenticated. If not, tell the user to
   run `ox login` and try again.
2. Smoke-test `claude -p` with the injected key:

   ```bash
   claude -p "say hi" --model claude-sonnet-4-6
   ```

   If it fails with an auth error, either the per-skill `apiKey` in
   `~/.openclaw/openclaw.json` is wrong/expired, or the host shell's
   `ANTHROPIC_API_KEY` (if set) is wrong/expired and is shadowing the
   per-skill config. Tell the user to fix whichever applies and try again.

## Configuration

The skill uses three pieces of state:

1. **Repo manifest** — `~/.openclaw/memory/sageox-distill-repos.json`
   (shared with the `sageox-distill` skill). Format:

   ```json
   {
     "repos": [
       { "path": "/home/user/repos/my-project", "team_id": "my-team" }
     ]
   }
   ```

2. **SageOx endpoint** — `~/.openclaw/memory/sageox-endpoint.txt` (a
   single line containing the endpoint URL, e.g.
   `https://test.sageox.ai`). Default if missing: `https://test.sageox.ai`.
   Ask the user to confirm the endpoint on first run and persist it.

3. **Summary state** — `~/.openclaw/memory/sageox-summary-state.json`.
   Tracks which distilled daily files have already been included in a
   prior summary run so the skill never re-summarizes the same content.
   Shape:
   `{updated_at, teams: {<team_id>: {included_files: [<basename>, ...]}}}`.
   Missing file → empty state (first run). Treat contents as
   **untrusted**: every filename must pass
   `^[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f-]+\.md$` or be silently
   dropped. Both bundled scripts handle this already — the rule is
   stated here for anyone reading the state file directly.

If the manifest does not exist, tell the user to run the `sageox-distill`
skill first to set up the repos, or ask for paths directly and populate
it.

## Summary Pipeline

When the user asks for a summary, run the steps in order. Steps 3 and
6 delegate their mechanics to `scripts/select-new-files.sh` and
`scripts/update-state.sh` (invoke via `bash scripts/<name>`).

### Step 1: Load the Manifest, Endpoint, and Summary State

1. Read `~/.openclaw/memory/sageox-distill-repos.json` with `jq`.
   **Re-validate every `path` entry** against the Path validation rules
   in Prerequisites § 3 before using it — the manifest is user-writable
   and may have been hand-edited between runs.
2. Read `~/.openclaw/memory/sageox-endpoint.txt` (or use default).
3. Read `~/.openclaw/memory/sageox-summary-state.json` with `jq` if it
   exists. If it is **missing**, proceed silently as if the `teams` map
   were empty — first runs are normal. If it exists but is **malformed
   or unreadable**, proceed as if empty AND emit exactly one warning to
   **stderr**:

   ```text
   warning: sageox-summary-state.json was unreadable, starting from empty state
   ```

   Never route this warning to stdout — stdout is the final summary
   (Step 7), and mixing decorative output into it breaks downstream
   consumers. This file is rewritten at the end of every successful
   run (see Step 6).
4. Group repos by `team_id` using `jq`.
5. Collect the unique team IDs.

### Step 2: Compute Team Context Directories

For each unique `team_id`, compute the team context directory:

```text
~/.local/share/sageox/<slug>/teams/<team_id>/
```

Where `<slug>` is derived from the endpoint URL:

1. Strip scheme (`https://`, `http://`)
2. Strip trailing slash
3. Strip port
4. Strip these prefixes if present: `api.`, `www.`, `app.`, `git.`
5. Normalize `127.0.0.1` to `localhost`

Examples:

- `https://test.sageox.ai` → `test.sageox.ai`
- `https://api.sageox.ai` → `sageox.ai`
- `http://127.0.0.1:8080` → `localhost`

The daily summary files live at:

```text
~/.local/share/sageox/<slug>/teams/<team_id>/memory/daily/
```

Verify each daily directory exists before proceeding. If a team's
directory is missing, log it and continue with the remaining teams.

### Step 3: Select New Daily Files

The window is the last 24h, keyed by the **UTC date prefix** of the
filename (`YYYY-MM-DD-<uuid>.md`). `ox distill` may run multiple times
per day and we don't track individual write times, so the window has
to over-include: any run must consider both today's and yesterday's
UTC-date files. The state file then filters out anything already
summarized, so re-runs within the window don't re-summarize content.
First run (no state file) = every candidate is new, not an error.

For each team from Step 2, invoke `scripts/select-new-files.sh`. It
handles BSD-vs-GNU `date`, the filename regex, and the subtraction
against the state file. Contract:

- **Usage:** `select-new-files.sh <team_daily_dir> <team_id> <state_file>`
- **Stdout:** one basename per line, sorted; empty if nothing new
- **Stderr:** one-line warnings (e.g. malformed state file)
- **Exit:** `0` success, `2` usage, `3` internal (`jq` missing)

```bash
STATE_FILE=~/.openclaw/memory/sageox-summary-state.json
NEW_FILES="$(bash scripts/select-new-files.sh \
  "$TEAM_DIR/memory/daily" "$TEAM_ID" "$STATE_FILE")"
```

Then:

1. If `NEW_FILES` is empty for a team, skip it this run — log
   `<team_id>: no new files since last summary` to **stderr** and
   continue with the remaining teams.
2. If **every** team ends up with zero new files, stop the pipeline
   before invoking Claude. Print one line to stdout —
   `No new distilled content since last summary.` — and exit. Do
   not modify the state file; Step 6's prune on the next
   successful run will collect any stale entries.

### Step 4: Build the Prompt

1. Read the template from the skill's assets directory:
   `./assets/SUMMARIZE.md` (relative to this SKILL.md file). The file
   path on disk depends on where OpenClaw loaded the skill from —
   typically one of:
   - `~/.openclaw/skills/sageox-summary/assets/SUMMARIZE.md`
   - `./skills/sageox-summary/assets/SUMMARIZE.md` (workspace skill)

2. Substitute template placeholders:
   - `{{FILE_LIST}}` — one section per team that has a non-empty
     `new_files` set, in this format. The team's absolute context
     directory is named **once** in the heading, and each file bullet
     is **relative to that directory**. Do not repeat the absolute
     prefix on every bullet — Claude has the team dir via `--add-dir`
     in Step 5, and duplicating ~100 chars of path on every line is
     pure token bloat.

     ```text
     ### Team "<team_id>" (files under <absolute_team_dir>/)
     - memory/daily/2026-04-12-019d40b9-....md
     - memory/daily/2026-04-13-019d4129-....md
     ```

     Teams with zero new files were already dropped in Step 3 and
     must not appear in `{{FILE_LIST}}`.

   - `{{MULTI_TEAM_RULES}}` — if **two or more** teams survived Step 3
     with non-empty `new_files`, replace with:

     ```text
     - Organize the summary by team, using each team ID as a section header
     - Attribute insights to the correct team
     ```

     Otherwise, replace with an empty string.

### Step 5: Run Claude

Invoke `claude -p` with:

- `--add-dir <team_dir>` for EACH team that has new files in this run
  (the parent `teams/<team_id>/` dir, not the `memory/daily/`
  subdirectory). Teams dropped in Step 3 must NOT be passed — nothing
  in their tree should be reachable.
- `--allowedTools Read` (summary is read-only)
- `--model claude-sonnet-4-6`
- The substituted prompt passed via stdin

The prompt enumerates the exact files Claude should read. Claude must
not open any other file under `memory/daily/` — the enumerated list
is the authoritative set for this run.

`ANTHROPIC_API_KEY` is already set in the skill's process environment
(either by OpenClaw's per-skill `apiKey` injection or inherited from the
host shell — see Prerequisites § 1), so `claude -p` picks it up naturally.
Wrap the invocation in `timeout 600` (10 minutes) — this matches the
timeout used by `pkg/sessionsummary/claude.go` in the `ox` repo for
comparable Claude synthesis work and gives the model enough headroom for
cross-team summaries that pull in many daily files:

```bash
timeout 600 claude -p \
  --add-dir "$TEAM_DIR_1" \
  --add-dir "$TEAM_DIR_2" \
  --allowedTools Read \
  --model claude-sonnet-4-6 <<< "$PROMPT"
```

If the invocation fails (non-zero exit, timeout exit 124, network
error), surface the error to the user and **do not** proceed to Step 6
— leaving the state file untouched lets the next run retry exactly
the same candidate set.

### Step 6: Update Summary State

Only run this step if Claude exited successfully. On any failure
(non-zero exit, timeout, network error), skip it entirely — leaving
the state file untouched lets the next run retry the same candidate set.

For each team that had non-empty `NEW_FILES` in Step 3, pipe that
team's basenames into `scripts/update-state.sh`. The script merges
them into the team's `included_files`, prunes entries whose date
prefix is strictly older than yesterday UTC, and writes the result
atomically via a sibling temp file + `mv -f`. See the script header
for full details; the short form:

- **Usage:** `update-state.sh <state_file> <team_id>`
- **Stdin:** one basename per line (regex-filtered on read)
- **Stdout:** nothing on success
- **Stderr:** one-line warnings (e.g. malformed prior state)
- **Exit:** `0` success, `2` usage, `3` internal

```bash
printf '%s\n' "$NEW_FILES" \
  | bash scripts/update-state.sh "$STATE_FILE" "$TEAM_ID"
```

Teams skipped in Step 3 are **not** invoked here — their prior state
passes through unchanged because `update-state.sh` only rewrites the
team_id it was given. Teams with no prior entry AND no new files are
correctly never added to the state file.

### Step 7: Return the Summary

Return Claude's stdout to the user directly. It is already formatted for
Slack mrkdwn. Do not reformat or annotate — just show it.

## Output

The primary output is the Claude-generated summary. Prefix it with a
brief one-line header showing:

- How many teams were summarized
- The endpoint used
- Any teams that were skipped (and why — typically "no new files since
  last summary" from Step 3)

Keep any preamble or postamble minimal. The summary itself is the value.

If Step 3 short-circuited because every team had zero new files, the
only output is the single line `No new distilled content since last
summary.` — do not invoke Claude and do not print a header.

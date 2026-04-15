# Installing `ox` — interactive setup

Invoked when `bash scripts/update-ox.sh` exits `2` (no install state
recorded). The deterministic shell lives in `scripts/install-ox-curl.sh`
and `scripts/install-ox-git.sh`; this file covers the user-facing
choice and validation. Path validation rules are defined in SKILL.md
§ 3.

## The choice

You **MUST** ask the user which method they want and **wait for their
response**. Do not pick a default. Do not guess. Do not run any install
commands yet. Present both options verbatim, then stop and wait:

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
`2`. If they say "you choose", "whatever", or try to skip, ask again
and explain that this is a persisted choice that affects every future
run.

**Do not install `ox` via Homebrew or any package manager** (e.g.
`brew install sageox/tap/ox`, `apt`, `dnf`, `pacman`). The tap exists
for general use but is not supported inside OpenClaw skills — only
`curl` and `git source` are. These two options work the same on macOS
and Linux; pick based on whether the user wants a pinned release or
`main` with optional auto-update, **not** based on their operating
system.

## Option 1: curl install

Invoke the bundled helper. It downloads the `ox` release tarball
directly from GitHub Releases at a pinned tag, verifies it against an
sha256 checksum embedded in the script, extracts it to
`$HOME/.local/bin`, and writes
`~/.openclaw/memory/sageox-ox-install.json` with `install_method: curl`.
No sudo, no shell-script piping, no dynamic "latest" resolution.

```bash
bash scripts/install-ox-curl.sh
```

Contract:

- **Args:** none
- **Stdout:** human-readable progress (download URL, checksum
  verification, extract, install dir)
- **Stderr:** errors and PATH-configuration guidance
- **Exit:** `0` success; `3` internal (curl/tar missing, download
  failed, checksum mismatch, unsupported platform, or `ox` not
  runnable after install)

If the script warns to stderr that `$HOME/.local/bin` is not on PATH,
surface its PATH guidance verbatim and ask the user to update
`~/.openclaw/.env` before retrying. If the script exits non-zero,
surface its stderr and stop — do not silently retry.

## Option 2: build from git source

Three things must happen before the script can run, and the agent owns
all of them:

1. **Verify Go ≥ 1.24:**

   ```bash
   command -v go && go version
   ```

   If missing, print OS-appropriate install commands and stop until the
   user installs Go:

   - **macOS:** `brew install go` or download from <https://go.dev/dl/>
   - **Linux (Debian/Ubuntu):** `sudo apt-get install -y golang-go`
     (verify the version; use <https://go.dev/dl/> if the distro
     package is older than 1.24)
   - **Linux (Fedora/RHEL):** `sudo dnf install -y golang`
   - **Linux (Arch):** `sudo pacman -S --noconfirm go`

2. **Ask the user for a clone path.** Default: `$HOME/src/sageox/ox`.
   **Validate** the input against the Path validation rules in
   SKILL.md § 3, including the additional rule that the path must be
   under `$HOME`. Re-prompt on failure. Expand `~` to `$HOME` before
   passing to the script — the script's defensive re-check requires a
   path starting with `$HOME/`.

   ⚠️ **The clone path becomes a privileged location.** If auto-update
   is enabled, this skill will run `make build && make install` from
   it on every invocation. Anyone with write access to the clone path
   can run arbitrary code in the user's environment. Strongly
   recommend a personal directory under `$HOME` (the default
   `$HOME/src/sageox/ox` is a good choice). Refuse anything in `/tmp`,
   `/var/tmp`, `/dev/shm`, shared filesystems, or world-writable
   directories — these are already in the validation rules; do not
   bypass them.

3. **Ask the user whether to auto-update from git on every run**
   (yes/no). Default: yes.

Then invoke the bundled helper with the validated clone path and the
literal string `true` or `false`:

```bash
bash scripts/install-ox-git.sh "$CLONE_PATH" "true"
```

Contract:

- **Args:** `<clone_path>` `<auto_update: true|false>`
- **Stdout:** clone/build progress
- **Stderr:** errors and PATH-configuration guidance
- **Exit:** `0` success; `2` usage / bad arg; `3` internal (Go missing
  or too old, build failed)

The script re-validates the clone path defensively, clones if needed,
runs `make build && make install`, detects Go's bin/install dirs,
persists the memory file, and warns to **stderr** if `command -v ox`
doesn't succeed in the current subprocess. If you see that warning,
surface the PATH guidance from the script's stderr to the user
verbatim, then stop and ask them to confirm once they've updated
`~/.openclaw/.env` and restarted the skill. OpenClaw loads `.env` into
the skill subprocess, so the updated PATH takes effect on the next
invocation.

## After installation

Re-run the state checker to confirm:

```bash
bash scripts/update-ox.sh
```

It should exit `0` with no stderr. Continue with the rest of the
prerequisites in SKILL.md (authentication, etc.).

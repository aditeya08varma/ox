---
audience: ai
ai_editing: allowed
refreshable: true
---

# Digital Twin Testing Architecture

Two digital twin systems provide realistic test environments without touching production remotes.

## Gitea Twin (Daemon Integration)

**Location:** `internal/daemon/twin_*.go` (18 files, ~5,100 lines)
**Build tag:** `slow` | **Requires:** Docker

A disposable Gitea 1.22 container emulates the real git+LFS remote. Tests exercise actual git protocol — clone, push, pull, rebase, LFS batch API — against a real server, not mocks.

### Architecture

```
┌─────────────────────────────────┐
│  Gitea Container (Docker)       │
│  localhost:13719 (fixed port)   │
│  git HTTP + LFS batch API       │
│  Admin: testadmin/testpass123   │
└──────────┬──────────────────────┘
           │ real git protocol
┌──────────▼──────────────────────┐
│  giteaFixture (twin_gitea_test) │
│  createRepo() → isolated repo  │
│  cloneRepo()  → git clone      │
│  pushFromTempClone() → remote Δ │
│  lfsClient()  → LFS batch API  │
└──────────┬──────────────────────┘
           │
┌──────────▼──────────────────────┐
│  Production Code Under Test     │
│  TwoPhaseClone, PushWithRetry,  │
│  runBlueGreenGC, pullManagedRepo│
└─────────────────────────────────┘
```

### Key Design Decisions

- **Shared singleton** (`sync.Once`): One container per test run. Non-twin slow tests don't pay startup cost.
- **Per-test repo isolation**: `createRepo(t, "unique-name")` — tests share the server, never data.
- **Fixed port 13719**: Gitea LFS batch responses embed `ROOT_URL` in action hrefs. Random ports break LFS URLs.
- **Ryuk cleanup**: testcontainers' sidecar auto-removes the container when the test process exits.

### What It Tests

| Domain | Files | Scenarios |
|--------|-------|-----------|
| Clone | `twin_clone_test.go` | Sparse checkout, manifest parsing, unshallow |
| Sync | `twin_sync_test.go` | Pull after concurrent push, rebase, autostash |
| Push | `twin_push_test.go` | Concurrent pushers, auto-resolve, conflicts |
| Team context | `twin_teamctx_push_test.go` | Push conflicts, autostash, ref checks |
| LFS | `twin_lfs_test.go`, `twin_lfs_git_test.go` | Batch API round-trip, pointer files |
| GC | `twin_gc_test.go`, `twin_gc_teamctx_test.go` | Blue-green reclone, dirty state, unpushed commits |
| Credentials | `twin_cred_test.go` | Auth, private repos, token handling |
| Sessions | `twin_session_finalize_test.go` | Session finalization workflows |
| Adversarial | `twin_adversarial_*.go` (4 files) | Symlink safety, races, state edge cases |

### Helpers

```go
pushMultipleFiles(t, cloneURL, map[string]string{...})  // seed repo structure
twinCommitFile(t, repoDir, relPath, content, msg)        // write + add + commit
gitConfig(t, dir)                                         // set test user identity
isolateCredentials(t)                                     // prevent leaking real creds
setupProjectWithConfig(t, "")                             // create .sageox/ project
newTestScheduler(projectDir)                              // daemon scheduler for GC tests
```

### Running

```bash
go test -tags=slow ./internal/daemon/ -run ^TestTwin    # all twin tests
go test -tags=slow ./internal/daemon/ -run TestGC_Team  # specific domain
OX_SKIP_DOCKER=1 go test -tags=slow ./internal/daemon/  # skip if no Docker
```

---

## Ledger Twin (Glance / Activity Analysis)

**Location:** `tests/ledger_twin/` (5 files, ~2,500 lines)
**Build tag:** `ledger_twin`

Generates a synthetic ledger with realistic sessions, murmurs, and carts for testing the glance (activity analysis) pipeline — conflict detection, pattern analysis, velocity tracking.

### Architecture

```
TwinManifest (scenarios.go)
  ├── 6 developers (alice, bob, carol, dave, eve, frank)
  ├── 60+ SessionSpecs → sessions/{timestamp}-{user}-{id}/
  ├── 100+ MurmurSpecs → data/murmurs/{date}/{hour}/{id}.json
  ├── 50+ CartSpecs    → carts/{id}.json
  └── 9 Windows (time ranges with statistical assertions)

GenerateTwinLedger(tmpDir, manifest) → writes to disk

Tests call:
  glance.HarvestMurmurs() / HarvestSessions()
  glance.DetectConflicts() / DetectPatterns()
  → assert against Window expectations
```

### Key Design Decisions

- **No Docker**: Pure filesystem — generates files to a tmpdir. Fast, no dependencies.
- **Deterministic scenarios**: Each Window defines expected conflicts, authors, pairs. Tests are assertions, not explorations.
- **Realistic paths**: Each developer has different worktree prefixes (macOS/Linux). Tests path normalization.
- **`PRESERVE_TWIN=1`**: Env var keeps the generated ledger on disk for manual inspection.

### What It Tests

| Domain | Scenarios |
|--------|-----------|
| Hot zone detection | 3-way file overlap between alice/bob/dave |
| Parallel streams | Zero collisions with isolated work (negative case) |
| Pair convergence | Exactly N conflicts matching expected author pairs |
| Escalation | Conflict density increasing over 3 consecutive days |
| Cluster bridge | Carol bridging two otherwise-disjoint team clusters |
| Hot file | Single file touched by 3+ authors |
| Session harvest | Files extracted from raw.jsonl tool calls |
| Cart analysis | Cart lifecycle across time windows |

### Running

```bash
go test -tags=ledger_twin ./tests/ledger_twin/...                    # all
go test -tags=ledger_twin -run TestPreCrime ./tests/ledger_twin/...  # specific
PRESERVE_TWIN=1 go test -tags=ledger_twin -v ./tests/ledger_twin/   # inspect output
```

---

## Relationship Between the Two

| | Gitea Twin | Ledger Twin |
|---|---|---|
| **Tests** | Git transport (daemon) | Data analysis (glance) |
| **Remote** | Real Gitea container | None (filesystem only) |
| **Data** | Team context structure, LFS blobs | Sessions, murmurs, carts |
| **Speed** | ~10s startup + tests | <1s |
| **Requires** | Docker | Nothing |

They are complementary: the Gitea twin proves the daemon can **move** data safely; the ledger twin proves the analysis pipeline can **interpret** data correctly.

---

## Extending for New Test Domains

### Pattern: Seeding a Team Context via Gitea Twin

The Gitea twin already supports team context structure. To test features that read/write team context content (e.g., distill pipeline, memory operations):

```go
func TestMyFeature(t *testing.T) {
    g := getSharedGitea(t)
    cloneURL := g.createRepo(t, "twin-my-feature")

    // Seed with team context structure
    pushMultipleFiles(t, cloneURL, map[string]string{
        ".sageox/config.json":            `{"version":1}`,
        "SOUL.md":                        "# Soul\n...",
        "memory/guidance/EXTRACT.md":     extractGuidance,
        "memory/guidance/DISTILL.md":     distillGuidance,
        "memory/.discussion-facts/d1.jsonl": knownFacts,
        "memory/daily/2026-03-10-abc.md": existingSummary,
    })

    // Clone locally — this path acts as tc.Path
    tcDir := filepath.Join(t.TempDir(), "teamctx")
    g.cloneRepo(t, cloneURL, tcDir)

    // Exercise your feature against tcDir
    // Push results back to verify they survive round-trip
}
```

### Pattern: Combining Both Twins

For features that span ledger + team context (e.g., session fact extraction feeds into distill):

1. Use ledger twin to generate sessions with known summaries
2. Use Gitea twin for the team context repo
3. Run the pipeline that reads sessions → writes facts → synthesizes memory
4. Assert on the output in the team context repo

---

## Distill Sandbox (`--sandbox`)

**Location:** `cmd/ox/distill_sandbox.go`

The distill sandbox extends the digital twin concept to the CLI itself. It creates an isolated copy of the real team context and ledger repos using `git clone --local` (hard-linked objects, near-instant), then swaps remotes to local bare repos so push succeeds without network access.

### How It Works

```
Real team context             Bare repo (tmpdir, disposable)
  ~/.local/share/sageox/        /tmp/ox-distill-sandbox-xxx/remote-tc.git
  .../team-contexts/team_xyz      (bare, accepts push)
         │                                ▲
         │ git clone --local              │ remote set-url origin
         ▼                                │
  /tmp/ox-distill-sandbox-xxx/  ──────────┘
    team-context/
    ledger-0/          (same pattern: local clone + bare remote)
    remote-ledger-0.git/
```

### Usage

```bash
# Sandbox with real data — full pipeline, push goes to local bare repo
ox distill --sandbox

# Override guidance files for experimentation
ox distill --sandbox --sandbox-extract=./my-EXTRACT-v2.md

# Override both guidance files
ox distill --sandbox --sandbox-extract=./EXTRACT.md --sandbox-distill=./DISTILL.md

# Guidance overrides imply --sandbox
ox distill --sandbox-extract=./my-EXTRACT.md
```

### Design Decisions

- **`git clone --local`** hard-links `.git/objects` — near-instant, minimal disk usage
- **Local bare remote** instead of Gitea — no Docker dependency for user-facing feature
- **CodeDB shared read-only** — no clone needed, distill only reads GitHub activity from it
- **`printSandboxResults()`** shows git log of new commits at the end for easy inspection
- **Guidance overrides imply `--sandbox`** — providing `--sandbox-extract` or `--sandbox-distill` automatically enables sandbox mode

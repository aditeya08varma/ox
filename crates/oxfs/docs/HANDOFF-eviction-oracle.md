# Handoff — cachesim oracle + eviction policy work

Autonomous session, 2026-07-14/15. **Working tree only — nothing committed or
pushed.** Tree is GREEN: `cargo test` passes across `oxfs` + `cachesim`, clippy
clean, formatted, `oxfsd` builds, and the no-bloat guarantee holds.

## Honest status vs your goals

| Goal | State |
|---|---|
| High conf **LRU implemented** | **Not yet implemented.** CLOCK-LRU is fully *designed* (design 1.1 §2 + review amendments) and the oracle is *ready to validate it*. The impl is the immediate next step. |
| High conf **2–3 other algos implementable** | **High** — design 1.1 §0 establishes the shared `slot`-substrate seam; LFU (sampled, per review R1) + S3-FIFO/TTL slot cleanly onto it. Not yet coded. |
| **Oracle to gain confidence changing algos** | **DONE and VERIFIED.** A differential oracle runs the same op stream through the real cache and a pure model; it already caught a real invariant bug on first run. This is the linchpin you asked for in `OxUc7i`, and it works. |

I stopped short of coding CLOCK because a half-finished schema migration in a
working tree you review raw is worse than a clean green checkpoint. The design is
complete and the oracle will validate CLOCK the moment it's built.

## What's DONE and VERIFIED (in the tree, green)

1. **`cachesim` crate** (`crates/cachesim/`) — pure in-memory `CacheModel` for
   today's default policy + a randomized differential test. Depends on `oxfs`;
   `oxfsd` does NOT depend on it (verified: `cargo tree -p oxfs` has no
   `cachesim`). Structural no-bloat guarantee, no feature flags.
2. **Differential oracle passes** — `generation_model_matches_impl_under_random_single_session_churn`:
   6 seeds × 60 generations of random churn under eviction pressure, asserting
   `Workspace::resident_keys()` == model AND `resident_bytes` == model every
   apply. **It caught a real bug first run**: the model consumed partial victims
   where the real cache is all-or-nothing on `StorageFull`
   (`failed_reservation_does_not_consume_partial_victims`) — fixed in
   `CacheModel::plan_victims` (plan before mutating).
3. **Catalog snapshot accessors** — `Catalog::resident_keys()` / `snapshot_state()`
   (mirror `pending_keys`), passthrough `ContentCache` → `Workspace`. Plus
   `Workspace::storage_key()` so the harness drives the model with exact keys.
   Types `CatalogSnapshot`/`ResidentRow` exported from `oxfs`.
4. **R3 rename** — the pre-existing free helper `resident_keys` (desired-set probe)
   → `resident_desired_keys`, freeing the name for the catalog accessor.
5. All 17 workspace + 6 catalog + 5 nfs + 13 lib tests still pass unchanged.

## Files touched
- NEW `crates/cachesim/{Cargo.toml,src/lib.rs,src/tests.rs}`
- `Cargo.toml` (workspace member)
- `crates/oxfs/src/cache_catalog.rs` (+`resident_keys`,`snapshot_state`,`CatalogSnapshot`,`ResidentRow`)
- `crates/oxfs/src/cache.rs` (+passthroughs)
- `crates/oxfs/src/workspace.rs` (+passthroughs, +`storage_key`, R3 rename)
- `crates/oxfs/src/lib.rs` (re-exports)
- Docs: `cachesim-oracle-north-star.md`, `eviction-policy-design-1.md` (+`-review`,
  `-decisions`), `eviction-policy-design-1.1.md` (Codex) + `-1.1-review.md` (my 4
  amendments), `small-files-analysis.md`, this file.

## Design decisions locked (see the docs)
- **CLOCK/second-chance** for LRU (your call): in-mem ref-bit, sweeping hand,
  ~zero read-path SQLite writes; crash-safe (bits advisory, reset on restart).
- **Sampled LFU** (review R1 corrects Codex's global-ordered LFU, which
  reintroduced the per-key String memory trap + O(log M) touch): dense-slot
  `Vec<u8>` aging freq + K-sample eviction. O(1) touch, ~1 MB/million.
- **Dense-slot substrate only** (R4): NO per-key `HashMap<String,_>` anywhere —
  thread `slot` through the residency probe. This is what makes it scale to
  millions of small files.
- **Small-files finding** (`small-files-analysis.md`): APFS 4 KiB block-rounding
  makes ~606 B objects cost ~6.7× on disk, so logical `resident_bytes ≤ capacity`
  does NOT bound real disk. Add a **physical-bytes universal invariant** to the
  oracle (R2). Also: dense-slot bitset mandatory; `gauges()` is an O(rows) scan.

## Next steps (backwards-from-oracle order, each validated by the oracle)
1. **Design-1 refactor (behavior-preserving):** extract `EvictionOrder` +
   `AdmitEverything` into `cache_policy.rs`; `Catalog::reserve` dispatches on the
   enum (default = today's `ORDER BY access_epoch,key`). Oracle must stay green
   → proves behavior preservation.
2. **CLOCK-LRU:** add `insert_seq`+`slot` migration (pin_epoch pattern), `hand` in
   `cache_meta`, index `(state,insert_seq)`; ring sweep in reserve; in-mem
   ref-bits (dense, indexed by slot from the row); touch tee at `open_file`
   (`cache.rs:665`) → `nfs/server.rs:304`. Add `ClockSecondChance` to the model
   (ring+bits+hand, `crash_recover` zeros bits). Serial exact-match; concurrent
   directional.
3. **Sampled LFU:** dense `Vec<u8>` aging freq + K-sample eviction + model mirror.
4. **Extend the harness into `oxjtest serve`** (which asserts nothing today,
   `oxjtest.rs:650`) so the oracle runs on the evolving stress workload, and add
   the physical-bytes + directional invariants (design 1.1 §5).

## Run the oracle
```
cargo test -p cachesim                 # differential oracle (~23s, real fetches)
cargo tree -p oxfs | grep cachesim     # must be empty (no-bloat guarantee)
cargo test -p oxfs                     # all existing invariants still green
```

## Note on Codex
Codex authored design 1.1 well, but its background `task` wrote into a sandbox
that synced back late/unreliably (its 1.1 doc appeared only after I'd started
re-authoring). For impl passes, use the **return-as-text** pattern (Codex returns
the diff in its final message; you/Claude write the file) to avoid lost writes.

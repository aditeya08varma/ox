# Cachesim oracle — north-star + backwards build plan

The target world we work backwards from. Supersedes nothing; it consolidates
`eviction-policy-design-brief.md`, `-design-1.md`, `-review.md`, and `-decisions.md`
into the end-state and the grounded decisions from the 2026-07-14 planning session
(SageOx `OxUc7i` cachesim research + `OxxRYg` eviction forensic map).

## Organizing principle: one spec, two mechanisms, a diff

The cache is a state machine defined once. Two mechanisms realize it; a driver runs
the same op stream through both and diffs. Divergence = bug, with a reproducing seed.

- **Reference model** (`cachesim` crate) — pure, in-memory, deterministic. No SQLite,
  no disk, no threads. Simple realization of the policy semantics.
- **Impl** (`oxfs` lib) — real `ContentCache`/`Catalog`: SQLite, disk, crash-safety,
  concurrency. The thing under test.

They share only the *spec vocabulary* (`EvictionOrder`, `Touch`, capacity constants),
never the *mechanism* (model sorts a map; impl scans an index). That independence is
what makes the diff catch policy bugs, not just plumbing bugs.

## Crate boundary (no oxfsd bloat — structural guarantee)

`oxfs` is one crate: `[lib]` (the real impl, shipped by the `oxfsd` binary) + three
bins (`oxfsd`, `oxjtest`, `oxdirtest`). The guarantee that the oracle never bloats the
real impl is the **dependency graph**, not a feature flag (feature unification in a
workspace can leak a flag into `oxfsd`):

```
        oxfs (lib) = REAL IMPL  ── shipped by oxfsd
          | EvictionOrder/Touch enums (policy SELECTOR — legit real-impl config)
          | Admission/Protection traits + trivial first-impls
          | resident_keys()  (NEW)
          v depended-on by
   ┌──────┴───────────────────────────────┐
   oxfsd            cachesim crate (NEW)     tests/ + proptest + oxjtest
   (real bin)       CacheModel, differential  (dev-only) depend on cachesim
   NO model         comparator, trace-replay,
   NO oracle        Belady, policy zoo
        └─X─► cachesim     ← this arrow does NOT exist = the guarantee
```

- **In the lib (ships, NOT bloat):** the `EvictionOrder`/`Touch`/`RecencyGranularity`
  enums, `AdmissionPolicy`/`ProtectionPolicy` traits + trivial v1 impls, and the
  impl's indexed realization. The real cache genuinely uses these to pick its scan.
- **In `cachesim` (never ships in oxfsd):** `CacheModel`, `ModelState`, the differential
  comparator, trace-replay, Belady-optimal, proptest state machine, the policy zoo.
- Workspace change: `members = ["crates/oxfs", "crates/cachesim"]`. `oxfsd`'s dep graph
  is untouched — `cargo build --bin oxfsd` can't compile a line of oracle code.

`cachesim` has **two harnesses over one `CacheModel`**: (1) the **oracle** (differential
vs the impl, for shipped policies) and (2) the **policy lab** (sim-only miss-ratio /
Belady evaluation for *any* policy, incl. ones the impl will never ship). Future policy
investigation happens in the lab with zero impl work; a policy graduates to the impl
(new `EvictionOrder` variant + index + independent model realization) only when chosen.

Build our own model, not libCacheSim/Caffeine: different job (exact resident-set vs
our apply/commit/crash semantics, not hit-ratio), and it's ~200 lines of pure Rust vs a
copyleft/FFI/JVM dependency that violates this crate's zero-copyleft + `unsafe forbid`
posture. Rent their *ideas* (Belady baseline, trace formats) for the lab, offline.

## Decisions (binding — from `-decisions.md` + 2026-07-14 answers)

| # | Decision |
|---|---|
| F1 | v1 scopes the policy split to `Workspace::apply` only. `open`/recovery stays on its per-entry loop; unifying it is a follow-up. |
| F2 | Admission hooks the `missing` computation (`workspace.rs:192-203`), not `begin_batch`'s arg. |
| F3 | Admission **owns** dedup + oversize-break + residency-skip; `AdmitEverything` replicates today exactly. |
| F4 | Eviction is a lazy, index-backed, early-stopping **ordering**, never an in-memory sort of all residents. Scales to millions; O(K) per reserve. |
| F5 | Protection accepted as documented scaffolding; skip the pending-key snapshot until it has a consumer. |
| F6 | "No schema churn" reworded: **interface**-stable; storage migrates when a policy needs a persisted signal. |
| Touch | **CLOCK / second-chance** for LRU (chosen). In-memory ref-bit, sweeping hand, ~zero read-path writes. |
| Algo | **LFU** is the concrete second algo; mirror CLOCK philosophy (approximate in-memory frequency, zero read-path writes). Show the seam also admits S3-FIFO / TTL. |
| Persist | Working tree only — no commits, no push, no bd. Leave a `NOTES` handoff. |
| Scope | "Use more time" — full chain, no scope-cutting, multiple Codex+Claude iterations. |

## CLOCK-LRU design (the crux)

**Mechanics.** Resident objects form a ring in a stable order (a monotonic `insert_seq`
column; ring = `ORDER BY insert_seq`, wrapping). Each has a reference bit. On a
successful read/use, set bit=1 (in memory). On eviction the hand advances over the ring:
bit==1 → clear to 0 and skip (second chance); bit==0 → evict. Hand position persists in
`cache_meta`.

**State placement.**
- Persisted (SQLite, authoritative): `insert_seq` per object, `hand` position, plus the
  existing `state`/`size`/`used_bytes`. Index `(state, insert_seq)` for the ring scan.
- In-memory (advisory hint): the reference bits — a bitset/`HashMap<key,bit>` over the
  resident set, keyed by a dense id (e.g. `insert_seq` slab index) for millions of files.

**Crash-safety (free).** Ref-bits are in-memory → lost on crash → reset to 0 on restart.
This changes only *future eviction order*, never residency. The catalog stays the sole
authority for what is resident. So CLOCK adds **no** new crash-safety burden; the
deferred-victim + apply-journal protocol is untouched. `crash_recover` in the model
mirrors this: bits→0.

**Scale.** Eviction is a bounded ring scan from the hand: touches ≈K victims + referenced
rows it clears+skips. Pathological all-referenced state is the standard CLOCK worst case,
amortized away because each sweep clears bits. Read path does ONE in-memory bit write —
no SQLite write, no index churn. This is why CLOCK beats coarse-bucket/exact-seq for
millions of small files.

**Oracle determinism.**
- *Serial replay* (proptest, oxjtest with reads quiesced/serialized): model and impl
  ring+bits+hand evolve identically → **exact key-set match**.
- *Concurrent* (oxjtest gate, live NFS readers): touch(bit=1) races eviction → ref-bit
  state at eviction can differ → victim can differ → **directional invariants only**
  ("everything the model says must-be-present IS present; nothing evicted-for-cause
  reappears"). This is the `OxUc7i` nondeterminism, now concretely from CLOCK touches.

## LFU design (scale-consistent second algo)

Basic LFU = per-object access count, evict smallest. Naive count-on-read = one indexed
write per read = the write-amp CLOCK avoids. So for scale-consistency: **approximate
in-memory frequency** (aging counter or count-min sketch — TinyLFU-style), advisory,
rebuilt on restart, mirroring CLOCK's zero-read-path-write property. Eviction orders by
the approximate frequency (persisted coarsely or held in memory with a periodic flush).
The exact scheme is a design-1.1 question; the seam requirement is only that frequency is
an *observable, model-mirrorable* signal. Proves the seam handles a non-recency order.

## Policy vocabulary (in the lib)

```rust
pub(crate) enum EvictionOrder {
    LeastRecentlySelectedGeneration,   // today: ORDER BY access_epoch, key (default)
    ClockSecondChance,                 // LRU-approx: ring scan + ref-bit + hand
    ApproxLeastFrequentlyUsed,         // LFU: order by approx frequency, key
    // S3FifoSieve, ExpiringFirst, ... = one variant + one index/ring + one Touch rule
}
pub(crate) enum Touch { None, SetRefBit, BumpFrequency }
```
Every order ends in `, key` for a total, deterministic tie-break. No raw SQL leaks from
policy code; the catalog is the single place that turns an order into an indexed scan or
ring sweep. Nothing a policy needs may live only in an un-observable in-process map that
the model can't mirror — un-oracle-able ⇒ forbidden. (Ref-bits are observable: the model
holds the identical bitset.)

## Backwards build sequence

1. **Oracle baseline** — add `resident_keys()` (mirror `pending_keys()`), stand up the
   `cachesim` crate with `CacheModel` for *today's* policy, wire a differential mode into
   oxjtest, assert exact-match. Locks a regression baseline before any refactor.
2. **Design-1 refactor under the oracle** — extract Admission/Protection/EvictionOrder;
   default = `LeastRecentlySelectedGeneration`. Oracle proves behavior-preservation.
3. **CLOCK-LRU under the oracle** — add `insert_seq` + `hand` + ring scan + in-mem
   ref-bits + read-path touch tee. Serial exact-match; concurrent directional.
4. **LFU under the oracle** — approximate frequency + `BumpFrequency` touch. Proves the
   seam generalizes.
5. **Extensibility evidence** — sketch S3-FIFO/Sieve + TTL as `EvictionOrder` variants to
   show "2-3 others" is high-confidence, without shipping them.

## Open questions for design 1.1 (Codex) to resolve

- Exact ring representation for CLOCK at millions of files (dense-id bitset vs
  `HashMap<key,bit>`; how `insert_seq` maps to a bitset index across evictions/reuse).
- Whether to persist the CLOCK `hand` or reset to 0 on restart (and the matching
  `crash_recover` in the model).
- LFU's exact frequency scheme (aging counter vs count-min sketch) and its flush/restart
  semantics.
- How the differential harness feeds the same touch sequence to the model in serial mode,
  and how it degrades cleanly to directional-only under concurrent readers (the read-path
  tee at `nfs/server.rs` open_file).
- Where `resident_keys()`/`snapshot_state()` live on the `ContentCache`→`Workspace` path.

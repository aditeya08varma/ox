# Design 1 — human-review decisions + F4 rethink

Human reviewer: Ajit. These resolve the F1–F6 findings in
`eviction-policy-design-1-review.md` and are binding inputs for **design 1.1**
and implementation.

## Decisions

| # | Decision |
|---|---|
| **F1** | v1 scopes the policy split to `Workspace::apply` **only**. `Workspace::open`/recovery stays on its current per-entry `materialize_missing` loop; unifying it is a documented follow-up, not design 1. |
| **F2** | Admission hooks the **`missing` computation** (`workspace.rs:192-203`), not `begin_batch`'s argument. `begin_batch` keeps receiving the full desired set. |
| **F3** | **Admission owns** dedup + oversize-break + residency-skip. `AdmitEverything` must replicate today's filtering exactly (behavior-preserving). |
| **F4** | Redesigned — see below. Eviction is a lazy, index-backed, early-stopping **ordering spec**, never an in-memory sort. Must scale to millions of resident files and admit true (use-based) LRU. |
| **F5** | Accepted as documented scaffolding. Do **not** build the pending-key snapshot until protection has a real consumer. |
| **F6** | Accepted. Reword "no schema churn" → "**interface**-stable; storage migrates when a policy needs a persisted signal." |

## F4 rethink (binding)

### Why design 1's shape is wrong, not just slow
`EvictionPolicy::rank(&mut [EvictionCandidate])` can only order a slice it is
handed, forcing the catalog to `SELECT` every resident row into a `Vec` and sort
in Rust: **O(M) memory + O(M log M) per `reserve`, × N reserves per batch**, with
M in the millions. The interface shape is the defect.

### The scale-correct shape (keep what today already does)
Today's `Catalog::reserve` is already correct in shape (`cache_catalog.rs:231-256`):
a **lazy scan of the `(state, access_epoch, key)` index that stops the moment
enough bytes are freed** — it touches ≈K victim rows, never M. The fix is to keep
this streamed early-stopping scan and let the policy choose **which index/order
is streamed**, not hand it a materialized slice.

- Rows touched per reserve ≈ K victims (+ a few protected skips). Independent of M.
- No `EvictionCandidate`/`EvictionSignals` materialized struct — it is removed.
  Recency/frequency/TTL are **columns the catalog orders by via an index**,
  registered per policy variant.

### Interface (typed ordering spec + touch, both catalog-owned)
```rust
/// What the catalog streams victims by. Each variant maps 1:1 to an index the
/// catalog provisions. A policy that scales MUST be index-backed; the type
/// enforces that (no raw SQL leaks from policy code).
pub(crate) enum EvictionOrder {
    /// ORDER BY access_epoch, key — today's default. Index already exists.
    LeastRecentlySelectedGeneration,
    /// ORDER BY last_access_bucket, key — use-based LRU.
    /// Requires: recency column + index + a Touch rule.
    LeastRecentlyUsed { granularity: RecencyGranularity },
    // LeastFrequentlyUsed / ExpiringFirst / ... = one variant + one index + one touch rule
}

/// Applied on a successful resident hit (resident()/open_file()). Cheap by design.
pub(crate) enum Touch { None, Bucket(RecencyGranularity), Count }
```
- Every order ends in `, key` → total, deterministic.
- **Protection** is a small **in-memory skip-filter on the streamed rows** (lease /
  open-handle set). Early-stop means we inspect ≈K rows, so an in-memory filter
  never forces materialization. In v1 it is inert (F5).
- The catalog remains the single owner of: the prepared cursor, the early-stop
  accumulation, `StorageFull`-without-partial-consumption, and all state
  transitions. Crash-safety protocol unchanged (effects still deferred to
  `commit_batch`).

### The LRU touch problem (the actual hard part)
True LRU needs a touch on every read. **Exact per-operation recency = one
index-moving `UPDATE` per read**, which at millions of files on a hot path is
write-amplification + recency-index B-tree churn + WAL/fsync pressure. Do not
ship exact-seq LRU. Design around cheap touch:

| Approach | Touch cost | Verdict |
|---|---|---|
| Exact seq LRU | 1 index-moving UPDATE / read | Does not scale — avoid |
| **Coarse-bucket LRU** | UPDATE only on bucket rollover | **Recommended.** Most touches are no-ops; ≈ time-to-idle; reuses epoch machinery |
| CLOCK / second-chance | in-mem ref-bit, lazy flush | O(1) touch, no per-read ordering write |
| Sampled (Redis-style) | coarse last-access only | No global recency index; sample K at eviction |

**Reframe:** today's policy is *already* a coarse generational LRU-approximation
keyed on **admission** generation. Migrating to LRU = (1) key recency on **use**,
(2) coarsen granularity so touch is write-cheap, (3) keep the streamed indexed
scan. It is a one-variant + one-index + one-touch-rule change, not a re-sort.

### Migration note
The LRU column + its `(state, last_access_bucket, key)` index are added by a
future migration following the existing legacy `pin_epoch` migration pattern in
`Catalog::open` (transactional: build index → backfill deterministic default →
validate → commit). Design 1 itself adds **no** column or index — default stays
`LeastRecentlySelectedGeneration` on the existing index.

## Open question for design 1.1 to answer
For the eventual LRU policy, pick the touch model (coarse-bucket vs CLOCK) and its
granularity, and state the write-amplification bound. Design 1 only needs to leave
the seam; it does not implement LRU.

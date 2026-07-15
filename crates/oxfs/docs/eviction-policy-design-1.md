# Design 1: composable cache admission, protection, and eviction policies

## Overview

This design separates the policy decision made at a manifest/apply boundary into
three axes while leaving storage mutation and crash recovery under the existing
`ContentCache`/`Catalog` machinery:

1. **Admission** chooses which missing manifest candidates to attempt and their
   order.
2. **Protection** declares which catalog objects are temporarily ineligible as
   victims.
3. **Eviction** deterministically ranks the eligible resident objects.

The first implementation is behavior-preserving. In particular, the current
policy is **not LRU**. `Catalog::reserve` stamps `access_epoch` with the current
catalog epoch, `Catalog::finish` repeats that stamp, and neither
`ContentCache::resident` nor `ContentCache::open_file` updates it. The accurate
name used throughout this design is **least-recently-selected generation
eviction, with deterministic key ordering inside a generation**. This agrees
with the brief; no contradiction was found between that key fact and the two
source files.

Policy traits make decisions only. `Catalog` remains responsible for querying
and mutating SQLite, byte accounting, transactions, and returning `Victim`s;
`ContentCache` remains responsible for fetching, durable file publication,
deferred victim effects, journaling, unlinking, validation flags, and telemetry.

## 1. Trait interfaces and call-path hooks

The signatures below are the proposed internal Rust API. The types intentionally
contain values rather than SQLite connections or filesystem handles: policies
cannot mutate the catalog or bypass the apply journal.

```rust
pub(crate) trait AdmissionPolicy: Send + Sync {
    fn select<'a>(
        &self,
        context: &AdmissionContext,
        missing_in_manifest_order: &'a [ContentRef],
    ) -> Vec<&'a ContentRef>;
}

pub(crate) struct AdmissionContext {
    pub capacity_bytes: u64,
    pub resident_bytes: u64,
}

pub(crate) trait ProtectionPolicy: Send + Sync {
    fn protects(
        &self,
        context: &ProtectionContext,
        candidate: &EvictionCandidate,
    ) -> bool;
}

pub(crate) struct ProtectionContext<'a> {
    pub incoming_key: &'a str,
    pub pending_keys: &'a BTreeSet<String>,
}

pub(crate) trait EvictionPolicy: Send + Sync {
    fn rank(
        &self,
        context: &EvictionContext,
        eligible: &mut [EvictionCandidate],
    );
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct EvictionCandidate {
    // Existing cache_objects columns:
    pub key: String,
    pub size: u64,
    pub access_epoch: u64,

    // Policy-neutral optional signals. None means the catalog/provider does not
    // currently record the signal; design 1 populates all of these as None.
    pub signals: EvictionSignals,
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub(crate) struct EvictionSignals {
    pub last_access_sequence: Option<u64>,
    pub access_count: Option<u64>,
    pub expires_at_unix_ms: Option<u64>,
    pub fetch_cost: Option<u64>,
}

pub(crate) struct EvictionContext {
    pub capacity_bytes: u64,
    pub planned_used_bytes: u64,
    pub required_bytes: u64,
    pub batch_epoch: u64,
}
```

`rank` establishes a total victim order; catalog-owned selection walks that
order and stops as soon as `planned_used <= capacity - required_bytes`. Keeping
the stopping rule and all state transitions in `Catalog::reserve` prevents a
policy from corrupting accounting. Implementations must use `key` as their final
tie-breaker. A debug assertion and policy conformance tests enforce that the
result is a permutation and has a total, repeatable order.

### Exact hooks

**`begin_batch(&self, admissions: &[ContentRef])`:** before the existing apply
journal is created, the caller/reconciliation path determines the already
missing references and invokes `AdmissionPolicy::select` once. The resulting
ordered references become the `admissions` passed to `begin_batch` and later to
`materialize_missing_batch`; this preserves the journal's role as the bounded
set of attempted admissions. `ContentCache::begin_batch` then performs its
existing steps unchanged: write and fsync `active-apply.v1`, sync the cache root,
initialize deferred directory/victim collections, and call
`Catalog::begin_batch`. That catalog method still executes `BEGIN IMMEDIATE`,
saves starting usage, increments `epoch` exactly once, and persists the new
epoch in the transaction. Admission selection does not need or observe the new
epoch and cannot bump it; the epoch becomes eviction context only during later
reservations in the active batch.

The implementation should make the precondition explicit by adding an internal
apply-boundary coordinator rather than calling admission policy separately from
scattered fetch workers. Public `begin_batch` need not become a policy API.

**`reserve(&self, key: &str, size: u64)`:** `ContentCache::reserve` keeps the
oversize check, catalog lock, blocked telemetry, and deferred-versus-inline
victim handling. Inside `Catalog::reserve`, after handling a same-key existing
row and computing `planned_used`, the catalog obtains resident candidates.
Rows with `state=0` remain absent from that set. Each candidate is filtered by
`ProtectionPolicy::protects`, then the eligible slice is passed once to
`EvictionPolicy::rank`. The catalog consumes the ranked slice until enough
bytes are freed, returns `StorageFull` without consuming partial victims when
insufficient space remains, marks chosen rows evicted, inserts/updates the
incoming row as pending with the current epoch, and updates byte accounting as
today. The existing same-key replacement path remains catalog logic: it can add
the incoming key's prior resident row directly to returned victims and excludes
that key from the general candidate query (`key<>?1`). It is not a policy choice.

**`commit_batch(&self)`:** no policy is invoked here. This is a deliberate hook
boundary: the policy decision is complete when `reserve` returns victims, but
its physical effects remain deferred. `commit_batch` retains its exact order:
sync deferred object directories; append deferred victim keys to
`active-apply.v1` and fsync it; call `Catalog::commit`; call `evict_victim` for
each chosen victim (unlink, remove validation flag, telemetry/logging); clear
batch directory state; remove the journal. A policy cannot add a commit hook,
reorder victims after journaling, unlink a file, or mutate catalog state.

`rollback_batch` likewise remains policy-free: it drops deferred victims without
unlinking, calls `Catalog::rollback`, runs apply-journal recovery, clears batch
directory state, and therefore restores catalog/file agreement.

## 2. Default implementations and exact compatibility

```rust
pub(crate) struct AdmitEverything;

impl AdmissionPolicy for AdmitEverything {
    fn select<'a>(
        &self,
        _context: &AdmissionContext,
        missing_in_manifest_order: &'a [ContentRef],
    ) -> Vec<&'a ContentRef> {
        missing_in_manifest_order.iter().collect()
    }
}

pub(crate) struct InFlightProtection;

impl ProtectionPolicy for InFlightProtection {
    fn protects(
        &self,
        context: &ProtectionContext,
        candidate: &EvictionCandidate,
    ) -> bool {
        context.pending_keys.contains(&candidate.key)
    }
}

pub(crate) struct LeastRecentlySelectedGeneration;

impl EvictionPolicy for LeastRecentlySelectedGeneration {
    fn rank(
        &self,
        _context: &EvictionContext,
        eligible: &mut [EvictionCandidate],
    ) {
        eligible.sort_by(|a, b| {
            (a.access_epoch, a.key.as_str())
                .cmp(&(b.access_epoch, b.key.as_str()))
        });
    }
}
```

`AdmitEverything` returns every missing candidate in manifest order. This
matches the loop in `materialize_missing_batch`, which reserves sequentially in
input order and stops at the first `StorageFull` before fetching reserved items
concurrently.

`InFlightProtection` makes the current implicit rule visible. In the actual
design-1 query path, pending rows are excluded structurally because candidates
still come only from `state=1`; consequently the predicate normally receives
no pending candidate and returns `false`. Passing the pending-key snapshot makes
the invariant explicit and testable without changing behavior. Fetching and
verification occur while the row is `state=0`, and `finish` changes it to
`state=1`. Separately, an object already returned by `open_file` can survive a
later Unix unlink because the returned `File` owns an open descriptor. That
open-descriptor property protects the reader, not the pathname/catalog row, and
does not require a pin column or an exclusion from victim selection.

`LeastRecentlySelectedGeneration` exactly preserves
`ORDER BY access_epoch, key`: older selection/admission generations first and
lexicographic key order within a generation. `Catalog::begin_batch` increments
the epoch once per batch; `reserve` and `finish` stamp the current epoch. Reads
do not touch the value. For the default policy the preferred implementation may
keep the existing indexed SQL ordering and treat it as the policy's query plan,
rather than materializing and sorting every resident in Rust, provided the trait
conformance tests prove identical output. The trait expresses semantics; it
does not require moving a performant deterministic sort out of SQLite.

## 3. Eviction decision context and future policy data

### Existing persisted data

Design 1 uses the current schema unchanged:

| Location | Existing fields | Policy use |
|---|---|---|
| `cache_objects` | `key`, `size`, `access_epoch`, `state` | Candidate identity, byte benefit, generation rank, and resident/pending/evicted eligibility |
| `cache_meta` | `epoch`, `used_bytes` | Batch generation and capacity accounting |

The current eviction index remains
`cache_objects(state, access_epoch, key)`. SQLite remains the source of truth
for residency and bytes; policy objects hold no mutable shadow residency,
frequency, or recency map.

### Future policy needs

| Future policy | Needed signal | Current support / bounded extension |
|---|---|---|
| True recency LRU | Last-use sequence/time and a touch on successful use | `last_access_sequence`; `access_epoch` could be updated on touch without a schema change, but doing so changes its generation meaning and write profile, so that policy must define the transition explicitly |
| LFU | Persistent access count and touch increment | `access_count`; not present today |
| Size-aware | Resident size, optionally fetch/recompute cost | `size` exists; optional `fetch_cost` does not |
| TTL | Persistent expiry/deadline and clock semantics | `expires_at_unix_ms`; not present today |

The stable trait/context shape prevents an algorithm change from also changing
the policy API: unsupported signals are `None`, and a policy declares/validates
which signals it requires before a batch starts. Design 1 does **not** fabricate
missing values or keep them only in process memory.

No SQLite migration occurs in design 1. If multiple richer policies are later
approved, add one migration-safe, policy-neutral metadata extension (for
example, a `cache_object_policy` table keyed by object key with nullable typed
signal columns), populate it transactionally with `cache_objects`, and expose
those values through the already-defined `EvictionSignals`. The migration must
run under an explicit transaction following the existing legacy `pin_epoch`
migration pattern: build the new structure/indexes, backfill deterministic
defaults or leave optional values null, validate, and commit atomically. A
single optional metadata extension avoids a new schema/interface redesign per
algorithm while preserving SQLite as source of truth. If only true LRU is
chosen, updating `access_epoch` on access is a possible no-column alternative,
but it is a new write behavior and expressly outside design 1.

Policy-derived values that are cheap functions of a decision snapshot (for
example, `size / fetch_cost`) may be computed in memory. Authoritative signals
that evolve across processes or restarts must be persisted transactionally in
SQLite; they may not live in a policy-owned shadow cache.

## 4. Compatibility, migration, and test plan

### Compatibility and migration

- The default policy bundle is constructed internally by `ContentCache::open`;
  no public API, cache layout, journal version, catalog schema, state encoding,
  telemetry counter, or capacity interpretation changes in design 1.
- Existing catalogs open without migration. The legacy `pin_epoch` migration in
  `Catalog::open` remains intact and still recreates
  `cache_objects(state, access_epoch, key)`.
- `Reservation::{Resident, Pending, Refetch}`, `Victim` contents, same-key
  replacement, first-`StorageFull` admission stopping, and autocommit behavior
  remain unchanged.
- A policy identifier/version is unnecessary until a non-default persistent
  policy is supported. Any future persisted signal format must be versioned and
  migrated atomically; opening an unsupported configured policy should fail
  before `begin_batch`, not silently fall back.

### Unit tests

- Admission: `AdmitEverything` returns all missing inputs in exact manifest
  order, including an empty list and duplicate inputs if the caller supplies
  them.
- Protection: pending keys are protected; residents not in the pending set are
  eligible; candidate construction never emits `state=0` rows.
- Eviction: shuffled candidates rank exactly by `(access_epoch, key)`; equal
  generations use key as a total tie-breaker; repeated runs produce identical
  order.
- Catalog parity: for fixed catalog fixtures, the refactored default bundle
  returns the exact victims returned by today's
  `WHERE state=1 AND key<>?1 ORDER BY access_epoch, key`, including the minimum
  prefix needed to fit, same-key replacement, and `StorageFull` without partial
  victim consumption (`failed_reservation_does_not_consume_partial_victims`).
- Epoch semantics: one and only one increment per successful `begin_batch`;
  every reservation/finish in that batch carries the same epoch; resident/open
  operations do not change it; rollback restores the durable epoch through the
  SQLite transaction.
- Policy conformance: a rank result contains every eligible candidate exactly
  once, supplies a deterministic key tie-break, and rejects missing required
  signals before mutation begins.

### Integration and concurrency tests

- Keep `oxdirtest`, `tests/workspace.rs`, existing catalog unit tests, and cache
  tests passing unchanged under the default bundle.
- Run the same manifest repeatedly from equivalent initial catalogs and assert
  identical admitted order, victim keys/order, final resident set, byte gauges,
  and telemetry counts.
- Start concurrent materialization workers after sequential reservation and
  vary fetch completion order. Assert victim order remains the sequential
  reservation decision order and is unaffected by worker scheduling.
- Concurrent readers open a victim before commit while another apply evicts it.
  On Unix, assert the open `File` remains readable after pathname unlink while a
  new `resident`/`open_file` lookup follows catalog state.
- Concurrent cache callers contend through the catalog mutex; from identical
  acquired-lock/reservation order, assert identical victims. Tests must not
  claim deterministic ordering across different lock-acquisition histories,
  because those are different inputs.

### Crash-safety fault scenarios

Inject failure/crash boundaries and reopen through `ContentCache::open`:

1. Before catalog commit after victims are selected: deferred victims are not
   unlinked; SQLite rolls back; victim rows and files remain resident. Preserve
   `crash_before_commit_keeps_evicted_victim_resident_and_on_disk`.
2. After victim keys are appended/fsynced to `active-apply.v1` but before
   `Catalog::commit`: recovery observes rolled-back resident rows and keeps
   their files.
3. After catalog commit but before any or all `evict_victim` calls: recovery
   sees journalled non-residents and removes remaining files.
4. After all unlinks but before journal removal: recovery idempotently tolerates
   missing files and removes the journal.
5. Explicit `rollback_batch`: deferred victims are dropped without unlinking,
   the catalog transaction rolls back, pending admissions are recovered, and
   catalog/file accounting agrees.
6. Fetch/verification failure calls `release`; pending bytes/state are removed
   without exposing an incomplete object as a resident victim.

Each scenario asserts catalog residency, object-tree existence, `used_bytes`,
pending count, and idempotent second recovery. The default-policy refactor must
not move any filesystem side effect into policy evaluation or catalog victim
selection.

## 5. Explicit non-goals

- No new eviction behavior: no true LRU/touch, LFU, ARC, cost-aware ranking,
  TTL expiry, randomization, or adaptive policy.
- No schema migration, new index, journal-format change, or repurposing of
  `access_epoch` in design 1.
- No persistent user-facing policy configuration or runtime policy switching.
- No admission budgeting, priority classes, prefetching, reordering, or skipping;
  all missing candidates are still attempted in manifest order until the first
  `StorageFull` result.
- No new pin/lease system. In-flight pending-state exclusion and Unix
  open-descriptor semantics remain the only protection mechanisms.
- No cross-process policy daemon, in-memory source-of-truth mirror, or shadow
  residency/access database.
- No changes to fetch concurrency, content validation, object-tree layout,
  capacity accounting, telemetry semantics, or recovery of orphan temps and
  pending rows.
- No portability promise that open-file-after-unlink behaves identically on
  non-Unix platforms; design 1 documents and preserves the current Unix
  mechanism.

## Hard constraints satisfied

- [x] **Crash safety:** policy decisions stop at victim selection. The existing
  reserve/materialize/begin/commit/rollback protocol remains: object and
  directory durability, deferred victims, fsynced apply journal, catalog commit,
  post-commit `evict_victim`, and journal-driven recovery.
- [x] **Determinism:** the default total order is exactly
  `ORDER BY access_epoch, key`; manifest admission order and minimum-prefix
  victim selection remain stable, including under concurrent fetch completion.
- [x] **SQLite source of truth:** catalog rows and `used_bytes` remain
  authoritative. Policies receive immutable snapshots and keep no mutable
  shadow residency or historical signal state.
- [x] **Composable apply-boundary hooks:** admission runs once for the missing
  manifest candidates; protection and eviction run inside reservation candidate
  selection; commit performs effects only. Policy logic is not scattered across
  reads, fetch workers, unlinking, or recovery.
- [x] **Current semantics named accurately:** neither the current nor default
  design-1 policy is described as LRU; it is least-recently-selected generation
  eviction with deterministic key ordering inside a generation.
- [x] **No schema churn today:** current columns, state values, migration, and
  eviction index remain unchanged; future persistent signals have one bounded,
  transactional extension path through `EvictionSignals`.
- [x] **Dependency policy:** the refactor requires only Rust standard-library
  traits/types and existing `rusqlite`; it introduces no dependency, copyleft or
  otherwise. Any future dependency remains subject to the repository's allowed
  MIT/Apache/BSD/ISC/MPL-2.0 licensing policy.

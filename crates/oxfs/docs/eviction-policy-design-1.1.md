# Eviction policy design 1.1: concrete API, CLOCK-LRU, LFU, and oracle

## 1. Concrete Rust API

The policy vocabulary lives in the `oxfs` library, not in the simulator. The
library exports it only as far as its bins and test harnesses require; the
production `oxfsd` binary continues to contain the real implementation but no
reference model. The current workspace has only `crates/oxfs` as a member
(`Cargo.toml:1-3`) and `oxfs` declares all three binaries beside its library
(`crates/oxfs/Cargo.toml:8-31`); section 4 gives the exact dependency split.

```rust
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum EvictionOrder {
    LeastRecentlySelectedGeneration,
    ClockSecondChance,
    ApproxLeastFrequentlyUsed,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Touch {
    None,
    SetRefBit,
    BumpFrequency,
}

impl EvictionOrder {
    pub const fn touch(self) -> Touch {
        match self {
            Self::LeastRecentlySelectedGeneration => Touch::None,
            Self::ClockSecondChance => Touch::SetRefBit,
            Self::ApproxLeastFrequentlyUsed => Touch::BumpFrequency,
        }
    }
}

pub(crate) struct AdmissionCandidate<'a> {
    pub content: &'a ContentRef,
    pub key: String,
}

pub(crate) struct AdmissionContext<'a> {
    pub capacity_bytes: u64,
    pub resident_keys: &'a BTreeSet<String>,
}

pub(crate) trait AdmissionPolicy: Send + Sync {
    fn select(
        &self,
        context: &AdmissionContext<'_>,
        manifest_order: &[AdmissionCandidate<'_>],
    ) -> Vec<ContentRef>;
}

pub(crate) struct AdmitEverything;

impl AdmissionPolicy for AdmitEverything {
    fn select(
        &self,
        context: &AdmissionContext<'_>,
        manifest_order: &[AdmissionCandidate<'_>],
    ) -> Vec<ContentRef> {
        let mut seen = BTreeSet::new();
        let mut selected = Vec::new();
        for candidate in manifest_order {
            if context.resident_keys.contains(&candidate.key)
                || !seen.insert(candidate.key.clone())
            {
                continue;
            }
            if candidate.content.size > context.capacity_bytes {
                break;
            }
            selected.push(candidate.content.clone());
        }
        selected
    }
}

pub(crate) struct ProtectionContext<'a> {
    pub incoming_key: &'a str,
}

pub(crate) struct EvictionCandidate<'a> {
    pub key: &'a str,
    pub size: u64,
    pub access_epoch: u64,
    pub insert_seq: u64,
}

pub(crate) trait ProtectionPolicy: Send + Sync {
    fn protects(
        &self,
        context: &ProtectionContext<'_>,
        candidate: &EvictionCandidate<'_>,
    ) -> bool;
}

pub(crate) struct PendingStateProtection;

impl ProtectionPolicy for PendingStateProtection {
    fn protects(
        &self,
        _context: &ProtectionContext<'_>,
        _candidate: &EvictionCandidate<'_>,
    ) -> bool {
        false
    }
}

pub(crate) struct CachePolicies {
    pub admission: Arc<dyn AdmissionPolicy>,
    pub protection: Arc<dyn ProtectionPolicy>,
    pub eviction: EvictionOrder,
}
```

`AdmitEverything` deliberately owns all three existing decisions: residency
skip, first-occurrence deduplication, and break at the first oversized object.
It therefore replaces, rather than wraps, the loop at
`crates/oxfs/src/workspace.rs:192-203`. `Workspace::apply` first obtains the
catalog-derived resident set, constructs key-bearing candidates in the order of
`admissions` (`workspace.rs:180-191`), and calls `select`. Only the returned
content goes to `materialize_missing_batch`, whose reservations are sequential
and stop on `StorageFull` (`cache.rs:420-437`). This v1 split applies only to
`Workspace::apply`; the per-entry restore loop in `Workspace::open`
(`workspace.rs:74-126`) remains unchanged.

Admission does **not** change `begin_batch`'s argument. The full
`admission_content` at `workspace.rs:204-208` still goes to
`ContentCache::begin_batch`, because that method journals every desired key
before opening the catalog transaction (`cache.rs:257-295`). The selected
missing list remains the distinct argument to `materialize_missing_batch` at
`workspace.rs:209`. This preserves the present recovery boundary.

`EvictionOrder` is an ordering selector, not a `rank(&mut Vec<_>)` trait.
`ContentCache::reserve` retains the oversize guard, catalog mutex, telemetry,
and deferred/inline victim handling (`cache.rs:491-532`). It passes the enum and
protection policy to `Catalog::reserve`; the catalog chooses one of three lazy,
early-stopping scans. The legacy scan remains `ORDER BY access_epoch, key`,
matching `cache_catalog.rs:227-257`. CLOCK uses the persisted ring index and LFU
uses its in-memory ordered frequency index. No path materializes and sorts all
resident rows per reservation. Candidate eligibility remains `state=1`; pending
rows are structurally excluded just as they are today (`cache_catalog.rs:231-233`).
Consequently `PendingStateProtection` is intentionally inert v1 scaffolding,
and there is no pending-key snapshot per reserve.

There is no policy hook in commit or rollback. `commit_batch` must retain the
order “sync object directories; append/fsync victims; commit SQLite; unlink
victims; remove journal” (`cache.rs:298-369`), and rollback must continue to
discard deferred victims before SQLite rollback and journal recovery
(`cache.rs:371-388`). Same-key replacement remains catalog-owned
(`cache_catalog.rs:208-225,266-303`). Every catalog order is total and ends in
`key`: `(access_epoch, key)`, `(insert_seq, key)`, or
`(effective_frequency, insert_seq, key)`.

The touch hook is after a successful open, not after a mere catalog lookup.
NFS `read` calls `Workspace::open_inode` (`nfs/server.rs:304-329`), which reaches
`ContentCache::open_file` for non-synthetic content
(`workspace.rs:402-417`). `open_file` currently validates residency and opens
the pathname at `cache.rs:654-665`; after `File::open` succeeds it calls
`policy_state.touch(key, eviction.touch())`. A failed open, a synthetic file,
and an internal `resident()` probe do not touch. The in-memory operation occurs
under the same policy-state mutex used by victim selection, making each serial
trace linearizable without adding a SQLite write to reads.

## 2. CLOCK-LRU

CLOCK uses a **dense-slot bitset**, not `HashMap<String, bool>`. One bit per
resident is about 122 KiB per million objects, while a key-owned hash map repeats
long content keys and has allocation/bucket overhead. A runtime
`HashMap<String, SlotId>` is still required to turn an opened content key into a
dense slot, but its value is a compact integer and the bit itself lives in
`Vec<u64>`; the ring order and residency remain authoritative in SQLite.

`insert_seq` is not used directly as the bit index. It is monotonic and never
reused, so direct indexing would leak address space as objects churn. On open,
the catalog streams residents in `(insert_seq, key)` order and assigns dense
slots `0..resident_count`; it builds `key_to_slot`, `slot_to_key`, and the
zero-filled bitset. On admission, the object receives the next persisted
`insert_seq` and either consumes a slot from a free list or appends one. On
eviction/release/mark-missing, its slot is cleared and returned to the free list.
A reused slot is always cleared before its new key is installed. The slot map is
therefore process-local and recyclable; `insert_seq` remains the stable ring
coordinate across eviction, refetch, compaction of slots, and restart.

The schema becomes:

```sql
ALTER TABLE cache_objects ADD COLUMN insert_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE cache_meta ADD COLUMN next_insert_seq INTEGER NOT NULL DEFAULT 1;
ALTER TABLE cache_meta ADD COLUMN clock_hand INTEGER NOT NULL DEFAULT 0;
DROP INDEX IF EXISTS cache_objects_clock;
CREATE INDEX cache_objects_clock
    ON cache_objects(state, insert_seq, key);
```

Migration assigns distinct positive `insert_seq` values to existing rows in
`(access_epoch, key)` order, including non-residents so a later refetch can
simply receive a fresh sequence. `next_insert_seq` is one greater than the
maximum. New pending admissions receive and persist a fresh sequence in the
same transaction as the upsert now at `cache_catalog.rs:283-293`; refetch is a
new ring insertion, never reuse of an old sequence. The hand stores the next
sequence to inspect, with `0` meaning “start at the smallest resident sequence.”

For a reservation needing bytes, `Catalog` performs a keyset sweep, never an
offset scan:

1. Query a bounded page with `state=1 AND key<>incoming AND
   (insert_seq > hand OR (insert_seq = hand AND key >= hand_key)) ORDER BY
   insert_seq, key`; after exhaustion, wrap with the same indexed order from the
   beginning. `hand_key` is transient and only disambiguates migration-era or
   defensive equal sequences; `clock_hand` persists the sequence.
2. For each row, resolve its slot. If its bit is one, clear it and advance the
   hand. If zero and protection does not exclude it, append it as a victim,
   subtract its size, clear/free its slot, and advance the hand.
3. Stop immediately when the incoming object fits. If the scan reaches the
   starting coordinate after a full pass with all bits cleared but not enough
   bytes, continue once over those now-zero entries. If the available resident
   bytes are insufficient, return `StorageFull` without applying any staged row,
   hand, slot, or bit changes, preserving the all-or-nothing behavior tested at
   `cache_catalog.rs:484-509`.
4. Persist the advanced `clock_hand` in the same SQLite transaction as victim
   state changes and the incoming reservation. In autocommit mode it is part of
   that reservation; in a batch it commits with the existing catalog commit at
   `cache_catalog.rs:140-149`. A rollback restores both the database hand and an
   in-memory policy-state checkpoint taken at `begin_batch`.

Pages are fixed-size (for example 256 rows), so memory is O(page + K), work is
the victims plus second chances actually encountered, and the covering index
supports early stop. A full all-referenced revolution is CLOCK's amortized
worst case: it clears each bit once, after which subsequent selection is cheap.

A successful `ContentCache::open_file` sets exactly one bit after the file has
opened (`cache.rs:654-665`). The touch never changes `insert_seq` or SQLite.
Admission/finish initializes its bit to zero; `finish` is currently the
pending-to-resident transition at `cache_catalog.rs:306-319`.

The hand **persists across restart**. Persisting it avoids a permanent bias
toward low insertion sequences after every daemon restart; resetting it would
make restart frequency part of victim selection. Reference bits intentionally
reset to zero because they are advisory. `CacheModel::crash_recover` performs
the identical semantic transition: retain residents, sizes, insertion
sequences, and hand; discard pending/uncommitted state; set all resident bits to
zero. Thus a crash changes future ordering only through the specified loss of
advisory touches, never catalog residency.

## 3. LFU

LFU uses **per-resident aging `u8` counters**, not a count-min sketch. A sketch
is excellent for admission-frequency estimates but collisions make resident
victim order indirect and require a second candidate mechanism. One byte per
resident gives a directly observable, model-mirrorable rank, exact key-to-count
updates, and predictable memory at millions of files. It reuses CLOCK's dense
slot allocation and key lookup.

The implementation maintains 256 ordered buckets
`[BTreeSet<(insert_seq, key)>; 256]` plus `Vec<u8>` counters. A successful open
moves the key from bucket `f` to `min(f + 1, 255)`. To prevent immortal saturated
entries, every 65,536 successful touches starts an aging revolution. Each later
touch performs one bounded maintenance step: visit the next resident in
`(insert_seq, key)` ring order, replace `f` with `f >> 1`, move its bucket entry,
and advance the aging hand. After one revolution aging stops until the next
65,536-touch boundary. Touch cost is bounded and there are zero read-path
SQLite writes.

Victim selection visits buckets from 0 through 255 and streams each bucket's
ordered entries, validating `state=1`, size, and incoming-key exclusion against
the catalog before staging a victim. This is a lazy in-memory index backed by
catalog validation: it stops as soon as K victims free enough bytes and never
sorts M residents per reserve. Order is
`(effective_frequency, insert_seq, key)`. Catalog transitions update the bucket
index under the catalog/policy-state lock, while SQLite remains authoritative
for residency and `used_bytes` (`cache_catalog.rs:274-295,418-443`). A stale
index entry can only be skipped/repaired, never authorize eviction of a
non-resident row.

Counters, bucket membership, touch count, and aging hand are advisory and are
**not flushed to SQLite**. On clean close or crash they are discarded. Restart
streams current residents by `(insert_seq, key)`, initializes every counter to
zero in bucket 0, resets the touch count and aging hand, and preserves only
catalog residency and insertion order. `CacheModel::crash_recover` does exactly
the same. This explicit cold-start behavior avoids schema/index churn and all
read amplification; serial differential tests remain exact because restart is
an operation in the shared trace. There is no background flusher and therefore
no partially flushed frequency state to reconcile.

## 4. The cachesim crate

Add workspace member `crates/cachesim`, whose normal dependency is `oxfs` for
the shared vocabulary and content-reference types. `oxfs` has **no normal or
optional dependency on `cachesim`**. Tests and the `oxjtest` binary may depend
on `cachesim` through a dev-only/test-harness arrangement or by moving
differential driving into the cachesim package; `oxfsd` remains the ordinary
`oxfs` bin at `crates/oxfs/Cargo.toml:21-23`. `cargo tree -p oxfs --bin oxfsd`
must contain no `cachesim`. No feature flag is used, so workspace feature
unification cannot defeat the boundary.

The pure model has no SQLite, filesystem, threads, or callbacks into
`ContentCache`:

```rust
pub struct CacheModel {
    pub capacity: u64,
    pub order: EvictionOrder,
    pub state: ModelState,
}

pub struct ModelState {
    pub committed: BTreeMap<String, ModelObject>,
    pub working: Option<ModelBatch>,
    pub used_bytes: u64,
    pub epoch: u64,
    pub next_insert_seq: u64,
    pub clock_hand: u64,
    pub clock_bits: BTreeMap<String, bool>,
    pub frequencies: BTreeMap<String, u8>,
    pub lfu_touch_count: u64,
    pub lfu_aging_hand: Option<(u64, String)>,
}

pub struct ModelObject {
    pub size: u64,
    pub access_epoch: u64,
    pub insert_seq: u64,
    pub state: ModelObjectState,
}

pub enum ModelObjectState { Pending, Resident, Evicted }

pub struct ModelSnapshot {
    pub resident: BTreeMap<String, u64>,
    pub pending: BTreeSet<String>,
    pub used_bytes: u64,
    pub clock_hand: u64,
}

impl CacheModel {
    pub fn apply(&mut self, manifest: &[ModelAdmission]) -> ModelApplyOutcome;
    pub fn touch(&mut self, key: &str);
    pub fn crash_recover(&mut self);
    pub fn snapshot_state(&self) -> ModelSnapshot;
}
```

`apply` independently performs v1 admission (resident skip, first-key dedup,
oversize break), begins a working copy, reserves in order until the first
capacity failure, marks successful fetches resident, and commits. Fault-trace
operations can stop at begin/reserve/finish/commit boundaries so recovery is
also modeled. The simple model is intentionally allowed to sort its
`BTreeMap`: generation order sorts by `(access_epoch,key)`; CLOCK materializes a
sorted `(insert_seq,key)` ring and applies bits/hand; LFU normalizes its aging
state and sorts by `(frequency,insert_seq,key)`. It does **not** reuse the
catalog's SQL, page cursor, dense slots, bucket sets, or victim-selection code.
Only the enums, touch meaning, and constants are shared. This different
mechanism is what lets the diff catch wrong predicates, cursor wrap bugs, stale
slot/bucket entries, missing second chances, and incorrect tie-breaking rather
than merely catching call-path plumbing errors.

For CLOCK, model bits are keyed by key for simplicity even though the real
implementation uses dense slots. `crash_recover` retains the persisted hand and
zeros bits. For LFU it zeros all frequencies and resets aging state. For all
orders it rolls back an uncommitted working batch, removes pending objects as
the real open path does at `cache.rs:725-756`, recomputes bytes from committed
resident rows, and leaves no journal-only victim resident mismatch.

## 5. The differential harness

The harness adds an oracle lane alongside the current deterministic `gate`
driver (`oxjtest.rs:192-225`) and the evolving apply site at
`oxjtest.rs:764-829`, especially `workspace.apply` at line 791. A
`DifferentialWorkspace` owned by the harness contains the real `Arc<Workspace>`,
a `Mutex<CacheModel>`, and an ordered event recorder. It is test/harness code,
not an `oxfsd` dependency.

For apply, the driver gives the same manifest and configured capacity/order to
the model and real workspace. The model computes its result without inspecting
the real victim list. After the real `Workspace::apply` returns, the driver
compares outcomes and snapshots. It does not tee at `begin_batch`, because that
would repeat the design-1 mistake: the full journal set at
`workspace.rs:204-209` is not the selected missing set.

For serial touch replay, the gate uses a harness-provided read method (or an NFS
server observer supplied only by `oxjtest`) that assigns a monotonically
increasing event number. A successful real `open_inode`/`open_file` first
performs the library touch, then reports `Touch { sequence, key }`; the harness
immediately calls `model.touch(key)` before issuing the next read or apply.
Because NFS reaches `open_inode` at `nfs/server.rs:314` and cache open at
`workspace.rs:402-417`, this records the actual successful-open boundary rather
than guessing from paths. Reads are quiesced around each apply in exact mode.

With live concurrent NFS reader threads, callback scheduling cannot prove the
same linearization as the cache-policy mutex: a bit set can race the eviction
hand. The driver records touches for diagnostics but marks the interval
`Concurrent`. It stops asserting policy-exact victim/resident equality and
checks directional invariants at quiescent generation boundaries. The existing
reader stress at `oxjtest.rs:323-374` remains intact and continues to assert
byte integrity.

The exact invariant sets are:

**Universal (every lane and concurrency mode).**

- `resident_bytes == sum(snapshot.resident sizes) <= capacity`; no pending rows
  remain after successful commit or crash recovery.
- Every resident catalog key has exactly one correctly sized object file; every
  journal-listed non-resident key is absent after recovery. A second recovery is
  idempotent.
- Successful admissions form a prefix of the policy-selected missing sequence;
  oversize and `StorageFull` stop admission; dedup admits a key at most once.
- A failed reservation consumes no victims; rollback preserves pre-batch
  residents and bytes. Victim unlink occurs only after durable commit.
- Resident/open miss behavior agrees with the snapshot, and an already-open
  descriptor remains readable after pathname eviction, as exercised at
  `oxjtest.rs:376-403`.
- All observable victim orders are total and terminate in `key`; replaying the
  same seed, operation schedule, policy, and crash points gives the same result.

**Cross-lane exact (serial touches; reads quiesced during apply).**

- `ApplyOutcome`, admitted prefix, ordered victim keys, resident key-to-size
  map, pending set, and `used_bytes` are identical after every operation.
- CLOCK hand and per-key reference bits are identical; LFU counter, touch-count,
  and aging-hand state are identical at every comparison point.
- After `crash_recover`, both lanes retain the same residents and CLOCK hand,
  zero the same CLOCK bits or LFU counters, and choose the same later victims.

**Cross-lane directional (concurrent NFS readers).**

- Keys admitted successfully in both lanes and not selected as victims by
  either lane are present in both snapshots (“model must-be-present” is a lower
  bound, not an assertion that race-sensitive victim sets are equal).
- A key evicted for a non-touch-dependent cause in both lanes—same-key
  replacement, corruption/mark-missing, release after failed fetch, or explicit
  recovery cleanup—is absent in both and does not reappear without a later
  successful admission.
- Resident sets may differ only among keys whose CLOCK bit/LFU count could have
  been affected by a touch concurrent with victim selection. Their symmetric
  difference is a subset of that recorded race window; outside it, membership
  and sizes match exactly.
- Each lane independently satisfies every universal capacity, accounting,
  prefix, on-disk, journal, rollback, and open-descriptor invariant.

## 6. `resident_keys()`/`snapshot_state()` placement on `Catalog` -> `ContentCache` -> `Workspace`

`Catalog::resident_keys(&self) -> io::Result<Vec<String>>` is the single
authoritative query: `SELECT key FROM cache_objects WHERE state=1 ORDER BY key`.
It sits beside `pending_keys` (`cache_catalog.rs:389-398`), not beside policy
state, and is used for ordinary reconciliation as well as tests.

`Catalog::snapshot_state(&self) -> io::Result<CatalogSnapshot>` returns resident
`(key,size,access_epoch,insert_seq)` rows in key order, pending keys,
`used_bytes`, `epoch`, and persisted `clock_hand`, all while the catalog mutex is
held. `ContentCache::resident_keys` simply locks the catalog and forwards the
query. `ContentCache::snapshot_state` takes the catalog/policy-state locks in
the same fixed order used by reserve, combines `CatalogSnapshot` with observable
CLOCK bits or LFU counters keyed by content key, and returns a value snapshot;
it never exposes SQLite or mutable internals.

`Workspace::resident_keys` and `Workspace::snapshot_state` forward to the cache
for the differential harness. Their visibility is the minimum needed by the
library/test boundary. The existing free helper named `resident_keys` at
`workspace.rs:38-49` is different and should be renamed
`resident_desired_keys`: it probes only keys found in supplied manifests and
cannot serve as a catalog snapshot. Its call sites after recovery and apply are
`workspace.rs:126` and `workspace.rs:220-223`. This placement avoids filesystem
scans and makes the real lane observable through the same
`Catalog -> ContentCache -> Workspace` ownership path as capacity and telemetry
(`workspace.rs:388-400`).

## 7. Migration/compat notes + test plan + explicit non-goals

Schema detection follows the existing `pin_epoch` migration pattern:
`Catalog::open` creates/opens the schema, inspects `PRAGMA table_info`, and runs
an explicit `BEGIN IMMEDIATE` rebuild/backfill/index/`COMMIT`
(`cache_catalog.rs:40-98`). The migration adds/backfills `insert_seq`, adds
`next_insert_seq` and `clock_hand` to `cache_meta`, and creates
`cache_objects(state,insert_seq,key)` without removing the legacy
`(state,access_epoch,key)` index needed by the default order. It validates
unique positive resident sequences and `next_insert_seq > MAX(insert_seq)`
before commit. SQLite integer conversion continues through the checked helpers
at `cache_catalog.rs:464-475`. No journal version or object-tree layout changes.

Opening an old catalog selects `LeastRecentlySelectedGeneration` by default and
preserves current behavior. Existing `access_epoch` stamping in reserve/finish
(`cache_catalog.rs:283-313`), capacity accounting, state values, telemetry,
same-key refetch, sequential reserve/concurrent fetch behavior, and deferred
victim protocol remain compatible. CLOCK and LFU policy selection is fixed for
the lifetime of an open `ContentCache`; runtime switching is rejected. CLOCK's
hand and sequence allocation participate in the existing transaction, whereas
both policies' touch hints are advisory and reset as specified after restart.

The implementation sequence is oracle-first: (1) add snapshots and the
`cachesim` baseline for the current order; (2) extract admission/protection and
the enum-selected legacy scan under exact differential testing; (3) add CLOCK;
(4) add LFU. Required tests are:

- Admission tables covering residency skip, first-key dedup, manifest order,
  first-oversize break, and first-`StorageFull` prefix behavior.
- Legacy parity against the current indexed query at
  `cache_catalog.rs:231-257`, including same-key replacement and
  `failed_reservation_does_not_consume_partial_victims`
  (`cache_catalog.rs:484-509`).
- CLOCK unit/property traces for wrap, all-bits-set second revolution, slot
  reuse with a cleared bit, refetch with a fresh sequence, key tie-break,
  persisted hand, rollback restoration, and restart-zeroed bits.
- LFU traces for saturation, bucket moves, exact 65,536-touch aging start,
  one-step bounded aging, tie-breaks, stale-index repair, and restart cold
  reset.
- Differential seeded state-machine traces for every order: apply, touch,
  failed fetch/release, corruption/mark-missing, begin/reserve/finish/commit,
  rollback, and crash at every journal/catalog/unlink boundary. Serial mode must
  produce exact snapshots; concurrent mode must satisfy the directional set.
- Preserve `crash_before_commit_keeps_evicted_victim_resident_and_on_disk`
  (`cache.rs:870-920`) and add crashes (a) after journal victim fsync but before
  catalog commit, (b) after commit before each unlink prefix, and (c) after all
  unlinks before journal removal. Each checks rows, files, bytes, pending state,
  hand semantics, and idempotent second recovery.
- Keep the current gate assertions for partial-fetch invisibility
  (`oxjtest.rs:285-311`), stale generations (`oxjtest.rs:314-321`), concurrent
  readers (`oxjtest.rs:323-374`), open-descriptor survival
  (`oxjtest.rs:376-403`), restart corruption healing (`oxjtest.rs:416-430`),
  oversize stopping and capacity (`oxjtest.rs:432-457`), plus all workspace,
  cache, catalog, and `oxdirtest` tests. Repeat identical serial seeds to prove
  deterministic admitted/victim order and final state.
- Assert dependency isolation with `cargo tree`: `cachesim` is absent from the
  `oxfsd` graph. Add no dependency beyond the standard library and existing
  permissively licensed crates; in particular no copyleft simulator library.

Explicit non-goals are: changing `Workspace::open`/recovery to the batch
admission trait in v1; exact recency or persistent read history; persistent LFU
counters; sorting all residents per reserve; S3-FIFO/Sieve, TTL, ARC, Belady in
the shipped implementation (Belady remains policy-lab-only); runtime policy
switching or customer-facing configuration; admission priorities/budgets;
live-open leases or pins; a new journal format; changing fetch concurrency,
validation, telemetry meaning, object layout, or capacity semantics; and making
`cachesim` available to `oxfsd` through a feature flag or any other dependency
path.

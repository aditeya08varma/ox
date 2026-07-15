# Millions-of-small-files: problems in the oxfs setup

Requested analysis (2026-07-14 planning). The corpus oxfs targets is **many
objects, most tiny**: oxjtest's own size distribution (`oxjtest.rs:1029-1041`) is
~68% ≈606 B, a middle tail to 128 KiB, 0.99% at 1.4 MB, 0.01% at 12.7 MB. At
millions of objects the median object is smaller than a filesystem block and
smaller than a single NFS RPC's framing. That inverts the usual assumptions.

## 1. NFS read-bytes reality (the emphasis)

For a 606 B file the **useful READ payload is dwarfed by protocol overhead**, and
`rsize` is irrelevant — you never fill a read.

- **Round-trips per file, not bytes per second.** Reading one small file over the
  mount is a sequence of LOOKUP → ACCESS → GETATTR → READ (+ OPEN/CLOSE at the
  VFS layer). ~4–5 RPCs to deliver ~606 useful bytes. Throughput is **latency-
  bound** (RPC round-trips × file count), never bandwidth-bound. Wire bytes are
  dominated by RPC headers + NFS3 arg/result structs, not file data.
- **`rsize`/`wsize` tuning buys nothing.** The knob that matters for big files
  (large reads) does nothing here; every read is a single sub-block transfer.
- **The read path is CPU-bound before it is IO-bound.** Each NFS READ →
  `open_inode` → `open_file` → `resident()` re-validates size **and SHA-256**
  unless the key is in the in-process `validated` set (`cache.rs:199-255`,
  `:219-234`). Cold-reading millions of small files = millions of SHA-256 hashes.
  Small files make the per-byte hash overhead (setup/finalize dominates) worst.
- **The `validated` set grows unbounded in memory** — one `String` key (~80 chars:
  `sha256hex/sha256-hex`) per hot object, `Mutex<BTreeSet<String>>` (`cache.rs:93`).
  At millions of hot objects this is tens–hundreds of MB of process memory that
  never shrinks within a run.

## 2. Host-filesystem problems (the biggest one)

### Block-rounding space amplification — capacity accounting is *wrong* at this scale
Each object is one file under `objects/<sha256(tenant)>/<storage_key>`, written
temp→fsync→rename (`cache.rs:567-611`). Capacity is tracked as **logical bytes**:
`used_bytes = Σ size`, gated against `max_bytes`. But APFS allocates in **4 KiB
blocks**. A 606 B object consumes 4 KiB on disk → **~6.7× amplification**.

**Consequence:** with millions of ~606 B objects, the cache believes it is at
1 GiB (`DEFAULT_CACHE_MAX_BYTES`) while consuming **~6–7 GiB of real disk**. The
capacity invariant the eviction work is built around (`resident_bytes ≤
max_bytes`) is a **logical** bound that does not bound physical disk use. Any
eviction policy that trusts `used_bytes` under-evicts relative to actual disk
pressure. This is correctness-adjacent, not cosmetic — it can fill the volume
while the cache reports headroom.
> Options: track block-rounded size (`ceil(size/4096)*4096`) in accounting, or
> apply a small-object headroom factor, or pack small objects (see §5). Flag for
> the eviction/oracle design: the oracle should be able to assert a *physical*
> bound, not only the logical one (the oxjtest gate already walks physical bytes
> — `cache_object_bytes`, `oxjtest.rs:1067` — so the two can diverge and be
> caught).

### One host directory per tenant, millions of entries
All objects for a tenant sit flat in `objects/<sha256(tenant)>/`. APFS copes with
large directories, but: directory-metadata ops degrade, `readdir` of the object
store (recovery's `walk`, `tmp` sweep at `cache.rs:685`) becomes O(millions), and
any host-side tooling (`ls`, backup, AV scan) chokes. **Recovery that scans the
object tree is O(total objects), not O(changed)** — restart cost grows with cache
size.

### fsync-per-object at admission
`materialize_missing_batch` fetches in parallel but each object is durably written
with its own `sync_all()` (`cache.rs:605`) before rename. Admitting N tiny objects
= N fsyncs. Small-file write throughput under fsync is a classic bottleneck; the
one batched directory sync doesn't amortize the per-file fsyncs. At high churn
(oxjtest `serve` churns 10%/gen) this dominates apply latency.

## 3. Catalog / cache problems at scale

- **Long TEXT primary key, millions of rows.** `cache_objects` is `WITHOUT ROWID`
  with an ~80-byte string PK (`cache_catalog.rs:57-63`) plus the eviction index.
  Millions of long-string keys → large B-trees, heavy page-cache pressure, slower
  lookups/scans. The F4 index-backed early-stop scan still pays long-key
  comparison costs. A content-hash surrogate (fixed 32-byte blob, or an integer
  id) would shrink the index materially — worth considering if catalog size bites.
- **`gauges()` is a full-table scan** (`SUM/COUNT` over all rows,
  `cache_catalog.rs:422-424`). `cache_telemetry()` calls it; at millions of rows
  every telemetry read is O(rows). `serve` reads telemetry **every generation**
  (`oxjtest.rs:799-822`) → O(rows) per generation. (Capacity's `used_bytes` is
  maintained incrementally and is fine; only the gauges are scans.) Make
  resident_objects/bytes incremental counters if telemetry is hot.
- **Eviction churn is high when objects are tiny.** To free room for one incoming
  object, the victim scan may evict *many* sub-block objects. K (victims/reserve)
  is larger and more variable; the deferred-victim journal (`active-apply.v1`)
  gets many keys per batch.

## 4. Interaction with the eviction / CLOCK / oracle work

- **CLOCK ref-bit memory** must be a **dense-id bitset**, not `HashMap<String,bit>`
  — 1 M objects as a bitset is ~125 KB; as a string-keyed map it's 100 MB+. This
  is why the north-star fixes CLOCK on an `insert_seq`-indexed bitset. Confirmed by
  scale. (Same argument kills any per-key in-memory recency map.)
- **LFU frequency must be approximate/compact** for the same reason — a per-key
  exact counter map is the same 100 MB+ trap; a count-min sketch or aging counter
  keyed by dense id stays bounded.
- **The oracle runs at oracle scale, not soak scale.** The model's `HashMap` +
  sort is fine at thousands (correctness); millions is impl-only soak. Do not run
  exact-match differential at millions — run it at a scale where the model is cheap
  and rely on invariants + impl-only telemetry at millions.
- **The physical/logical gap belongs in the oracle's invariant set.** A universal
  invariant "physical bytes on disk ≤ some function of capacity" catches the §2
  block-amplification bug that the logical `resident_bytes ≤ max_bytes` invariant
  cannot see.

## 5. What to measure / consider (not yet decisions)

- Instrument **physical vs logical** cache bytes (already both available:
  `cache_capacity()` logical, `cache_object_bytes` physical) and alert on the gap.
- Consider **block-rounded accounting** or a **small-object headroom factor** so
  capacity bounds real disk.
- Consider **sharding the object dir** (e.g. two-hex-nibble fan-out
  `objects/<tenant>/ab/cd/<key>`) to bound per-directory entry counts and
  recovery-scan cost.
- Consider **object packing** for sub-block objects (many small objects into one
  packfile with an offset index) — eliminates block amplification and the
  millions-of-inodes problem, at the cost of a compaction story. Big change;
  flag only.
- Consider **incremental gauges** and a **surrogate catalog key** if catalog
  size/telemetry cost shows up in soak.
- On the read path: an **attribute/negative-lookup cache** and honoring NFS
  `actimeo` would cut the GETATTR/LOOKUP storm that dominates small-file reads.

None of these block the eviction/oracle work; §2 (block amplification → logical
capacity ≠ physical) and §4 (dense-id bitset, physical invariant in the oracle)
are the ones that directly touch it.

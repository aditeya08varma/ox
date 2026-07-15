# Claude review of design 1.1 (Codex-authored)

Verdict: **accept with 4 amendments.** CLOCK, admission, migration, the
begin_batch-not-teed point, and the crash/invariant test matrix are right and
thorough. But two findings (R1, R4) reintroduce the exact per-key in-memory
String trap the millions-of-small-files analysis flagged — they must be fixed or
the design misses its headline scale goal. R2/R3 are smaller.

## Accept as-is
- CLOCK as second-chance over a dense-slot ring; `insert_seq` as the stable
  persisted ring coordinate; hand persists across restart; bits reset (advisory);
  keyset paged sweep with early stop; all-or-nothing on `StorageFull`.
- `AdmitEverything` owning dedup + oversize-break + residency-skip, replacing the
  `workspace.rs:192-203` loop; `begin_batch` still journals the full desired set.
- The `pin_epoch`-pattern migration; legacy `(access_epoch,key)` index retained;
  default policy triggers no migration.
- The universal/exact/directional invariant split and the crash-boundary matrix.

## R1 — LFU reintroduces the per-key String memory trap AND an O(log M) hot-path touch
§3 maintains `[BTreeSet<(insert_seq, key)>; 256]`. At millions of residents those
buckets hold **every resident key as a `String`** across the sets → the same
100 MB+/million trap the design took dense slots to avoid for CLOCK
(`small-files-analysis.md §4`). Worse, each touch does a BTreeSet remove+insert =
**O(log M) on the NFS read hot path**, vs CLOCK's O(1) bit — at millions of small
reads that is a real per-read cost. This is internally inconsistent: dense-slot
discipline for CLOCK, String-set discipline for LFU.

**Fix (recommended): dense-slot `Vec<u8>` frequency + sampled eviction.** Touch =
`freq[slot] = freq[slot].saturating_add(1)` (O(1), 1 byte/object = ~1 MB/million).
Eviction samples K resident slots deterministically (seeded stride from a moving
cursor) and evicts min-`freq`, repeating until enough bytes free — O(K)/victim, no
global sort, no String structures. This is the Redis approach and it also proves
the seam supports *sampling* policies (widening the "2–3 more" confidence).
Alternative if exact frequency order is truly wanted: keep the 256 buckets but hold
**slot ids** (`Vec<u32>`/intrusive list), never `(insert_seq,key)` Strings — removes
the memory trap but keeps O(log/const) touch. Prefer sampled.

## R4 — CLOCK (and LFU) still carry a `HashMap<String, slot>`; eliminate it
§2 concedes "a runtime `HashMap<String, SlotId>` is still required to turn an
opened content key into a dense slot." At millions that map **is** the String trap
again (~100 MB+). It's avoidable: `slot` is a persisted column, and the read-path
touch happens *after* `open_file`→`resident()`, which already loads the object's
catalog row. **Thread `slot` out of that residency probe** so `touch(slot)` needs
no key→slot map. Then all in-memory policy state is pure dense arrays (`ref_bits`,
`freq`) indexed by slot — the only way the design actually hits its
millions-of-small-files memory target. (Reserve's sweep already reads `slot` from
the row; only the touch path needed the map, and it doesn't.)

## R2 — add a physical-disk-bytes universal invariant
§5's universal set bounds only *logical* `resident_bytes ≤ capacity`. Per
`small-files-analysis.md §2`, APFS block-rounding makes ~606 B objects cost 4 KiB
→ ~6.7× amplification, so the logical bound does not bound real disk. Add a
universal invariant: **physical bytes on disk ≤ headroom·capacity** (the gate
already walks physical bytes via `cache_object_bytes`, `oxjtest.rs:1067`), so the
logical/physical divergence is caught, not silently filling the volume.

## R3 — keep Codex's good catch: the `resident_keys` name collision
§6 correctly notes the existing free helper `resident_keys` (`workspace.rs`
~:88, called at `:126` and `:220-223`) collides with the new catalog accessor.
Keep the proposed rename of the *existing* helper to `resident_desired_keys`; the
new catalog snapshot accessor takes the `resident_keys` name.

## Net
Fold R1+R4 (dense-slot-only in-memory state; no per-key String maps anywhere;
sampled LFU), R2 (physical invariant), R3 (rename). With those, 1.1 is
implementation-ready and actually scale-correct. Proceeding to implement backwards
from the oracle with these amendments binding.

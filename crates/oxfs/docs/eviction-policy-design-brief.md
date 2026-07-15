# Design brief: composable cache admission / protection / eviction policies

> Seed brief for **design 1** (to be authored by Codex, reviewed by Claude, then
> a human). This file states the problem, the current ground truth, the target
> decomposition, the first implementation, and the hard constraints any design
> must respect. It is NOT the design itself.

## Origin

Walking through the exact semantics of cache eviction with `oxdirtest`
(SageOx session OxxRYg) surfaced that what the code calls/behaves-like "LRU" is
not LRU. There is no access "touch": an object's ordering key is stamped once at
admission and never moved by subsequent use.

**Accurate name for today's policy:**
"least-recently-selected generation eviction, with deterministic key ordering
inside a generation."

## Current ground truth (read before designing)

Catalog: SQLite `cache_objects(key, size, access_epoch, state)`, state =
`pending(0) | resident(1) | evicted(2)`. Source of truth for residency and byte
accounting. Files live in an object tree keyed by `key`.

- **Generation counter** — `epoch` bumps exactly once per `begin_batch`
  (`cache_catalog.rs:124`). One manifest/apply == one epoch.
- **Admission stamp** — on `reserve`, the admitted object's `access_epoch` is set
  to the current `epoch` (`cache_catalog.rs:285-291`). Never updated on read/use.
- **Eviction ranking** — victims chosen by
  `WHERE state=1 AND key<>?1 ORDER BY access_epoch, key`
  (`cache_catalog.rs:231-233`), freeing bytes until
  `planned_used <= capacity - size`; if still short → `StorageFull`.
- **Protection (implicit, today)** —
  1. Pending rows (`state=0`) are excluded from the resident-victim query, so an
     object mid-fetch is never a victim.
  2. An already-open object survives `unlink` because the open fd owns the
     descriptor (Unix). `OpenFile` holding the fd is the protection mechanism.
- **Crash-safety protocol (MUST NOT BREAK)** — inside a batch, victim side
  effects (unlink, validation-flag drop, telemetry) are deferred to
  `commit_batch`. Order: sync object dirs → append victim keys to
  `active-apply.v1` journal (fsync) → catalog `commit` → unlink victims → remove
  journal (`cache.rs:319-368`). Recovery removes any journalled key that is not
  resident after restart. Rollback drops deferred victims WITHOUT unlinking
  (`cache.rs:371-389`). The catalog and object tree must stay in agreement across
  a crash at any point.

## Target decomposition

Every manifest/apply event is resolved through three explicit, composable policy
axes (today they are one tangled implicit policy):

```
manifest / apply event
      │
      ├── Admission policy
      │     Which missing candidates are attempted, and in what order?
      │
      ├── Protection policy
      │     Which current objects are temporarily ineligible for eviction?
      │
      └── Eviction policy
            How are eligible residents ranked / selected?
```

## First implementation (design 1 targets this)

Behavior-preserving where it can be; the point is to make the axes explicit and
pluggable, not to change what happens yet.

- **Admission = admit everything.** Attempt every missing candidate, in manifest
  order (current behavior).
- **Protection = in-flight.** An object being fetched/verified cannot be evicted.
  Document that this is already provided by (a) the `state=1`-only victim query
  and (b) Unix open-fd-survives-unlink. The design should express this as an
  explicit protection predicate even though no new mechanism is needed yet.
- **Eviction = least-recently-selected generation**, correctly named. Preserve
  `ORDER BY access_epoch, key` semantics and determinism.

## Hard constraints for any design

- Keep the crash-safety protocol above intact (deferred victims + apply journal +
  post-commit unlink + recovery).
- Preserve determinism — `oxdirtest`, `tests/workspace.rs`, and catalog unit
  tests assert deterministic victim order. Same input → same victims.
- Keep the SQLite catalog as source of truth. Policies should be expressible
  against existing catalog columns or cheap derived/in-memory state. Flag any
  schema change explicitly and justify it (schema migrations already exist for
  the `pin_epoch` drop — see `cache_catalog.rs:70-96`).
- Rust. No new copyleft deps (repo policy: MIT/Apache/BSD/ISC/MPL-2.0 only).
- Policies compose at the manifest/apply boundary (`reserve` /
  `begin_batch` / `commit_batch`), not scattered.

## What design 1 must deliver

1. Trait/interface shape for `AdmissionPolicy`, `ProtectionPolicy`,
   `EvictionPolicy` — inputs, outputs, and where each is invoked in the existing
   call path (`reserve`, `begin_batch`, `commit_batch`).
2. How the three first-impls slot into those traits.
3. The data an eviction policy is given to rank victims, chosen so a future
   policy (true recency-LRU with touch, LFU, size/cost-aware, TTL) can be added
   **without schema churn** — or with a clearly-bounded, migration-safe change.
4. Compat/migration notes and a test plan (including the crash-safety and
   determinism tests that must keep passing).
5. Explicit non-goals for design 1 (e.g. no new eviction algorithm yet).

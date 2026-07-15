# Claude review of design 1 (eviction-policy-design-1.md)

Reviewer: Claude. Stage 2 of {Codex design → Claude review → human review → impl}.
Verdict: **sound decomposition, correct core instincts, but the hook placement is
mis-mapped and the "single apply-boundary coordinator" is a larger refactor than
the doc implies.** Resolve F1–F4 before implementing. F5–F6 are accept-as-is with
eyes open.

## What the design gets right

- Names the policy correctly (least-recently-selected generation, not LRU) and
  ties it to the actual `access_epoch`-stamped-once behavior.
- Keeps all physical/mutating effects (`unlink`, journal, catalog commit) out of
  policy code — policies are decision-only. This is the right cut and it
  preserves the crash-safety protocol untouched.
- Correctly reasons that victim order is decided at `reserve` time under the
  catalog mutex (sequential), so concurrent fetch completion can't perturb it.
- Honest about protection: the open-fd mechanism protects the reader, not the
  catalog row, and needs no pin column.

## Findings (ranked)

### F1 — There is no single apply choke-point; there are two, with different shapes
The design proposes "an internal apply-boundary coordinator" that runs
`AdmissionPolicy::select` once. But the two callers do admission very
differently:

- **`Workspace::apply`** (`workspace.rs:192-209`) precomputes a deduped, in-order
  `missing: Vec<ContentRef>` (residency-skip + dedup + oversize-break) and hands
  it to `materialize_missing_batch(&missing)`. Admission maps cleanly here.
- **`Workspace::open` / recovery** (`workspace.rs:104-125`) has **no precomputed
  missing set** — it loops entries and calls the *singular*
  `materialize_missing` per entry, with its own inline `stopped` / `available`
  bookkeeping and a per-entry oversize/StorageFull stop.

Unifying these into one coordinator that calls a single `select()` is real work
the doc treats as a footnote. **The design should either (a) scope the coordinator
refactor explicitly as part of design 1, or (b) apply the policy split only to
the `apply` batch path in v1 and leave `open`/recovery on a documented follow-up.**
As written it reads as "drop a trait in," which understates the change.

### F2 — Admission is hooked to the wrong parameter
The doc says the admission result "become[s] the `admissions` passed to
`begin_batch`." But `begin_batch(&admission_content)` today receives the **full
desired set** (`workspace.rs:204-208`), while the **missing subset actually
attempted** is the separate `missing` vec passed to `materialize_missing_batch`
(`:209`). AdmissionPolicy ("which *missing* candidates to attempt, in what
order") maps to the construction of `missing` (`:192-203`), **not** to
`begin_batch`'s argument. The design conflates two distinct sets. Fix the mapping
before impl or the coordinator will hook the wrong list.

### F3 — Admission's scope boundary is undefined (dedup / oversize / residency)
`AdmitEverything::select` just returns the input slice verbatim — so it assumes
the caller already did dedup (`missing_keys.insert`), oversize-break
(`size > capacity`), and residency-skip (`workspace.rs:196-202`). For behavior
preservation, **define whether those three filters live inside admission or
upstream of it.** If they stay upstream, `AdmitEverything` is trivially correct
but the trait doesn't actually own "which candidates" — the caller does. If they
move in, `AdmitEverything` must replicate them exactly. Pick one and state it.

### F4 — Eviction trait vs. SQL index: the default policy bypasses the trait it ships
`EvictionPolicy::rank(&mut [EvictionCandidate])` requires **materializing every
eligible resident into a `Vec` per `reserve` and sorting in Rust**, losing
today's indexed early-stop (`ORDER BY access_epoch, key` streamed from
`cache_objects(state, access_epoch, key)`, broken as soon as enough bytes free —
`cache_catalog.rs:231-256`). Inside one batch with N admissions that is N full
resident scans + sorts — a potential O(N·M) regression on large caches.

The doc hedges: "the preferred implementation *may* keep the existing indexed SQL
ordering ... provided conformance tests prove identical output." But that means
**the default policy doesn't run through `rank()` at all** — the shipped
abstraction isn't the one that executes. Decide explicitly:
- **(a)** Eviction policy is a *materialized Rust comparator* (clean, uniform,
  but eats the scan cost — probably fine at current cache sizes, needs a
  stated bound), **or**
- **(b)** Eviction policy is a *query-plan / ordering provider* (e.g. returns an
  `ORDER BY` clause or a keyset cursor) so the default stays index-fast and
  custom policies still get a real seam.

Right now it's "(a) as the interface, (b) as the implementation," which is the
one option that gives neither guarantee.

### F5 — Protection is inert in impl 1 (accept, but say so plainly)
Candidates come only from `state=1`; `InFlightProtection::protects` only ever
sees `pending_keys`, so it **never returns true for any real candidate**. The
genuinely useful protection (don't evict a resident with a live lease/open
handle) is an explicit non-goal, and open-fd survival needs no policy. So the
protection axis carries **zero load-bearing behavior in v1** — it is pure
forward-scaffolding + an executable statement of the invariant. That's a
defensible choice, but the design should say it in one sentence instead of
implying the predicate does work. Also: building a `BTreeSet<String>` of pending
keys per `reserve` is nonzero cost for a predicate that always short-circuits —
consider skipping the snapshot until protection has a real consumer.

### F6 — "No schema churn" is true for the interface, not the storage
`EvictionSignals`'s `Option` fields keep the *trait* stable, but every concrete
future policy that needs history (true-LRU touch, LFU count, TTL deadline) needs
a **persisted** column/table = schema churn (the doc concedes a
`cache_object_policy` table in §3). The honest claim is "**interface**-stable,
storage-migrates-when-needed." Make sure the human reads it that way; the
deliverable's "without schema churn" phrasing oversells it.

## Recommendation for the human reviewer

Approve the three-axis decomposition and the decision-only/effects-deferred cut —
those are right. Before implementation, get decisions on:

1. **F1/F2** — coordinator scope: unify both apply paths now, or v1-scope to
   `apply` and defer `open`/recovery? And hook admission to the `missing`
   computation, not `begin_batch`'s arg.
2. **F3** — does admission own dedup/oversize/residency filtering, or the caller?
3. **F4** — eviction policy as materialized comparator (accept scan cost) vs.
   ordering-provider (keep index fast). This is the one with runtime consequences.

F5/F6 are fine to accept as-documented with a one-line honesty edit each.

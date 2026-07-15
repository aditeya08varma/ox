# Concurrency model

oxfs is **blocking thread-per-connection**, not an async server. One acceptor
thread (`nfs/server.rs:72-104`) spawns one OS thread per accepted loopback TCP
connection (`server.rs:91-95`); within a connection, requests are strictly
serial — read record → dispatch → write reply (`server.rs:111-117`). Parallelism
comes only from the client opening multiple connections. There is no async
runtime and `unsafe_code = "forbid"`.

## Why not async (à la `nfsserve`)

The obvious alternative — a tokio server that `spawn`s a task per RPC message
for intra-connection parallelism — buys throughput oxfs doesn't need and forces
the filesystem to be safe under many concurrent same-connection calls, including
its own miss-coalescing. oxfs is read-only and cache-resident: each op is a cheap
local read, so the serial-per-connection model gives up little and keeps
correctness in the core rather than in the protocol adapter.

## The three invariants that make the simple model safe

1. **No fetch on the read path.** NFS `read` opens an already-resident cache file
   or returns `NOENT` (`workspace.rs:425-446`, `cache.rs:677-678`); it never calls
   the backend. Backend fetches happen only in `apply()`.
2. **Lock-free reads via RCU snapshot.** The namespace is `RwLock<Arc<Namespace>>`;
   `snapshot()` clones the `Arc` under a momentary read lock and serves the request
   from the immutable snapshot (`workspace.rs:17,146-151`). Writers build a new
   `Namespace` and swap it in.
3. **All mutation serialized.** `apply()` takes the coarse `reconcile: Mutex<()>`
   first (`workspace.rs:156-159`); the cache catalog sits behind `Mutex<Catalog>`
   (`cache.rs:87`). Fetch fan-out within a batch is capped at 8 workers
   (`cache.rs:464-468`).

Together these make the properties documented elsewhere *corollaries*, not
separate mechanisms: **no thundering herd** (reads don't fetch; `apply` is
serialized; batch admission dedups — no single-flight table needed) and **backend
overload bounded** (≤8 concurrent fetches, one batch at a time).

## What would force a revisit

Any of these breaks an invariant and reopens problems the current model closes:

- **Adding a fetch to the read path** (demand paging, lazy hydration). Two
  concurrent readers of the same absent object would stampede the backend — you'd
  then need an explicit single-flight/coalescing layer, which does not exist today.
- **Making reads mutate shared state** outside the RCU snapshot / atomic telemetry.
- **Unbounding fetch concurrency** (removing the `.min(8)` cap) or letting more than
  one `apply` batch run at once.

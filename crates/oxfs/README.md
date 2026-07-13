# oxFS macOS walking skeleton

This crate is the first macOS-only oxFS vertical slice. It is an unprivileged
Rust NFSv3 service with an EdenFS-shaped core: immutable namespace generations,
stable inodes, a storage-neutral object source, a verified materialization
cache, and a thin operating-system protocol adapter.

The service itself remains rootless. Its mount command asks `sudo` to run only
macOS's built-in `mount_nfs`/`umount` tools at the privilege boundary.

## Semantic boundary

oxFS borrows EdenFS's architecture, not its hydration behavior.

- EdenFS exposes a broad source tree and can fetch content when a file is used.
- oxFS is WYSIWYG. A selected path remains absent until its entire object has
  been fetched, size-checked, SHA-256 checked, fsynced, and atomically renamed.
- NFS `LOOKUP`, `READDIR`, and `READ` only see resident local files. They never
  start or wait for network work.
- The mount is read-only. All NFSv3 mutation procedures return `NFS3ERR_ROFS`.

```text
Manifest generation
       │
       ▼
validate paths ──► fetch immutable objects ──► verify + fsync + rename
                                                    │
                                                    ▼
                                        build namespace off to the side
                                                    │
                                                    ▼
                                            atomic Arc snapshot swap
                                                    │
                                                    ▼
                                         NFSv3 serves local reads only
```

## Package map

| Module | Responsibility |
|---|---|
| `workspace` | Per-Session generations, reconciliation, immutable snapshot publication |
| `namespace` | In-memory inode tree independent of NFS and process connections |
| `inode` | Append-only stable path-to-inode persistence |
| `content` | Tenant-scoped immutable content identity and storage-neutral fetch trait |
| `cache` | Tenant-separated objects, coalesced fetch, verification, crash-safe publication |
| `observations` | Append-only open/read/dismiss JSONL |
| `selections` | Transactional persistence of complete per-Session generations |
| synthetic index | Always-resident `.sageox/INDEX.md` and `.sageox/INDEX.json` with all selectors |
| `nfs` | XDR, ONC RPC record marking, mountd v3, and read-only NFSv3 |

The crate intentionally has no third-party dependencies. This is useful for the
walking skeleton, but not a permanent objection to small permissively licensed
libraries where they reduce risk.

## Mount on macOS

```bash
cargo build -p oxfs
target/debug/oxfsd mount-demo /tmp/oxfs-state /tmp/oxfs-source /tmp/oxfs
cat /tmp/oxfs/hello.txt
target/debug/oxfsd unmount /tmp/oxfs-state
```

The daemon listens only on a dynamic localhost port. `mount_nfs` receives that
same explicit NFS and mountd port, so oxFS does not need `rpcbind`. If mounting
fails, the child server is stopped and its runtime state is removed.

## Run without mounting

```bash
cargo run -p oxfs -- serve-demo /tmp/oxfs-state /tmp/oxfs-source
```

The process prints the selected localhost port and export (`/oxfs`). The demo
publishes `hello.txt` without invoking a mount command.

## Validation

```bash
cargo fmt --all --check
cargo clippy -p oxfs --all-targets -- -D warnings
cargo test -p oxfs
```

The mount-required end-to-end release gate runs on macOS and refuses to prompt
for elevation. Its runner must allow non-interactive `sudo` only for the system
`mount_nfs` and `umount` commands:

```bash
cargo run -p oxfs --bin oxjtest -- gate --artifact oxjtest-results.json
```

`oxjtest` creates a deterministic million-file virtual corpus, materializing
only selected objects from a weighted tiny-file/media size distribution. It
mutates per-Session working sets under concurrent reads through a real NFS
mount, injects a partial fetch, forces LRU eviction, and verifies restart
healing. Correctness, capacity, and
mount cleanup are gating; materialization latency percentiles are recorded in
the JSON artifact but are not gating in E1. Use `--seed`, `--generations`,
`--readers`, `--reads-per-reader`, and `--source-delay-ms` to reproduce or scale
a run. A non-macOS host or unavailable passwordless mount privilege is a hard
failure, not a skipped test.

### Interactive mount (`serve`)

`gate` owns its mount for the duration of one automated run and unmounts on the
way out, so there is no window to inspect the live filesystem by hand. The
`serve` subcommand fills that gap: it generates a working set of manifests,
mounts it at a mountpoint you choose, and evolves it in place until you press
Ctrl-C.

```bash
sudo -v    # cache mount privilege; serve uses non-interactive `sudo -n`
cargo run -p oxfs --bin oxjtest -- serve \
  --mountpoint /tmp/oxjfs \
  --seed 1 \
  --files 1000 \
  --churn 10 \
  --evolve-forever
```

Each generation applies a manifest for a synthetic working set of `--files`
files laid out as `shelf-NN/obj-NNNNNN.bin`, drawn from the same weighted
tiny-file/media size distribution as `gate`. Every `--interval-ms` (default
1000) a `--churn` percentage of the set turns over — that many files are
removed and that many fresh files are added — so the mounted tree visibly
evolves while staying near `--files` in size. Content is immutable
(content-addressed, write-once, matching ox-fs); files are never rewritten in
place, only added and removed. Cache capacity is derived from `--files`
(override with `--cache-bytes`); if a generation exceeds it, the eager
capacity gate reports `stopped > 0` in the stats and hides the overflow.

Each generation emits one stats record. **In an interactive terminal** it is a
human-readable table (the header reprints every 20 rows); **when stdout is
piped** it is a single-line `oxjtest.serve.v1` JSON object instead, so
`| jq` keeps working.

```
  GEN   FILES    BYTES  ADD  RMV EVICT  BLK STOP  FETCH   MAT_MS         CACHE
    1    1000    35.2M 1000    0     0    0    0   1000     84.2   35.2M/256.0M
    2    1000    35.1M  100  100     0    0    0    100     12.7   70.1M/256.0M
    3    1000    35.3M  100  100    73    0    0    100     11.9  105.0M/256.0M
```

| Column   | Meaning |
|----------|---------|
| `GEN`    | Generation number (one manifest applied per row). |
| `FILES`  | Files in the working set right now (held near `--files`). |
| `BYTES`  | Sum of the working set's file sizes. |
| `ADD`    | Files that **entered** this generation. |
| `RMV`    | Files that **left** this generation. |
| `EVICT`  | Cache objects **unlinked** to make room this generation (churned-out content reclaimed once the cache fills). |
| `BLK`    | `evict_blocked`: times a reservation needed space but **every resident object was pinned** by the live namespace or an in-flight fetch, so nothing could be evicted. This is the "could not evict, it's in use" signal — `> 0` means capacity pressure against pinned content. |
| `STOP`   | Entries the eager capacity gate **refused to admit** this generation (they stay invisible). |
| `FETCH`  | Origin fetches this generation (new bytes pulled from the source). |
| `MAT_MS` | Wall-clock milliseconds to fetch + admit this generation's new content. |
| `CACHE`  | Cache used / capacity. |

The JSON form carries the same fields plus `ts_unix`, `applied`, and cumulative
`source_fetches`:

```json
{"schema":"oxjtest.serve.v1","ts_unix":...,"generation":3,"mountpoint":"/tmp/oxjfs",
 "files":1000,"total_bytes":37025792,"added":100,"removed":100,
 "evicted":73,"evict_blocked":0,"stopped":0,"applied":true,"materialize_us":11912,
 "fetched":100,"source_fetches":1200,"cache_capacity":268435456,"cache_remaining":158613504}
```

To see **every** eviction (victim key, size, LRU access, occupancy) rather than
just the per-generation count, set `OXFS_CACHE_LOG=1` — the cache then emits a
`level=INFO action=evict …` line per unlink on stderr. `action=evict_blocked`
WARN lines are always emitted regardless, since they are rare and important. To
force eviction pressure (and watch `EVICT`/`BLK`/`STOP` climb), shrink the cache
with `--cache-bytes`.

Pass `--generations N` to stop after N generations instead of `--evolve-forever`,
and `--source-delay-ms` to simulate a slow origin. From another terminal, watch
the working set evolve:

```bash
find /tmp/oxjfs -type f | wc -l
du -sh /tmp/oxjfs
cat /tmp/oxjfs/.sageox/INDEX.json | jq '.entries | length'
while true; do find /tmp/oxjfs -type f | wc -l; sleep 1; done
```

Ctrl-C (or SIGTERM) unmounts, shuts down the server, and removes the temporary
workspace. Like `gate`, `serve` requires macOS and non-interactive `sudo` for
`mount_nfs`/`umount`.

The integration suite sends real ONC RPC records over TCP. It does not bypass
the wire adapter. Coverage includes mountd `MNT`, hierarchical `LOOKUP`, ranged
`READ`, and a mutation returning `NFS3ERR_ROFS`. Core tests cover unsafe paths,
stale working-set generations, same-size cache corruption after restart, stable
inode reuse, failed verification remaining invisible, and concurrent fetch
coalescing.

## Grounding against `nfsserve` (2024)

After implementing the slice, it was compared with
`/Users/port8080/oss/2026/h1/nfsserve` as requested. The useful validation
findings incorporated here are:

- ONC RPC over TCP must accept records split across multiple record fragments.
- macOS uses `READDIRPLUS`; response size must respect both `dircount` and
  `maxcount`, and truncation must clear `eof`.
- Kernel clients probe `FSINFO`, `FSSTAT`, `PATHCONF`, `ACCESS`, and `READLINK`
  in addition to the obvious lookup/read path.
- Supplying explicit `port` and `mountport` avoids requiring a portmapper.
- Mountd can remain stateless. `UMNT` is idempotent.
- macOS directory enumeration is tolerant of a zero/stable cookie verifier and
  reacts poorly to aggressive `NFS3ERR_BAD_COOKIE` responses.
- Opaque file handles need a server-owned format rather than exposing raw inode
  bytes. oxFS uses a format marker plus its persisted stable inode.

The implementation is not copied from or linked to `nfsserve`; that project is
used only as validation prior art.

## Intentionally incomplete

- No GitLab-LFS HTTP adapter yet; tests and the demo use local sources.
- No byte-limit admission, physical free-space watermark, LRU eviction, or open
  pin accounting yet.
- No RPC authentication policy beyond loopback-only listening and mount auth
  flavor advertisement.
- No graceful connection takeover across daemon restart.

Those omissions are explicit so “NFS server responds” is not confused with the
complete 40-hour oxFS walking skeleton.

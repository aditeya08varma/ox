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

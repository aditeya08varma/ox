//! Adversarial NFSv3 wire tests: correctness of the *read path under a mutating
//! namespace* — the one property oxFS's product depends on and its suite never
//! exercises. Every mature user-mode FS in ~/oss/2026/h1 carries machinery for
//! exactly this and tests it by mutating the tree under a live client:
//!
//!   * nfsserve — `cookieverf`/`cookie3` contract + `transaction_tracker.rs`
//!     (duplicate-request cache); a resumed READDIR is verifier-guarded.
//!   * edenfs   — `integration/{invalidate,readdir,persistence,remove}_test.py`
//!     mutate the mount out from under a live client and assert nothing lies.
//!
//! oxFS-v1 is a read-only server whose *entire product* is "the tree mutates
//! under a live client" (`Workspace::apply`), yet it has zero tests in that
//! intersection. These three encode the CORRECT invariant, so they go RED on
//! the current code and green only when the two defects are fixed:
//!
//!   1. READDIR resume is not cookieverf-guarded and the cookie is a positional
//!      skip  (`nfs/server.rs:322` discards the incoming verifier; `:342`
//!      does `.skip(cookie as usize)` over a per-apply-rebuilt `BTreeMap`).
//!   2. There is no server-side open-handle binding: `read()` re-resolves the inode
//!      to *current* content on every RPC (`nfs/server.rs:300`), so an apply()
//!      mid-read either 404s the handle or splices two objects into one file.
//!
//! Left `#[ignore]` so the shared `cargo test` stays green while the eviction
//! work lands; run them on demand:
//!
//!     cargo test --test nfs_adversarial -- --ignored --nocapture

use oxfs::nfs::{
    Decoder, Encoder, MOUNT_PROGRAM, MOUNT_VERSION, NFS_PROGRAM, NFS_VERSION, NFS3_OK, NfsServer,
    ServerConfig,
};
use oxfs::{ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace};
use std::collections::BTreeMap;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

// NFSv3 status codes the current mod.rs does not (yet) name — RFC 1813.
const NFS3ERR_STALE: u32 = 70;
const NFS3ERR_BAD_COOKIE: u32 = 10022;

// sha256(content) for the fixed corpus these tests publish.
const DIGEST_ABC: &str = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";
const DIGEST_A8: &str = "c34ab6abb7b2bb595bc25c3b388c872fd1d575819a8f55cc689510285e212385"; // "AAAAAAAA"
const DIGEST_B8: &str = "2f858775d71cc4ece5f46f497c58c01167cd6fc301e56e935070f5e81bfe5890"; // "BBBBBBBB"

/// Content source keyed by digest; the tests publish a tiny fixed corpus.
struct MemorySource(BTreeMap<String, Vec<u8>>);
impl ContentSource for MemorySource {
    fn fetch(&self, r: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
        w.write_all(self.0.get(&r.digest).ok_or(FetchError::NotFound)?)?;
        Ok(())
    }
}

fn temp() -> std::path::PathBuf {
    static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    std::env::temp_dir().join(format!(
        "oxfs-adv-{}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

fn content(tenant: &str, digest: &str, size: u64) -> ContentRef {
    ContentRef::new(tenant, "sha256", digest, size).unwrap()
}

fn entry(path: &str, c: ContentRef) -> ManifestEntry {
    ManifestEntry::new(path, "id", "Session", 0o444, 0, c, "adversarial").unwrap()
}

// --- ONC RPC / NFSv3 wire helpers (self-contained; no shared test module) ---

/// One synchronous RPC round trip; returns the reply body after the accepted
/// RPC header, exactly like a real client on a single mount TCP connection.
fn call(
    stream: &mut TcpStream,
    xid: u32,
    program: u32,
    version: u32,
    procedure: u32,
    args: Vec<u8>,
) -> Vec<u8> {
    let mut e = Encoder::new();
    e.u32(xid);
    e.u32(0); // CALL
    e.u32(2); // RPC version 2
    e.u32(program);
    e.u32(version);
    e.u32(procedure);
    e.u32(0);
    e.opaque(&[]); // cred: AUTH_NONE
    e.u32(0);
    e.opaque(&[]); // verf: AUTH_NONE
    let mut request = e.into_bytes();
    request.extend(args);
    let marker = (request.len() as u32) | 0x8000_0000;
    stream.write_all(&marker.to_be_bytes()).unwrap();
    stream.write_all(&request).unwrap();
    let mut h = [0; 4];
    stream.read_exact(&mut h).unwrap();
    let len = (u32::from_be_bytes(h) & 0x7fff_ffff) as usize;
    let mut reply = vec![0; len];
    stream.read_exact(&mut reply).unwrap();
    let mut d = Decoder::new(&reply);
    assert_eq!(d.u32().unwrap(), xid, "reply xid");
    assert_eq!(d.u32().unwrap(), 1, "REPLY");
    assert_eq!(d.u32().unwrap(), 0, "MSG_ACCEPTED");
    assert_eq!(d.u32().unwrap(), 0, "verf flavor");
    assert!(d.opaque(400).unwrap().is_empty(), "verf body");
    assert_eq!(d.u32().unwrap(), 0, "accept SUCCESS");
    reply[reply.len() - d.remaining()..].to_vec()
}

fn mount(stream: &mut TcpStream) -> Vec<u8> {
    let mut a = Encoder::new();
    a.string("/oxfs");
    let body = call(stream, 1, MOUNT_PROGRAM, MOUNT_VERSION, 1, a.into_bytes());
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), 0, "MNT3_OK");
    d.opaque(64).unwrap().to_vec()
}

/// LOOKUP returning the child file handle (asserts success).
fn lookup(stream: &mut TcpStream, xid: u32, parent: &[u8], name: &str) -> Vec<u8> {
    let mut a = Encoder::new();
    a.opaque(parent);
    a.string(name);
    let body = call(stream, xid, NFS_PROGRAM, NFS_VERSION, 3, a.into_bytes());
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), NFS3_OK, "LOOKUP status for {name}");
    d.opaque(64).unwrap().to_vec()
}

/// READ; returns (nfs_status, Some(data) on success else None).
fn read(
    stream: &mut TcpStream,
    xid: u32,
    handle: &[u8],
    offset: u64,
    count: u32,
) -> (u32, Option<Vec<u8>>) {
    let mut a = Encoder::new();
    a.opaque(handle);
    a.u64(offset);
    a.u32(count);
    let body = call(stream, xid, NFS_PROGRAM, NFS_VERSION, 6, a.into_bytes());
    let mut d = Decoder::new(&body);
    let status = d.u32().unwrap();
    if status != NFS3_OK {
        return (status, None);
    }
    assert!(d.boolean().unwrap(), "READ post-op attr follows");
    skip_attr(&mut d);
    let _count = d.u32().unwrap();
    let _eof = d.boolean().unwrap();
    let data = d.opaque(1 << 20).unwrap().to_vec();
    (status, Some(data))
}

struct ReadDir {
    status: u32,
    verifier: [u8; 8],
    entries: Vec<(String, u64)>, // (name, cookie)
    eof: bool,
}

/// READDIR (proc 16). `dircount` is sized by callers to force one entry/page.
fn readdir(
    stream: &mut TcpStream,
    xid: u32,
    dir: &[u8],
    cookie: u64,
    verifier: [u8; 8],
    dircount: u32,
) -> ReadDir {
    let mut a = Encoder::new();
    a.opaque(dir);
    a.u64(cookie);
    a.fixed(&verifier);
    a.u32(dircount);
    let body = call(stream, xid, NFS_PROGRAM, NFS_VERSION, 16, a.into_bytes());
    let mut d = Decoder::new(&body);
    let status = d.u32().unwrap();
    if status != NFS3_OK {
        return ReadDir {
            status,
            verifier: [0; 8],
            entries: vec![],
            eof: true,
        };
    }
    assert!(d.boolean().unwrap(), "READDIR dir attr follows");
    skip_attr(&mut d);
    let mut verifier_out = [0u8; 8];
    verifier_out.copy_from_slice(d.fixed(8).unwrap());
    let mut entries = Vec::new();
    while d.boolean().unwrap() {
        let _fileid = d.u64().unwrap();
        let name = d.string(255).unwrap().to_owned();
        let cookie = d.u64().unwrap();
        entries.push((name, cookie));
    }
    let eof = d.boolean().unwrap();
    ReadDir {
        status,
        verifier: verifier_out,
        entries,
        eof,
    }
}

fn skip_attr(d: &mut Decoder<'_>) {
    // fattr3: type,mode,nlink,uid,gid (5×u32), size,used (2×u64),
    // rdev (2×u32), fsid,fileid (2×u64), atime/mtime/ctime (3×(u32,u32)).
    for _ in 0..5 {
        let _ = d.u32();
    }
    let _ = d.u64();
    let _ = d.u64();
    let _ = d.u32();
    let _ = d.u32();
    let _ = d.u64();
    let _ = d.u64();
    for _ in 0..3 {
        let _ = d.u32();
        let _ = d.u32();
    }
}

fn boot(ws: Arc<Workspace>) -> (TcpStream, oxfs::nfs::ServerHandle) {
    let server = NfsServer::new(ws, ServerConfig::default()).spawn().unwrap();
    let stream = TcpStream::connect(server.address()).unwrap();
    (stream, server)
}

// ---------------------------------------------------------------------------
// Avenue 1 — READDIR resume across an apply() silently drops and duplicates
// entries because the resume is not cookieverf-guarded.
//
// gen1 dir "d" = [b, z]; a small dircount forces one entry per page.
//   page1(cookie 0) -> ["b"], eof=false           (client has seen {b})
// apply gen2: dir "d" = [a, b, z]  (BTreeMap sorts a<b<z; "a" lands BEFORE cursor)
//   page2(cookie 1) -> `.skip(1)` over [a,b,z] -> ["b"] again
// Full enumeration = [b, b]: "a" is never returned, "b" is duplicated, and the
// server returns NFS3_OK on the stale cookie instead of NFS3ERR_BAD_COOKIE.
// A correct server (nfsserve, edenfs) rejects the stale verifier so the client
// restarts from cookie 0.
// ---------------------------------------------------------------------------
#[test]
#[ignore = "KNOWN BUG: READDIR resume not cookieverf-guarded (nfs/server.rs:322,:342) — drops/duplicates entries across apply()"]
fn readdir_resume_across_apply_drops_and_duplicates_entries() {
    let root = temp();
    let src = Arc::new(MemorySource(BTreeMap::from([(
        DIGEST_ABC.into(),
        b"abc".to_vec(),
    )])));
    let ws = Workspace::open(&root, src).unwrap();
    let abc = || content("t", DIGEST_ABC, 3);

    // gen1: d = [b, z]
    ws.apply(Manifest {
        session_id: "s".into(),
        generation: 1,
        entries: vec![entry("d/b", abc()), entry("d/z", abc())],
    })
    .unwrap();

    let (mut stream, server) = boot(ws.clone());
    let root_h = mount(&mut stream);
    let dir_h = lookup(&mut stream, 2, &root_h, "d");

    // Small dircount => exactly one entry per page (single-char names).
    const DIRCOUNT: u32 = 180;
    let page1 = readdir(&mut stream, 3, &dir_h, 0, [0; 8], DIRCOUNT);
    assert_eq!(page1.status, NFS3_OK);
    assert!(!page1.eof, "gen1 dir has 2 entries; page 1 must not be EOF");
    let resume_cookie = page1.entries.last().expect("page1 has one entry").1;

    // The tree mutates under the mid-enumeration client — the product's steady
    // state (Insight murmurs a new working set). "a" sorts before the cursor.
    ws.apply(Manifest {
        session_id: "s".into(),
        generation: 2,
        entries: vec![
            entry("d/a", abc()),
            entry("d/b", abc()),
            entry("d/z", abc()),
        ],
    })
    .unwrap();

    // Real client resumes with the last cookie and the verifier it was handed.
    let page2 = readdir(
        &mut stream,
        4,
        &dir_h,
        resume_cookie,
        page1.verifier,
        DIRCOUNT,
    );

    let mut seen: Vec<String> = page1.entries.iter().map(|(n, _)| n.clone()).collect();
    seen.extend(page2.entries.iter().map(|(n, _)| n.clone()));
    let b_count = seen.iter().filter(|n| *n == "b").count();
    let saw_a = seen.iter().any(|n| n == "a");

    // Correct: either reject the stale cookie, or produce a drop-free,
    // duplicate-free enumeration of the still-present entries.
    let correct = page2.status == NFS3ERR_BAD_COOKIE || (b_count == 1 && saw_a);
    assert!(
        correct,
        "READDIR resumed across apply(): status={} seen={:?} (b×{b_count}, saw_a={saw_a}). \
         Positional cookie + discarded verifier => 'a' dropped, 'b' duplicated. \
         nfsserve/edenfs guard this with cookieverf.",
        page2.status, seen
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).ok();
}

// ---------------------------------------------------------------------------
// Avenue 2a — a handle whose path was dropped by an apply() returns NFS3ERR_NOENT
// from READ instead of NFS3ERR_STALE. NOENT is not a legal READ error for a
// well-formed handle to a vanished object (RFC 1813); macOS treats STALE as
// "reopen transparently" but NOENT as a hard mid-`cat` failure.
// ---------------------------------------------------------------------------
#[test]
#[ignore = "KNOWN BUG: READ on a dropped inode returns NOENT, not STALE (nfs/server.rs:300-302)"]
fn open_handle_to_dropped_path_returns_stale_not_noent() {
    let root = temp();
    let src = Arc::new(MemorySource(BTreeMap::from([(
        DIGEST_A8.into(),
        b"AAAAAAAA".to_vec(),
    )])));
    let ws = Workspace::open(&root, src).unwrap();
    let a8 = || content("t", DIGEST_A8, 8);

    ws.apply(Manifest {
        session_id: "s".into(),
        generation: 1,
        entries: vec![entry("f", a8())],
    })
    .unwrap();

    let (mut stream, server) = boot(ws.clone());
    let root_h = mount(&mut stream);
    let handle = lookup(&mut stream, 2, &root_h, "f");

    let (status, data) = read(&mut stream, 3, &handle, 0, 8);
    assert_eq!(status, NFS3_OK);
    assert_eq!(data.as_deref(), Some(&b"AAAAAAAA"[..]));

    // apply() drops "f" (session now selects a different path).
    ws.apply(Manifest {
        session_id: "s".into(),
        generation: 2,
        entries: vec![entry("other", a8())],
    })
    .unwrap();

    // Same handle, second read: the object is gone.
    let (status, _) = read(&mut stream, 4, &handle, 0, 8);
    assert_eq!(
        status, NFS3ERR_STALE,
        "READ on a dropped handle returned {status} (NOENT=2). A vanished object must be \
         NFS3ERR_STALE(70) so the client reopens; NOENT breaks `cat` mid-stream."
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).ok();
}

// ---------------------------------------------------------------------------
// Avenue 2b — the scary one. A handle read across a same-path content swap
// splices two different objects into one logical file, silently. There is no
// server-side open binding: `read()` re-resolves the inode to *current* content on
// every RPC (nfs/server.rs:300). The design doc promises "existing handles keep
// reading their bound generation"; this proves that invariant is false.
//
//   read(H, 0..4) from A -> "AAAA"
//   apply(): same path "p" now -> object B
//   read(H, 4..8)         -> "BBBB"    (should be A's "AAAA", or STALE)
//   client's file = "AAAABBBB": A's head spliced onto B's tail.
// ---------------------------------------------------------------------------
#[test]
#[ignore = "KNOWN BUG: no open-handle binding — READ across a same-path content swap splices two objects (nfs/server.rs:300)"]
fn open_handle_across_same_path_content_swap_splices_two_objects() {
    let root = temp();
    let src = Arc::new(MemorySource(BTreeMap::from([
        (DIGEST_A8.into(), b"AAAAAAAA".to_vec()),
        (DIGEST_B8.into(), b"BBBBBBBB".to_vec()),
    ])));
    let ws = Workspace::open(&root, src).unwrap();

    // gen1: path "p" -> A
    ws.apply(Manifest {
        session_id: "s".into(),
        generation: 1,
        entries: vec![entry("p", content("t", DIGEST_A8, 8))],
    })
    .unwrap();

    let (mut stream, server) = boot(ws.clone());
    let root_h = mount(&mut stream);
    let handle = lookup(&mut stream, 2, &root_h, "p");

    let (s1, first) = read(&mut stream, 3, &handle, 0, 4);
    assert_eq!(s1, NFS3_OK);
    assert_eq!(
        first.as_deref(),
        Some(&b"AAAA"[..]),
        "first half is from object A"
    );

    // Same path "p" is repointed to a different object mid-read.
    ws.apply(Manifest {
        session_id: "s".into(),
        generation: 2,
        entries: vec![entry("p", content("t", DIGEST_B8, 8))],
    })
    .unwrap();

    let (s2, second) = read(&mut stream, 4, &handle, 4, 4);

    // Correct: the two halves come from the SAME object (generation binding held), or the
    // second read fails STALE so the client reopens. A silent "BBBB" is a
    // cross-object splice presented as one file.
    let bound = s2 == NFS3_OK && second.as_deref() == Some(&b"AAAA"[..]);
    let reopened = s2 == NFS3ERR_STALE;
    assert!(
        bound || reopened,
        "READ across a same-path content swap: status={s2} second_half={:?}. \
         Got a splice of A's head + B's tail in one file — the 'handles keep reading their \
         bound generation' invariant is false; nothing retains the inode->content binding.",
        second.as_deref().map(String::from_utf8_lossy)
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).ok();
}

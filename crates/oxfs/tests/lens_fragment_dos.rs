//! LENS-6: `read_record` (src/nfs/rpc.rs) caps assembled *bytes* but not the
//! number of fragments. A zero-length, non-last fragment (record marker
//! `0x0000_0000` — high bit clear = not last, low 31 bits = 0 length) adds 0
//! assembled bytes, so `output.len().saturating_add(0) > max` is never true and
//! the loop reads marker after marker without ever completing a record or
//! tripping `max_record_bytes`. A stream of such fragments pins the per-connection
//! thread indefinitely: the `max_record_bytes` guard is fully bypassed.
//!
//! Contract under test: a server that advertises a per-record resource cap
//! (`max_record_bytes`, default 1 MiB) MUST bound the wire input a single record
//! assembly can consume. Zero-length fragments still cost 4 wire bytes each, so a
//! flood of them must eventually be rejected / the connection closed. Nothing in
//! RFC 1831 record marking requires a server to accept an unbounded number of
//! empty fragments; a robust implementation caps them.
//!
//! This test floods one connection with zero-length non-last fragments and asserts
//! the server bounds the abuse (closes the connection / stops consuming). It is
//! hermetic and self-terminating: the flood is capped at a finite marker count and
//! runs on a worker thread joined with a hard timeout, so a hanging server surfaces
//! as a bounded-timeout failure rather than hanging the test process.

use oxfs::nfs::{NfsServer, ServerConfig};
use oxfs::{ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace};
use std::collections::BTreeMap;
use std::io::Write;
use std::net::TcpStream;
use std::sync::Arc;
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

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
        "oxfs-fragdos-{}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

fn setup() -> (std::path::PathBuf, oxfs::nfs::ServerHandle) {
    let root = temp();
    let digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";
    let source = Arc::new(MemorySource(BTreeMap::from([(
        digest.into(),
        b"abc".to_vec(),
    )])));
    let ws = Workspace::open(&root, source).unwrap();
    let r = ContentRef::new("tenant", "sha256", digest, 3).unwrap();
    ws.apply(Manifest {
        session_id: "s".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new("dir/file.txt", "id", "Session", 0o444, 42, r, "test").unwrap(),
        ],
    })
    .unwrap();
    let handle = NfsServer::new(ws, ServerConfig::default()).spawn().unwrap();
    (root, handle)
}

/// Failure prevented: a single connection sending only zero-length non-last RPC
/// fragments loops forever inside `read_record`, pinning its thread. Many such
/// connections exhaust the server's threads — a cheap remote DoS — because the
/// `max_record_bytes` cap is defeated by fragments that assemble 0 bytes.
#[test]
#[ignore = "KNOWN BUG: read_record has no fragment-count bound; zero-length non-last fragments loop forever (nfs/rpc.rs read_record)"]
fn zero_length_fragment_flood_is_bounded() {
    let (root, server) = setup();
    let addr = server.address();

    // A well-behaved server bounds the wire input one record may consume.
    // Default `max_record_bytes` is 1 MiB; each zero-length marker costs 4 wire
    // bytes, so a byte-counting cap would reject after ~256K markers, and a
    // fragment-count cap sooner. 4M markers (16 MiB of pure record markers) is far
    // past any sane bound — if the server is still happily consuming them, it has
    // no bound at all.
    const LIMIT_MARKERS: usize = 4_000_000;
    // length 0, high bit clear => "not last" fragment.
    const EMPTY_NON_LAST_MARKER: [u8; 4] = [0, 0, 0, 0];
    // Batch markers to keep syscall count reasonable (64 KiB per write).
    const BATCH: usize = 16_384;

    let (tx, rx) = mpsc::channel::<bool>();
    let worker = thread::spawn(move || {
        let mut stream = TcpStream::connect(addr).unwrap();
        // A blocked write (server stopped consuming but hasn't RST yet) must not
        // wedge the worker forever — surface it as an error just like a close.
        stream
            .set_write_timeout(Some(Duration::from_secs(5)))
            .unwrap();

        let batch: Vec<u8> = EMPTY_NON_LAST_MARKER
            .iter()
            .copied()
            .cycle()
            .take(BATCH * 4)
            .collect();

        let mut bounded = false;
        let mut sent = 0usize;
        while sent < LIMIT_MARKERS {
            if stream.write_all(&batch).is_err() {
                // Server closed the connection or stopped consuming the abusive
                // stream: the fix is in place.
                bounded = true;
                break;
            }
            sent += BATCH;
        }
        let _ = tx.send(bounded);
    });

    match rx.recv_timeout(Duration::from_secs(30)) {
        Ok(bounded) => {
            let _ = worker.join();
            assert!(
                bounded,
                "server consumed {LIMIT_MARKERS} zero-length non-last fragments (16 MiB of \
                 record markers) without ever closing the connection: read_record has no \
                 fragment-count / fragment-byte bound, so an empty-fragment flood loops \
                 forever and pins the connection thread (max_record_bytes is bypassed \
                 because 0-length fragments never grow the assembled record)"
            );
        }
        Err(_) => {
            // Worker still writing after 30s: the server is consuming markers as
            // fast as we send them and never rejecting — exactly the unbounded loop
            // the lens predicts. Detach the worker; dropping the stream lets the
            // pinned server thread unwind on EOF.
            panic!(
                "flooding {LIMIT_MARKERS} zero-length fragments neither completed nor was \
                 rejected within 30s: read_record keeps consuming empty non-last fragments \
                 with no bound, pinning the connection thread"
            );
        }
    }

    server.shutdown().unwrap();
    let _ = std::fs::remove_dir_all(root);
}

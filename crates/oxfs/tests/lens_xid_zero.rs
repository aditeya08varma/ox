//! LENS-4: On a decodable-header-but-failing RPC call, serve_connection replies
//! with a hard-coded xid=0 instead of the xid it already parsed.
//!
//! RFC 5531 (ONC RPC): the transaction identifier (xid) in a reply MUST equal
//! the xid of the call it answers — this is how a client matches replies to
//! outstanding calls. A GARBAGE_ARGS reply is still a reply to *that* call and
//! must carry its xid. Because `decode_call` parses the xid BEFORE it validates
//! the auth cred, a call whose header decodes but whose cred opaque length is
//! bogus produces an error AFTER the real xid is known. The server throws that
//! xid away and answers with 0.

use oxfs::nfs::{Decoder, Encoder, NFS_PROGRAM, NFS_VERSION, NfsServer, ServerConfig};
use oxfs::{ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace};
use std::collections::BTreeMap;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

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
        "oxfs-lens-xid-{}-{}-{}",
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

/// Frame one ONC-RPC record and return the raw reply bytes (after the record mark).
fn send_raw(stream: &mut TcpStream, request: &[u8]) -> Vec<u8> {
    let marker = (request.len() as u32) | 0x8000_0000;
    stream.write_all(&marker.to_be_bytes()).unwrap();
    stream.write_all(request).unwrap();
    let mut h = [0u8; 4];
    stream.read_exact(&mut h).unwrap();
    let len = (u32::from_be_bytes(h) & 0x7fff_ffff) as usize;
    let mut reply = vec![0u8; len];
    stream.read_exact(&mut reply).unwrap();
    reply
}

/// A well-formed RPC call header whose FIRST auth cred (the client credential)
/// declares an opaque body length far larger than `skip_auth`'s 400-byte cap.
/// `decode_call` parses `xid` successfully, then fails in `skip_auth` — the
/// exact "decodable header but failing call" the lens describes.
#[test]
#[ignore = "KNOWN BUG: GARBAGE_ARGS reply hard-codes xid=0, breaking reply-to-call correlation (nfs/server.rs serve_connection decode error arm)"]
fn garbage_args_reply_preserves_the_call_xid() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();

    const XID: u32 = 0xDEAD_BEEF;

    let mut e = Encoder::new();
    e.u32(XID); // xid — parsed before the failure
    e.u32(0); // mtype = CALL
    e.u32(2); // rpcvers = 2
    e.u32(NFS_PROGRAM);
    e.u32(NFS_VERSION);
    e.u32(0); // procedure = NULL
    // First auth cred: flavor + opaque<>. Declare a length (500) that exceeds the
    // 400-byte cap in skip_auth so opaque() returns LengthOverflow.
    e.u32(0); // AUTH_NONE flavor
    e.u32(500); // credential body length — bogus, > 400
    // No actual credential bytes follow; the length check fails before reading.
    let request = e.into_bytes();

    let reply = send_raw(&mut stream, &request);

    // Parse the RPC reply header directly.
    let mut d = Decoder::new(&reply);
    let reply_xid = d.u32().unwrap();
    assert_eq!(d.u32().unwrap(), 1, "mtype must be REPLY");
    assert_eq!(d.u32().unwrap(), 0, "reply_stat must be MSG_ACCEPTED");
    assert_eq!(d.u32().unwrap(), 0, "verf flavor");
    assert!(d.opaque(400).unwrap().is_empty(), "verf body");
    assert_eq!(d.u32().unwrap(), 4, "accept_stat must be GARBAGE_ARGS");

    assert_eq!(
        reply_xid, XID,
        "GARBAGE_ARGS reply must echo the call's xid (RFC 5531); got {reply_xid:#x}"
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

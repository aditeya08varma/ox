// LENS-1: A LOOKUP whose filename is non-UTF-8 or longer than 255 bytes must be
// answered at the NFS layer (NFS3ERR_NOENT / NFS3ERR_NAMETOOLONG), not with an
// RPC-layer GARBAGE_ARGS. RFC 1813 filename3 is OPAQUE bytes; a client stat'ing
// a Latin-1 or over-long name expects ENOENT / ENAMETOOLONG, never EIO.
//
// server.rs lookup() does `let name = d.string(255)?`, and Decoder::string runs
// from_utf8 + a 255 length cap. Either error propagates to dispatch(), which
// replies rpc::accepted(xid, GARBAGE_ARGS). These tests assert the NFS status
// instead, so they FAIL today (accept_stat == GARBAGE_ARGS) and would PASS once
// lookup treats the name as opaque bytes.

use oxfs::nfs::{
    Decoder, Encoder, MOUNT_PROGRAM, MOUNT_VERSION, NFS_PROGRAM, NFS_VERSION, NFS3_OK,
    NFS3ERR_NAMETOOLONG, NFS3ERR_NOENT, NfsServer, ServerConfig,
};
use oxfs::{ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace};
use std::collections::BTreeMap;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

// ONC RPC accept_stat
const GARBAGE_ARGS: u32 = 4;

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
        "oxfs-lens-nonutf8-{}-{}-{}",
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

/// Frames one ONC-RPC record and returns (accept_stat, proc_result_bytes).
/// Unlike the nfs_wire.rs `call` helper, this does NOT assert accept_stat == 0,
/// so it can observe an RPC-layer rejection (GARBAGE_ARGS) directly.
fn call_raw(
    stream: &mut TcpStream,
    xid: u32,
    program: u32,
    version: u32,
    procedure: u32,
    args: Vec<u8>,
) -> (u32, Vec<u8>) {
    let mut e = Encoder::new();
    e.u32(xid);
    e.u32(0); // mtype = CALL
    e.u32(2); // rpcvers
    e.u32(program);
    e.u32(version);
    e.u32(procedure);
    e.u32(0);
    e.opaque(&[]);
    e.u32(0);
    e.opaque(&[]);
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
    assert_eq!(d.u32().unwrap(), xid);
    assert_eq!(d.u32().unwrap(), 1, "mtype REPLY");
    assert_eq!(d.u32().unwrap(), 0, "reply_stat MSG_ACCEPTED");
    assert_eq!(d.u32().unwrap(), 0, "verf flavor");
    assert!(d.opaque(400).unwrap().is_empty(), "verf body");
    let accept_stat = d.u32().unwrap();
    let body = reply[reply.len() - d.remaining()..].to_vec();
    (accept_stat, body)
}

fn mount(stream: &mut TcpStream) -> Vec<u8> {
    let mut a = Encoder::new();
    a.string("/oxfs");
    let (accept_stat, body) = call_raw(stream, 1, MOUNT_PROGRAM, MOUNT_VERSION, 1, a.into_bytes());
    assert_eq!(accept_stat, 0);
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), 0);
    d.opaque(64).unwrap().to_vec()
}

/// Issues a LOOKUP with a raw (possibly non-UTF-8 / over-long) opaque name and
/// returns (rpc_accept_stat, nfs_status).
fn lookup_raw(stream: &mut TcpStream, xid: u32, parent: &[u8], raw_name: &[u8]) -> (u32, u32) {
    let mut a = Encoder::new();
    a.opaque(parent);
    a.opaque(raw_name); // filename3 = opaque<> : u32 len + bytes + XDR pad
    let (accept_stat, body) = call_raw(stream, xid, NFS_PROGRAM, NFS_VERSION, 3, a.into_bytes());
    let nfs_status = if accept_stat == 0 {
        Decoder::new(&body).u32().unwrap()
    } else {
        u32::MAX // no NFS body on an RPC-layer rejection
    };
    (accept_stat, nfs_status)
}

#[test]
fn lookup_of_non_utf8_name_returns_noent_not_garbage_args() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);

    // A Latin-1 / binary name a real macOS or Linux client can legitimately stat.
    let (accept_stat, nfs_status) = lookup_raw(&mut stream, 20, &root_handle, &[0xff, 0xfe]);

    assert_ne!(
        accept_stat, GARBAGE_ARGS,
        "non-UTF-8 LOOKUP name rejected at RPC layer (GARBAGE_ARGS -> client sees EIO); \
         RFC 1813 filename3 is opaque bytes and must be answered at the NFS layer"
    );
    assert_eq!(accept_stat, 0, "expected an accepted NFS reply");
    assert_eq!(
        nfs_status, NFS3ERR_NOENT,
        "a non-existent binary name must map to NFS3ERR_NOENT (ENOENT)"
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn lookup_of_overlong_name_returns_nametoolong_not_garbage_args() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);

    // 300 bytes > 255: a client stat of a too-long name must get ENAMETOOLONG.
    let overlong = vec![b'a'; 300];
    let (accept_stat, nfs_status) = lookup_raw(&mut stream, 21, &root_handle, &overlong);

    assert_ne!(
        accept_stat, GARBAGE_ARGS,
        "over-length LOOKUP name rejected at RPC layer (GARBAGE_ARGS -> client sees EIO); \
         RFC 1813 requires NFS3ERR_NAMETOOLONG (ENAMETOOLONG)"
    );
    assert_eq!(accept_stat, 0, "expected an accepted NFS reply");
    assert!(
        nfs_status == NFS3ERR_NAMETOOLONG || nfs_status == NFS3ERR_NOENT,
        "over-long name must map to NFS3ERR_NAMETOOLONG (or at least NOENT), got {nfs_status}"
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

// Sanity: prove the harness observes a normal accepted NFS reply for a valid
// (UTF-8, non-existent) name, so a GARBAGE_ARGS above is truly the name-encoding
// path and not a harness framing bug.
#[test]
fn lookup_of_valid_missing_name_is_accepted_noent() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);

    let (accept_stat, nfs_status) = lookup_raw(&mut stream, 22, &root_handle, b"missing");
    assert_eq!(accept_stat, 0);
    assert_eq!(nfs_status, NFS3ERR_NOENT);
    let _ = NFS3_OK;

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

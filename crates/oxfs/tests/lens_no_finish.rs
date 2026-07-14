// LENS-8: NFS argument decoders never call d.finish(); only MNT validates
// end-of-args. A GETATTR carrying a valid file handle followed by trailing
// garbage bytes is decoded as well-formed and answered with SUCCESS, when
// RFC 1813 / ONC-RPC (RFC 5531 §9) require GARBAGE_ARGS for a request whose
// argument buffer does not deserialize exactly.
//
// This test sends a GETATTR whose args are a valid root handle + 8 trailing
// bytes and asserts the RPC accept_stat is GARBAGE_ARGS(4). It FAILS today
// (server returns SUCCESS(0)) and PASSES once every NFS arg decoder calls
// d.finish() the way dispatch_mount already does.

use oxfs::nfs::{
    Decoder, Encoder, MOUNT_PROGRAM, MOUNT_VERSION, NFS_PROGRAM, NFS_VERSION, NfsServer,
    ServerConfig,
};
use oxfs::{ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace};
use std::collections::BTreeMap;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

const GETATTR: u32 = 1;
const RPC_SUCCESS: u32 = 0;
const RPC_GARBAGE_ARGS: u32 = 4;

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
        "oxfs-lens-finish-{}-{}-{}",
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

/// Frame one ONC-RPC record and return (accept_stat, proc_body).
fn call_accept_stat(
    stream: &mut TcpStream,
    xid: u32,
    program: u32,
    version: u32,
    procedure: u32,
    args: Vec<u8>,
) -> (u32, Vec<u8>) {
    let mut e = Encoder::new();
    e.u32(xid);
    e.u32(0); // CALL
    e.u32(2); // RPC version
    e.u32(program);
    e.u32(version);
    e.u32(procedure);
    e.u32(0);
    e.opaque(&[]); // cred
    e.u32(0);
    e.opaque(&[]); // verf
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
    assert_eq!(d.u32().unwrap(), 1); // REPLY
    assert_eq!(d.u32().unwrap(), 0); // MSG_ACCEPTED
    assert_eq!(d.u32().unwrap(), 0); // verf flavor
    assert!(d.opaque(400).unwrap().is_empty()); // verf body
    let accept_stat = d.u32().unwrap();
    let body = reply[reply.len() - d.remaining()..].to_vec();
    (accept_stat, body)
}

fn mount(stream: &mut TcpStream) -> Vec<u8> {
    let mut a = Encoder::new();
    a.string("/oxfs");
    let (stat, body) = call_accept_stat(stream, 1, MOUNT_PROGRAM, MOUNT_VERSION, 1, a.into_bytes());
    assert_eq!(stat, RPC_SUCCESS);
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), 0); // mount status OK
    d.opaque(64).unwrap().to_vec()
}

#[test]
#[ignore = "KNOWN BUG: NFS arg decoders skip d.finish(); trailing garbage accepted as SUCCESS (nfs/server.rs)"]
fn getattr_with_trailing_garbage_is_rejected_as_garbage_args() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);

    // Sanity: a clean GETATTR (handle only, nothing trailing) succeeds.
    let mut clean = Encoder::new();
    clean.opaque(&root_handle);
    let (clean_stat, clean_body) = call_accept_stat(
        &mut stream,
        2,
        NFS_PROGRAM,
        NFS_VERSION,
        GETATTR,
        clean.into_bytes(),
    );
    assert_eq!(
        clean_stat, RPC_SUCCESS,
        "well-formed GETATTR must be accepted"
    );
    assert_eq!(
        Decoder::new(&clean_body).u32().unwrap(),
        0,
        "well-formed GETATTR must report NFS3_OK"
    );

    // Now the same valid handle, but with 8 trailing garbage bytes appended
    // AFTER the opaque handle. The argument buffer no longer deserializes
    // exactly to a GETATTR3args, so ONC-RPC requires GARBAGE_ARGS.
    let mut e = Encoder::new();
    e.opaque(&root_handle); // valid GETATTR3args.object
    e.fixed(&[0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04]); // trailing garbage
    let (stat, _body) = call_accept_stat(
        &mut stream,
        3,
        NFS_PROGRAM,
        NFS_VERSION,
        GETATTR,
        e.into_bytes(),
    );

    assert_eq!(
        stat, RPC_GARBAGE_ARGS,
        "GETATTR with trailing garbage after the handle must be rejected as \
         GARBAGE_ARGS(4); server accepted it as accept_stat={stat} (SUCCESS=0)"
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

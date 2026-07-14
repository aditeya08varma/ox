// LENS-2: READDIR/READDIRPLUS with a tiny dircount/maxcount must not return
// ZERO entries with eof=false and no NFS3ERR_TOOSMALL. RFC 1813 requires that a
// server which cannot fit even the first entry into the requested count return
// NFS3ERR_TOOSMALL; returning an empty, non-eof reply makes a conforming client
// (which re-issues the SAME cookie=0) livelock forever.
//
// Helpers below are copied from tests/nfs_wire.rs (this is a fresh test binary).

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

// RFC 1813 status: server cannot fit even one entry into the requested count.
const NFS3ERR_TOOSMALL: u32 = 10005;
const READDIR: u32 = 16;
const READDIRPLUS: u32 = 17;

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
        "oxfs-lens-readdir-{}-{}-{}",
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
    e.u32(0);
    e.u32(2);
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
    assert_eq!(d.u32().unwrap(), 1);
    assert_eq!(d.u32().unwrap(), 0);
    assert_eq!(d.u32().unwrap(), 0);
    assert!(d.opaque(400).unwrap().is_empty());
    assert_eq!(d.u32().unwrap(), 0);
    reply[reply.len() - d.remaining()..].to_vec()
}

fn mount(stream: &mut TcpStream) -> Vec<u8> {
    let mut a = Encoder::new();
    a.string("/oxfs");
    let body = call(stream, 1, MOUNT_PROGRAM, MOUNT_VERSION, 1, a.into_bytes());
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), 0);
    d.opaque(64).unwrap().to_vec()
}

fn skip_attr(d: &mut Decoder<'_>) {
    let _ = d.u32();
    let _ = d.u32();
    let _ = d.u32();
    let _ = d.u32();
    let _ = d.u32();
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

/// Returns (status, entry_count, eof) from a READDIR3res / READDIRPLUS3res body.
fn parse_readdir(body: &[u8], plus: bool) -> (u32, usize, bool) {
    let mut d = Decoder::new(body);
    let status = d.u32().unwrap();
    if status != NFS3_OK {
        return (status, 0, false);
    }
    // dir_attributes: post_op_attr
    if d.boolean().unwrap() {
        skip_attr(&mut d);
    }
    // cookieverf: fixed 8 bytes
    let _ = d.fixed(8).unwrap();
    // dirlist: sequence of entries terminated by value_follows == false
    let mut count = 0usize;
    while d.boolean().unwrap() {
        let _fileid = d.u64().unwrap();
        let _name = d.string(1024).unwrap();
        let _cookie = d.u64().unwrap();
        if plus {
            // name_attributes: post_op_attr
            if d.boolean().unwrap() {
                skip_attr(&mut d);
            }
            // name_handle: post_op_fh3
            if d.boolean().unwrap() {
                let _ = d.opaque(64).unwrap();
            }
        }
        count += 1;
    }
    let eof = d.boolean().unwrap();
    (status, count, eof)
}

#[test]
#[ignore = "KNOWN BUG: tiny dircount/maxcount READDIR returns 0 entries eof=false, never NFS3ERR_TOOSMALL (nfs/server.rs readdir budget)"]
fn readdir_tiny_dircount_does_not_livelock() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);
    // Root has exactly one child: "dir". Ask for a dircount too small to hold it.

    // --- READDIR, dircount = 8 ---
    let mut a = Encoder::new();
    a.opaque(&root_handle);
    a.u64(0); // cookie
    a.fixed(&[0u8; 8]); // cookieverf
    a.u32(8); // dircount
    let body = call(
        &mut stream,
        20,
        NFS_PROGRAM,
        NFS_VERSION,
        READDIR,
        a.into_bytes(),
    );
    let (status, count, eof) = parse_readdir(&body, false);
    assert!(
        !(status == NFS3_OK && count == 0 && !eof),
        "READDIR livelock: OK with zero entries and eof=false (client re-issues cookie=0 forever). \
         Expected at least one entry OR NFS3ERR_TOOSMALL. got status={status} count={count} eof={eof}"
    );
    // Positively assert the RFC-conforming shape.
    assert!(
        count >= 1 || status == NFS3ERR_TOOSMALL,
        "READDIR must return an entry or NFS3ERR_TOOSMALL; got status={status} count={count} eof={eof}"
    );

    // --- READDIRPLUS, dircount = 8, maxcount = 64 ---
    let mut a = Encoder::new();
    a.opaque(&root_handle);
    a.u64(0); // cookie
    a.fixed(&[0u8; 8]); // cookieverf
    a.u32(8); // dircount
    a.u32(64); // maxcount
    let body = call(
        &mut stream,
        21,
        NFS_PROGRAM,
        NFS_VERSION,
        READDIRPLUS,
        a.into_bytes(),
    );
    let (status, count, eof) = parse_readdir(&body, true);
    assert!(
        !(status == NFS3_OK && count == 0 && !eof),
        "READDIRPLUS livelock: OK with zero entries and eof=false. \
         Expected at least one entry OR NFS3ERR_TOOSMALL. got status={status} count={count} eof={eof}"
    );
    assert!(
        count >= 1 || status == NFS3ERR_TOOSMALL,
        "READDIRPLUS must return an entry or NFS3ERR_TOOSMALL; got status={status} count={count} eof={eof}"
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

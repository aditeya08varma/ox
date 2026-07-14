// LENS-3: The oxfs namespace has no "." or ".." entries. `Namespace::lookup`
// only reads a node's `children` map, and READDIR iterates `node.children`
// only. RFC 1813 requires a server to resolve the special names "." (the
// directory itself) and ".." (its parent) in LOOKUP. This test drives the
// wire protocol and asserts that contract.
//
// Failure prevented: a subdirectory cannot resolve its parent over NFS, so
// LOOKUP(subdir, "..") returns NFS3ERR_NOENT instead of the parent handle,
// and LOOKUP(dir, ".") returns NFS3ERR_NOENT instead of the dir's own handle.

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
        "oxfs-dotdot-{}-{}-{}",
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

/// Successful LOOKUP helper: asserts NFS3_OK and returns the child handle.
fn lookup(stream: &mut TcpStream, xid: u32, parent: &[u8], name: &str) -> Vec<u8> {
    let mut a = Encoder::new();
    a.opaque(parent);
    a.string(name);
    let body = call(stream, xid, NFS_PROGRAM, NFS_VERSION, 3, a.into_bytes());
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), NFS3_OK, "LOOKUP({name}) status");
    let handle = d.opaque(64).unwrap().to_vec();
    assert!(d.boolean().unwrap());
    skip_attr(&mut d);
    assert!(d.boolean().unwrap());
    skip_attr(&mut d);
    handle
}

/// Raw LOOKUP that returns only the NFS status word and, on success, the handle.
fn lookup_status(stream: &mut TcpStream, xid: u32, parent: &[u8], name: &str) -> (u32, Vec<u8>) {
    let mut a = Encoder::new();
    a.opaque(parent);
    a.string(name);
    let body = call(stream, xid, NFS_PROGRAM, NFS_VERSION, 3, a.into_bytes());
    let mut d = Decoder::new(&body);
    let status = d.u32().unwrap();
    let handle = if status == NFS3_OK {
        d.opaque(64).unwrap().to_vec()
    } else {
        Vec::new()
    };
    (status, handle)
}

// RFC 1813 3.3.3 LOOKUP: the special filenames "." and ".." must resolve to
// the directory itself and its parent, respectively. A subdirectory that
// cannot name its own parent breaks the "walk to the export root" contract
// that every hierarchical filesystem relies on.
#[test]
#[ignore = "KNOWN BUG: LOOKUP of '.'/'..' returns NOENT and READDIR omits them (namespace.rs lookup, nfs/server.rs readdir)"]
fn lookup_dot_and_dotdot_resolve_self_and_parent() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);

    // Resolve /dir and confirm the file underneath it exists (so `dir` is a
    // real interior directory whose parent is the export root).
    let dir = lookup(&mut stream, 2, &root_handle, "dir");
    let _file = lookup(&mut stream, 3, &dir, "file.txt");

    // "." on the directory must return the directory's own handle.
    let (dot_status, dot_handle) = lookup_status(&mut stream, 4, &dir, ".");
    // ".." on the directory must return the parent (export root) handle.
    let (dotdot_status, dotdot_handle) = lookup_status(&mut stream, 5, &dir, "..");

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();

    assert_eq!(
        dot_status, NFS3_OK,
        "LOOKUP(dir, \".\") must succeed (RFC 1813); got status {dot_status}"
    );
    assert_eq!(
        dot_handle, dir,
        "LOOKUP(dir, \".\") must return the directory's own file handle"
    );
    assert_eq!(
        dotdot_status, NFS3_OK,
        "LOOKUP(dir, \"..\") must succeed (RFC 1813); got status {dotdot_status}"
    );
    assert_eq!(
        dotdot_handle, root_handle,
        "LOOKUP(dir, \"..\") must return the parent (export root) file handle"
    );
}

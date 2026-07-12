use oxfs::nfs::{
    Decoder, Encoder, MOUNT_PROGRAM, MOUNT_VERSION, NFS_PROGRAM, NFS_VERSION, NFS3_OK,
    NFS3ERR_ROFS, NfsServer, ServerConfig,
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
        "oxfs-nfs-{}-{}-{}",
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
fn lookup(stream: &mut TcpStream, parent: &[u8], name: &str) -> Vec<u8> {
    let mut a = Encoder::new();
    a.opaque(parent);
    a.string(name);
    let body = call(stream, 2, NFS_PROGRAM, NFS_VERSION, 3, a.into_bytes());
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), NFS3_OK);
    let handle = d.opaque(64).unwrap().to_vec();
    assert!(d.boolean().unwrap());
    skip_attr(&mut d);
    assert!(d.boolean().unwrap());
    skip_attr(&mut d);
    assert_eq!(d.remaining(), 0);
    handle
}

fn lookup_body(stream: &mut TcpStream, xid: u32, parent: &[u8], name: &str) -> Vec<u8> {
    let mut args = Encoder::new();
    args.opaque(parent);
    args.string(name);
    call(stream, xid, NFS_PROGRAM, NFS_VERSION, 3, args.into_bytes())
}

fn assert_lookup_failure(body: &[u8], status: u32, attributes: bool) {
    let mut decoder = Decoder::new(body);
    assert_eq!(decoder.u32().unwrap(), status);
    assert_eq!(decoder.boolean().unwrap(), attributes);
    if attributes {
        skip_attr(&mut decoder);
    }
    assert_eq!(
        decoder.remaining(),
        0,
        "trailing or malformed LOOKUP3resfail"
    );
}

#[test]
fn lookup_failures_encode_the_complete_post_op_attr_union() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);
    let dir = lookup(&mut stream, &root_handle, "dir");
    let file = lookup(&mut stream, &dir, "file.txt");

    assert_lookup_failure(
        &lookup_body(&mut stream, 10, &root_handle, "missing"),
        oxfs::nfs::NFS3ERR_NOENT,
        true,
    );
    assert_lookup_failure(
        &lookup_body(&mut stream, 11, b"not-an-oxfs-handle", "missing"),
        oxfs::nfs::NFS3ERR_BADHANDLE,
        false,
    );
    assert_lookup_failure(
        &lookup_body(&mut stream, 12, &file, "child"),
        oxfs::nfs::NFS3ERR_NOTDIR,
        true,
    );

    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn mount_export_is_a_well_formed_linked_list() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let body = call(&mut stream, 9, MOUNT_PROGRAM, MOUNT_VERSION, 5, Vec::new());
    let mut d = Decoder::new(&body);
    assert!(d.boolean().unwrap());
    assert_eq!(d.string(1024).unwrap(), "/oxfs");
    assert!(
        !d.boolean().unwrap(),
        "export must have no group restrictions"
    );
    assert!(!d.boolean().unwrap(), "export list must terminate");
    assert_eq!(d.remaining(), 0);
    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn mount_lookup_read_and_rofs_over_onc_rpc() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);
    let dir = lookup(&mut stream, &root_handle, "dir");
    let file = lookup(&mut stream, &dir, "file.txt");
    let mut args = Encoder::new();
    args.opaque(&file);
    args.u64(1);
    args.u32(8);
    let body = call(
        &mut stream,
        3,
        NFS_PROGRAM,
        NFS_VERSION,
        6,
        args.into_bytes(),
    );
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), NFS3_OK);
    assert!(d.boolean().unwrap());
    skip_attr(&mut d);
    assert_eq!(d.u32().unwrap(), 2);
    assert!(d.boolean().unwrap());
    assert_eq!(d.opaque(8).unwrap(), b"bc");
    let mut mutation = Encoder::new();
    mutation.opaque(&dir);
    mutation.string("nope");
    let body = call(
        &mut stream,
        4,
        NFS_PROGRAM,
        NFS_VERSION,
        8,
        mutation.into_bytes(),
    );
    assert_eq!(Decoder::new(&body).u32().unwrap(), NFS3ERR_ROFS);
    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn accepts_fragmented_rpc_records() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let mut e = Encoder::new();
    e.u32(91);
    e.u32(0);
    e.u32(2);
    e.u32(NFS_PROGRAM);
    e.u32(NFS_VERSION);
    e.u32(0);
    e.u32(0);
    e.opaque(&[]);
    e.u32(0);
    e.opaque(&[]);
    let request = e.into_bytes();
    let split = 13;
    stream.write_all(&(split as u32).to_be_bytes()).unwrap();
    stream.write_all(&request[..split]).unwrap();
    stream
        .write_all(&(((request.len() - split) as u32) | 0x8000_0000).to_be_bytes())
        .unwrap();
    stream.write_all(&request[split..]).unwrap();
    let mut header = [0; 4];
    stream.read_exact(&mut header).unwrap();
    let mut reply = vec![0; (u32::from_be_bytes(header) & 0x7fff_ffff) as usize];
    stream.read_exact(&mut reply).unwrap();
    let mut d = Decoder::new(&reply);
    assert_eq!(d.u32().unwrap(), 91);
    assert_eq!(d.u32().unwrap(), 1);
    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn metadata_probes_and_access_never_advertise_write() {
    let (root, server) = setup();
    let mut stream = TcpStream::connect(server.address()).unwrap();
    let root_handle = mount(&mut stream);
    for (xid, procedure) in [(11, 18), (12, 19), (13, 20)] {
        let mut args = Encoder::new();
        args.opaque(&root_handle);
        let body = call(
            &mut stream,
            xid,
            NFS_PROGRAM,
            NFS_VERSION,
            procedure,
            args.into_bytes(),
        );
        assert_eq!(Decoder::new(&body).u32().unwrap(), NFS3_OK);
    }
    let mut args = Encoder::new();
    args.opaque(&root_handle);
    args.u32(0x3f);
    let body = call(
        &mut stream,
        14,
        NFS_PROGRAM,
        NFS_VERSION,
        4,
        args.into_bytes(),
    );
    let mut d = Decoder::new(&body);
    assert_eq!(d.u32().unwrap(), NFS3_OK);
    assert!(d.boolean().unwrap());
    skip_attr(&mut d);
    let allowed = d.u32().unwrap();
    assert_eq!(
        allowed & 0x1c,
        0,
        "write/delete access leaked: {allowed:#x}"
    );
    server.shutdown().unwrap();
    std::fs::remove_dir_all(root).unwrap();
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

use oxfs::mount::{self, MountState};
use oxfs::nfs::{NfsServer, ServerConfig};
use oxfs::{
    CacheConfig, ContentRef, ContentSource, DEFAULT_CACHE_MAX_BYTES, FetchError, Manifest,
    ManifestEntry, Workspace,
};
use std::fs::File;
use std::io::{self, Write};
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::Arc;

struct DirectorySource {
    root: PathBuf,
}
impl ContentSource for DirectorySource {
    fn fetch(&self, reference: &ContentRef, output: &mut dyn Write) -> Result<(), FetchError> {
        let mut input = File::open(self.root.join(reference.storage_key())).map_err(|error| {
            if error.kind() == io::ErrorKind::NotFound {
                FetchError::NotFound
            } else {
                FetchError::Io(error)
            }
        })?;
        io::copy(&mut input, output)?;
        Ok(())
    }
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let cache_max_bytes = match std::env::var("SAGEOX_OXFS_CACHE_MAX_BYTES") {
        Ok(value) => value
            .parse::<u64>()
            .ok()
            .filter(|v| *v > 0)
            .ok_or("SAGEOX_OXFS_CACHE_MAX_BYTES must be a positive integer byte count")?,
        Err(std::env::VarError::NotPresent) => DEFAULT_CACHE_MAX_BYTES,
        Err(error) => return Err(error.into()),
    };
    let mut args = std::env::args().skip(1);
    let command = args.next().unwrap_or_else(|| "help".into());
    if command == "mount-demo" {
        let state = PathBuf::from(args.next().ok_or("missing state-dir")?);
        let objects = PathBuf::from(args.next().ok_or("missing object-dir")?);
        let mountpoint = PathBuf::from(args.next().ok_or("missing mountpoint")?);
        return mount::mount_demo(&state, &objects, &mountpoint).map_err(Into::into);
    }
    if command == "unmount" {
        let state = PathBuf::from(args.next().ok_or("missing state-dir")?);
        return mount::unmount(&state).map_err(Into::into);
    }
    if command != "serve-demo" {
        eprintln!(
            "usage:\n  oxfsd mount-demo <state-dir> <object-dir> <mountpoint>\n  oxfsd unmount <state-dir>\n  oxfsd serve-demo <state-dir> <object-dir> [127.0.0.1:0]"
        );
        return Ok(());
    }
    let state = PathBuf::from(args.next().ok_or("missing state-dir")?);
    let objects = PathBuf::from(args.next().ok_or("missing object-dir")?);
    let address: SocketAddr = args
        .next()
        .unwrap_or_else(|| "127.0.0.1:0".into())
        .parse()?;
    let mut ready_file = None;
    let mut mountpoint = None;
    while let Some(option) = args.next() {
        match option.as_str() {
            "--ready-file" => {
                ready_file = Some(PathBuf::from(args.next().ok_or("missing ready-file")?))
            }
            "--mountpoint" => {
                mountpoint = Some(PathBuf::from(args.next().ok_or("missing mountpoint")?))
            }
            _ => return Err(format!("unknown option: {option}").into()),
        }
    }
    let bytes = b"hello from oxFS\n";
    let digest = "0d018694d173a2a486c324edee3ead66c123b3383915b2a5d8e9347660ac4dc3";
    std::fs::create_dir_all(&objects)?;
    let object = objects.join(format!("sha256-{digest}"));
    if !object.exists() {
        std::fs::write(&object, bytes)?;
    }
    let source = Arc::new(DirectorySource { root: objects });
    let workspace = Workspace::open_with_config(
        &state,
        source,
        CacheConfig {
            max_bytes: cache_max_bytes,
        },
    )?;
    let content = ContentRef::new("demo", "sha256", digest, bytes.len() as u64)?;
    workspace.apply(Manifest {
        session_id: "demo".into(),
        generation: 1,
        entries: vec![ManifestEntry::new(
            "hello.txt",
            "demo",
            "Session",
            0o444,
            0,
            content,
            "walking skeleton",
        )?],
    })?;
    let server = NfsServer::new(
        workspace,
        ServerConfig {
            address,
            ..Default::default()
        },
    )
    .spawn()?;
    eprintln!(
        "level=INFO action=nfs_listen address={} export=/oxfs mount_attempted=false",
        server.address()
    );
    if let Some(path) = ready_file {
        mount::publish_ready(
            &path,
            &MountState {
                pid: std::process::id(),
                port: server.address().port(),
                mountpoint: mountpoint.ok_or("missing --mountpoint")?,
            },
        )?;
    }
    loop {
        std::thread::park();
    }
}

use oxfs::{ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace};
use std::collections::BTreeMap;
use std::io::Write;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

struct MemorySource {
    objects: BTreeMap<String, Vec<u8>>,
}
impl ContentSource for MemorySource {
    fn fetch(&self, r: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
        let bytes = self.objects.get(&r.digest).ok_or(FetchError::NotFound)?;
        w.write_all(bytes)?;
        Ok(())
    }
}
fn temp(name: &str) -> std::path::PathBuf {
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    std::env::temp_dir().join(format!(
        "oxfs-{name}-{}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}
fn reference() -> ContentRef {
    ContentRef::new(
        "t",
        "sha256",
        "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
        3,
    )
    .unwrap()
}
fn source(bytes: &[u8]) -> Arc<dyn ContentSource> {
    Arc::new(MemorySource {
        objects: BTreeMap::from([(reference().digest, bytes.to_vec())]),
    })
}
fn manifest(generation: u64) -> Manifest {
    Manifest {
        session_id: "s1".into(),
        generation,
        entries: vec![
            ManifestEntry::new(
                "sessions/one/raw.jsonl",
                "src",
                "Session",
                0o644,
                123,
                reference(),
                "related",
            )
            .unwrap(),
        ],
    }
}

#[test]
fn wysiwyg_and_stable_restart() {
    let root = temp("restart");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert!(ws.snapshot().by_path("sessions/one/raw.jsonl").is_none());
    ws.apply(manifest(1)).unwrap();
    let before = ws
        .snapshot()
        .by_path("sessions/one/raw.jsonl")
        .unwrap()
        .inode;
    assert_eq!(ws.open_inode(before).unwrap().read(0, 99).unwrap(), b"abc");
    drop(ws);
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert_eq!(
        before,
        ws.snapshot()
            .by_path("sessions/one/raw.jsonl")
            .unwrap()
            .inode
    );
    std::fs::remove_dir_all(root).unwrap();
}
#[test]
fn invalid_bytes_never_become_visible() {
    let root = temp("invalid");
    let ws = Workspace::open(&root, source(b"abd")).unwrap();
    assert!(ws.apply(manifest(1)).is_err());
    assert!(ws.snapshot().by_path("sessions/one/raw.jsonl").is_none());
    std::fs::remove_dir_all(root).unwrap();
}
#[test]
fn stale_generation_is_ignored() {
    let root = temp("stale");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    assert!(ws.apply(manifest(2)).unwrap().applied);
    assert!(!ws.apply(manifest(1)).unwrap().applied);
    std::fs::remove_dir_all(root).unwrap();
}

struct CountingSource {
    calls: Arc<AtomicUsize>,
}
impl ContentSource for CountingSource {
    fn fetch(&self, _: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        std::thread::sleep(std::time::Duration::from_millis(20));
        w.write_all(b"abc")?;
        Ok(())
    }
}

#[test]
fn concurrent_sessions_coalesce_one_fetch() {
    let root = temp("coalesce");
    let calls = Arc::new(AtomicUsize::new(0));
    let ws = Workspace::open(
        &root,
        Arc::new(CountingSource {
            calls: calls.clone(),
        }),
    )
    .unwrap();
    let mut joins = vec![];
    for index in 0..8 {
        let ws = ws.clone();
        joins.push(std::thread::spawn(move || {
            ws.apply(Manifest {
                session_id: format!("s{index}"),
                generation: 1,
                entries: vec![
                    ManifestEntry::new(
                        format!("p{index}"),
                        "src",
                        "Session",
                        0o444,
                        0,
                        reference(),
                        "shared",
                    )
                    .unwrap(),
                ],
            })
            .unwrap();
        }));
    }
    for join in joins {
        join.join().unwrap();
    }
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn conflicting_session_content_does_not_replace_live_snapshot() {
    let root = temp("conflict");
    let mut objects = BTreeMap::new();
    objects.insert(reference().digest, b"abc".to_vec());
    let second = ContentRef::new(
        "t",
        "sha256",
        "3608bca1e44ea6c4d268eb6db02260269892c0b42b86bbf1e77a6fa16c3c9282",
        3,
    )
    .unwrap();
    objects.insert(second.digest.clone(), b"xyz".to_vec());
    let ws = Workspace::open(&root, Arc::new(MemorySource { objects })).unwrap();
    ws.apply(Manifest {
        session_id: "a".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new("same", "a", "Session", 0o444, 0, reference(), "first").unwrap(),
        ],
    })
    .unwrap();
    let inode = ws.snapshot().by_path("same").unwrap().inode;
    assert!(
        ws.apply(Manifest {
            session_id: "b".into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("same", "b", "Session", 0o444, 0, second, "second").unwrap()
            ]
        })
        .is_err()
    );
    assert_eq!(ws.open_inode(inode).unwrap().read(0, 3).unwrap(), b"abc");
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn synthetic_index_is_resident_and_aggregates_selectors() {
    let root = temp("index");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    for (session, reason) in [("one", "first reason"), ("two", "second reason")] {
        ws.apply(Manifest {
            session_id: session.into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("shared", "src", "Session", 0o444, 0, reference(), reason)
                    .unwrap(),
            ],
        })
        .unwrap();
    }
    let snapshot = ws.snapshot();
    let md = snapshot.by_path(".sageox/INDEX.md").unwrap().inode;
    let json = snapshot.by_path(".sageox/INDEX.json").unwrap().inode;
    let md = String::from_utf8(ws.open_inode(md).unwrap().read(0, usize::MAX).unwrap()).unwrap();
    let json =
        String::from_utf8(ws.open_inode(json).unwrap().read(0, usize::MAX).unwrap()).unwrap();
    for expected in ["shared", "one", "two", "first reason", "second reason"] {
        assert!(md.contains(expected), "markdown missing {expected}: {md}");
        assert!(json.contains(expected), "JSON missing {expected}: {json}");
    }
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn same_size_corruption_is_rejected_after_restart() {
    let root = temp("corrupt");
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    ws.apply(manifest(1)).unwrap();
    drop(ws);
    let object = walk_files(&root.join("cache/objects"))
        .into_iter()
        .find(|p| {
            p.file_name()
                .unwrap()
                .to_string_lossy()
                .starts_with("sha256-")
        })
        .unwrap();
    std::fs::write(&object, b"abd").unwrap();
    let ws = Workspace::open(&root, source(b"abc")).unwrap();
    ws.apply(manifest(1)).unwrap();
    let inode = ws
        .snapshot()
        .by_path("sessions/one/raw.jsonl")
        .unwrap()
        .inode;
    assert_eq!(ws.open_inode(inode).unwrap().read(0, 3).unwrap(), b"abc");
    std::fs::remove_dir_all(root).unwrap();
}
fn walk_files(root: &std::path::Path) -> Vec<std::path::PathBuf> {
    let mut out = vec![];
    for entry in std::fs::read_dir(root).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            out.extend(walk_files(&path))
        } else {
            out.push(path)
        }
    }
    out
}

fn xyz_reference() -> ContentRef {
    ContentRef::new(
        "t",
        "sha256",
        "3608bca1e44ea6c4d268eb6db02260269892c0b42b86bbf1e77a6fa16c3c9282",
        3,
    )
    .unwrap()
}

#[test]
fn ranked_admission_stops_but_keeps_later_resident_content() {
    let root = temp("ranked");
    let objects = BTreeMap::from([
        (reference().digest, b"abc".to_vec()),
        (xyz_reference().digest, b"xyz".to_vec()),
    ]);
    let ws = Workspace::open_with_config(
        &root,
        Arc::new(MemorySource { objects }),
        oxfs::CacheConfig { max_bytes: 3 },
    )
    .unwrap();
    ws.apply(Manifest {
        session_id: "old".into(),
        generation: 1,
        entries: vec![
            ManifestEntry::new(
                "resident",
                "x",
                "Session",
                0o444,
                0,
                xyz_reference(),
                "resident",
            )
            .unwrap(),
        ],
    })
    .unwrap();
    let outcome = ws
        .apply(Manifest {
            session_id: "new".into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("blocked", "a", "Session", 0o444, 0, reference(), "rank one")
                    .unwrap(),
                ManifestEntry::new(
                    "also-resident",
                    "b",
                    "Session",
                    0o444,
                    0,
                    xyz_reference(),
                    "rank two",
                )
                .unwrap(),
            ],
        })
        .unwrap();
    assert_eq!((outcome.available, outcome.stopped), (2, 1));
    assert!(ws.snapshot().by_path("blocked").is_none());
    assert!(ws.snapshot().by_path("also-resident").is_some());
    let json_inode = ws.snapshot().by_path(".sageox/INDEX.json").unwrap().inode;
    let json = String::from_utf8(
        ws.open_inode(json_inode)
            .unwrap()
            .read(0, usize::MAX)
            .unwrap(),
    )
    .unwrap();
    assert!(json.contains("\"path\":\"blocked\"") && json.contains("stopped: cache_limit_reached"));
    std::fs::remove_dir_all(root).unwrap();
}

#[test]
fn dropped_content_is_evicted_and_oversize_never_fetches() {
    let root = temp("evict");
    let calls = Arc::new(AtomicUsize::new(0));
    struct Source {
        calls: Arc<AtomicUsize>,
    }
    impl ContentSource for Source {
        fn fetch(&self, r: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
            self.calls.fetch_add(1, Ordering::SeqCst);
            if r.digest == reference().digest {
                w.write_all(b"abc")?
            } else {
                w.write_all(b"xyz")?
            };
            Ok(())
        }
    }
    let ws = Workspace::open_with_config(
        &root,
        Arc::new(Source {
            calls: calls.clone(),
        }),
        oxfs::CacheConfig { max_bytes: 3 },
    )
    .unwrap();
    ws.apply(manifest(1)).unwrap();
    ws.apply(Manifest {
        session_id: "s1".into(),
        generation: 2,
        entries: vec![
            ManifestEntry::new(
                "replacement",
                "x",
                "Session",
                0o444,
                0,
                xyz_reference(),
                "new",
            )
            .unwrap(),
        ],
    })
    .unwrap();
    assert!(ws.snapshot().by_path("replacement").is_some());
    let oversized = ContentRef::new("t", "sha256", "00", 4).unwrap();
    let before = calls.load(Ordering::SeqCst);
    let outcome = ws
        .apply(Manifest {
            session_id: "big".into(),
            generation: 1,
            entries: vec![
                ManifestEntry::new("too-big", "x", "Session", 0o444, 0, oversized, "big").unwrap(),
            ],
        })
        .unwrap();
    assert_eq!(calls.load(Ordering::SeqCst), before);
    assert_eq!(outcome.stopped, 1);
    std::fs::remove_dir_all(root).unwrap();
}

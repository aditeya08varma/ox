use oxfs::mount;
use oxfs::nfs::{NfsServer, ServerConfig};
use oxfs::{
    CacheConfig, ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace,
};
use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File, Metadata};
use std::io::{self, BufRead, IsTerminal, Write};
use std::path::{Component, Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, RwLock};
use std::time::UNIX_EPOCH;

const DEFAULT_CACHE_BYTES: u64 = 10 * 1024 * 1024 * 1024;

#[derive(Debug)]
struct Config {
    source: PathBuf,
    mountpoint: PathBuf,
    state: Option<PathBuf>,
    cache_bytes: u64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct SourceFile {
    path: PathBuf,
    len: u64,
    modified: Option<std::time::SystemTime>,
}

#[derive(Default)]
struct LocalSource {
    files: RwLock<BTreeMap<String, SourceFile>>,
}

impl LocalSource {
    fn replace(&self, files: BTreeMap<String, SourceFile>) -> io::Result<()> {
        *self
            .files
            .write()
            .map_err(|_| io::Error::other("local source lock poisoned"))? = files;
        Ok(())
    }
}

impl ContentSource for LocalSource {
    fn fetch(&self, reference: &ContentRef, output: &mut dyn Write) -> Result<(), FetchError> {
        let expected = self
            .files
            .read()
            .map_err(|_| io::Error::other("local source lock poisoned"))?
            .get(&reference.storage_key())
            .cloned()
            .ok_or(FetchError::NotFound)?;
        let before = fs::metadata(&expected.path)?;
        verify_metadata(&expected, &before)?;
        let mut input = File::open(&expected.path)?;
        io::copy(&mut input, output)?;
        let after = input.metadata()?;
        verify_metadata(&expected, &after)?;
        Ok(())
    }
}

fn verify_metadata(expected: &SourceFile, actual: &Metadata) -> io::Result<()> {
    if !actual.is_file()
        || actual.len() != expected.len
        || actual.modified().ok() != expected.modified
    {
        return Err(io::Error::other(format!(
            "source changed while selecting: {}",
            expected.path.display()
        )));
    }
    Ok(())
}

fn main() {
    let config = match parse_args(std::env::args().skip(1).collect()) {
        Ok(config) => config,
        Err(error) => {
            eprintln!("oxdirtest: {error}");
            std::process::exit(2);
        }
    };
    if let Err(error) = run(config) {
        eprintln!("oxdirtest: {error}");
        std::process::exit(1);
    }
}

fn run(config: Config) -> Result<(), Box<dyn std::error::Error>> {
    if !cfg!(target_os = "macos") {
        return Err("oxdirtest requires macOS mount_nfs".into());
    }
    let source_root = config.source.canonicalize()?;
    if !source_root.is_dir() {
        return Err(format!("source is not a directory: {}", source_root.display()).into());
    }
    let temporary = config.state.is_none();
    let state = config.state.unwrap_or_else(temp_root);
    fs::create_dir_all(&state)?;
    let result = run_session(&config.mountpoint, &source_root, &state, config.cache_bytes);
    if temporary
        && let Err(error) = fs::remove_dir_all(&state)
        && error.kind() != io::ErrorKind::NotFound
    {
        eprintln!(
            "oxdirtest: warning: cannot remove {}: {error}",
            state.display()
        );
    }
    result
}

fn run_session(
    mountpoint: &Path,
    source_root: &Path,
    state: &Path,
    cache_bytes: u64,
) -> Result<(), Box<dyn std::error::Error>> {
    let source = Arc::new(LocalSource::default());
    let workspace = Workspace::open_with_config(
        &state.join("workspace"),
        source.clone(),
        CacheConfig {
            max_bytes: cache_bytes,
        },
    )?;
    let stop = Arc::new(AtomicBool::new(false));
    install_stop_handler(stop.clone())?;
    let server = NfsServer::new(workspace.clone(), ServerConfig::default()).spawn()?;
    let mounted = mount::mount_server(server.address().port(), mountpoint)?;
    eprintln!(
        "oxdirtest: mounted {} from {}\ncommands: select DIR [DIR ...] | clear | status | help | quit",
        mountpoint.display(),
        source_root.display()
    );
    let interaction = command_loop(source_root, mountpoint, &source, &workspace, &stop);
    eprintln!("oxdirtest: unmounting {}...", mountpoint.display());
    let teardown = mounted
        .unmount()
        .and_then(|()| server.shutdown())
        .map_err(Box::<dyn std::error::Error>::from);
    drop(workspace);
    teardown?;
    interaction
}

fn command_loop(
    source_root: &Path,
    mountpoint: &Path,
    source: &LocalSource,
    workspace: &Workspace,
    stop: &AtomicBool,
) -> Result<(), Box<dyn std::error::Error>> {
    let stdin = io::stdin();
    let interactive = stdin.is_terminal();
    let mut generation = 0u64;
    let mut selected = Vec::new();
    while !stop.load(Ordering::SeqCst) {
        if interactive {
            eprint!("oxdirtest> ");
            io::stderr().flush()?;
        }
        let mut line = String::new();
        if stdin.lock().read_line(&mut line)? == 0 {
            break;
        }
        let words: Vec<_> = line.split_whitespace().collect();
        let Some(command) = words.first().copied() else {
            continue;
        };
        match command {
            "select" if words.len() > 1 => {
                let paths = parse_selections(source_root, &words[1..])?;
                eprintln!("oxdirtest: scanning {} selection(s)...", paths.len());
                let indexed = index_files(source_root, &paths)?;
                let (capacity, _) = workspace.cache_capacity();
                if indexed.bytes > capacity {
                    eprintln!(
                        "oxdirtest: selection needs {} bytes but cache capacity is {}; selection unchanged",
                        indexed.bytes, capacity
                    );
                    continue;
                }
                generation += 1;
                source.replace(indexed.sources)?;
                let outcome = match workspace.apply(Manifest {
                    session_id: "human-selection".into(),
                    generation,
                    entries: indexed.entries,
                }) {
                    Ok(outcome) => outcome,
                    // A rejected selection is a normal interactive outcome, not
                    // a reason to tear down the mount. The previous selection is
                    // still published and still readable.
                    Err(error) => {
                        eprintln!("oxdirtest: selection rejected: {error}");
                        eprintln!(
                            "oxdirtest: selection unchanged; run `clear` first, or restart with a larger --cache-bytes"
                        );
                        continue;
                    }
                };
                selected = paths;
                eprintln!(
                    "oxdirtest: generation={} files={} bytes={} available={} stopped={} mountpoint={}",
                    generation,
                    indexed.files,
                    indexed.bytes,
                    outcome.available,
                    outcome.stopped,
                    mountpoint.display()
                );
                if outcome.stopped > 0 {
                    eprintln!(
                        "oxdirtest: WARNING {} of {} file(s) did not fit the cache and are NOT in the mount",
                        outcome.stopped, indexed.files
                    );
                }
            }
            "select" => eprintln!("usage: select DIR [DIR ...]"),
            "clear" => {
                generation += 1;
                let outcome = workspace.apply(Manifest {
                    session_id: "human-selection".into(),
                    generation,
                    entries: Vec::new(),
                })?;
                source.replace(BTreeMap::new())?;
                selected.clear();
                eprintln!(
                    "oxdirtest: generation={} cleared available={}",
                    generation, outcome.available
                );
            }
            "status" => eprintln!(
                "oxdirtest: generation={} selections={} [{}]",
                generation,
                selected.len(),
                selected
                    .iter()
                    .map(|path| path.display().to_string())
                    .collect::<Vec<_>>()
                    .join(", ")
            ),
            "help" => eprintln!(
                "commands:\n  select DIR [DIR ...]  replace mounted contents\n  clear                 mount an empty selection\n  status                show current selection\n  quit                  unmount and exit"
            ),
            "quit" | "exit" => break,
            other => eprintln!("oxdirtest: unknown command {other:?}; type help"),
        }
    }
    Ok(())
}

struct Indexed {
    entries: Vec<ManifestEntry>,
    sources: BTreeMap<String, SourceFile>,
    files: usize,
    bytes: u64,
}

fn index_files(source_root: &Path, selections: &[PathBuf]) -> io::Result<Indexed> {
    let mut files = BTreeMap::new();
    for selection in selections {
        collect_files(&source_root.join(selection), &mut files)?;
    }
    let mut entries = Vec::with_capacity(files.len());
    let mut sources = BTreeMap::new();
    for (absolute, metadata) in files {
        let relative = absolute
            .strip_prefix(source_root)
            .map_err(io::Error::other)?;
        let content = hash_file(&absolute, &metadata)?;
        let source_file = SourceFile {
            path: absolute.clone(),
            len: metadata.len(),
            modified: metadata.modified().ok(),
        };
        sources.insert(content.storage_key(), source_file);
        entries.push(
            ManifestEntry::new(
                path_string(relative)?,
                absolute.display().to_string(),
                "LocalDirectory",
                readonly_mode(&metadata),
                modified_secs(&metadata),
                content,
                "human-selected local directory",
            )
            .map_err(io::Error::other)?,
        );
    }
    let total_bytes = sources.values().map(|source| source.len).sum();
    Ok(Indexed {
        files: entries.len(),
        entries,
        sources,
        bytes: total_bytes,
    })
}

fn collect_files(path: &Path, files: &mut BTreeMap<PathBuf, Metadata>) -> io::Result<()> {
    let metadata = fs::symlink_metadata(path)?;
    if metadata.file_type().is_symlink() {
        eprintln!("oxdirtest: skipping symlink {}", path.display());
        return Ok(());
    }
    if metadata.is_file() {
        files.insert(path.to_owned(), metadata);
        return Ok(());
    }
    if !metadata.is_dir() {
        eprintln!("oxdirtest: skipping special file {}", path.display());
        return Ok(());
    }
    let mut children = fs::read_dir(path)?.collect::<Result<Vec<_>, _>>()?;
    children.sort_by_key(|entry| entry.file_name());
    for child in children {
        collect_files(&child.path(), files)?;
    }
    Ok(())
}

fn hash_file(path: &Path, metadata: &Metadata) -> io::Result<ContentRef> {
    let expected = SourceFile {
        path: path.to_owned(),
        len: metadata.len(),
        modified: metadata.modified().ok(),
    };
    let file = File::open(path)?;
    let content = ContentRef::for_sha256_reader("oxdirtest", &file)?;
    verify_metadata(&expected, &file.metadata()?)?;
    if content.size != expected.len {
        return Err(io::Error::other(format!(
            "source changed while selecting: {}",
            path.display()
        )));
    }
    Ok(content)
}

fn parse_selections(source_root: &Path, values: &[&str]) -> io::Result<Vec<PathBuf>> {
    let mut unique = BTreeSet::new();
    for value in values {
        let relative = safe_relative(Path::new(value))?;
        let canonical = source_root.join(&relative).canonicalize()?;
        if !canonical.starts_with(source_root) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("selection escapes source root: {value}"),
            ));
        }
        let canonical_relative = canonical
            .strip_prefix(source_root)
            .map_err(io::Error::other)?;
        unique.insert(canonical_relative.to_owned());
    }
    Ok(unique.into_iter().collect())
}

fn safe_relative(path: &Path) -> io::Result<PathBuf> {
    if path.as_os_str().is_empty()
        || path
            .components()
            .any(|part| !matches!(part, Component::Normal(_) | Component::CurDir))
    {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!(
                "selection must be a relative path inside the source: {}",
                path.display()
            ),
        ));
    }
    Ok(path.to_owned())
}

fn path_string(path: &Path) -> io::Result<String> {
    path.to_str().map(str::to_owned).ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            format!("path is not UTF-8: {}", path.display()),
        )
    })
}

#[cfg(unix)]
fn readonly_mode(metadata: &Metadata) -> u32 {
    use std::os::unix::fs::MetadataExt;
    metadata.mode() & 0o555
}

#[cfg(not(unix))]
fn readonly_mode(_: &Metadata) -> u32 {
    0o444
}

fn modified_secs(metadata: &Metadata) -> u64 {
    metadata
        .modified()
        .ok()
        .and_then(|time| time.duration_since(UNIX_EPOCH).ok())
        .map_or(0, |duration| duration.as_secs())
}

fn temp_root() -> PathBuf {
    std::env::temp_dir().join(format!("oxdirtest-{}", std::process::id()))
}

#[cfg(unix)]
fn install_stop_handler(stop: Arc<AtomicBool>) -> Result<(), Box<dyn std::error::Error>> {
    ctrlc::set_handler(move || stop.store(true, Ordering::SeqCst))?;
    Ok(())
}

#[cfg(not(unix))]
fn install_stop_handler(_: Arc<AtomicBool>) -> Result<(), Box<dyn std::error::Error>> {
    Err("signal handling is only available on unix".into())
}

fn parse_args(args: Vec<String>) -> Result<Config, String> {
    let mut source = None;
    let mut mountpoint = None;
    let mut state = None;
    let mut cache_bytes = DEFAULT_CACHE_BYTES;
    let mut index = 0;
    while index < args.len() {
        let option = &args[index];
        index += 1;
        let value = args
            .get(index)
            .ok_or_else(|| format!("missing value for {option}"))?;
        index += 1;
        match option.as_str() {
            "--source" => source = Some(PathBuf::from(value)),
            "--mountpoint" => mountpoint = Some(PathBuf::from(value)),
            "--state" => state = Some(PathBuf::from(value)),
            "--cache-bytes" => {
                cache_bytes = value
                    .parse()
                    .ok()
                    .filter(|size| *size > 0)
                    .ok_or_else(|| "--cache-bytes must be a positive integer".to_string())?
            }
            _ => return Err(format!("unknown option: {option}")),
        }
    }
    Ok(Config {
        source: source.ok_or_else(usage)?,
        mountpoint: mountpoint.ok_or_else(usage)?,
        state,
        cache_bytes,
    })
}

fn usage() -> String {
    "usage: oxdirtest --source DIR --mountpoint DIR [--state DIR] [--cache-bytes N]".into()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> PathBuf {
        let root = std::env::temp_dir().join(format!(
            "oxdirtest-test-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = fs::remove_dir_all(&root);
        fs::create_dir_all(root.join("one/nested")).unwrap();
        fs::create_dir_all(root.join("two")).unwrap();
        fs::write(root.join("one/a.txt"), b"a").unwrap();
        fs::write(root.join("one/nested/b.txt"), b"bee").unwrap();
        fs::write(root.join("two/c.txt"), b"see").unwrap();
        root
    }

    #[test]
    fn indexes_only_selected_directories() {
        let root = fixture();
        let indexed = index_files(&root, &[PathBuf::from("one")]).unwrap();
        let paths: Vec<_> = indexed
            .entries
            .iter()
            .map(|entry| entry.path.as_str())
            .collect();
        assert_eq!(paths, ["one/a.txt", "one/nested/b.txt"]);
        assert_eq!(indexed.files, 2);
        assert_eq!(indexed.bytes, 4);
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn rejects_parent_and_absolute_selections() {
        assert!(safe_relative(Path::new("../secret")).is_err());
        assert!(safe_relative(Path::new("/tmp/secret")).is_err());
        assert!(safe_relative(Path::new("ok/nested")).is_ok());
    }

    #[test]
    fn local_source_detects_changed_files() {
        let root = fixture();
        let path = root.join("one/a.txt");
        let metadata = fs::metadata(&path).unwrap();
        let expected = SourceFile {
            path: path.clone(),
            len: metadata.len(),
            modified: metadata.modified().ok(),
        };
        fs::write(&path, b"changed length").unwrap();
        assert!(verify_metadata(&expected, &fs::metadata(path).unwrap()).is_err());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn selected_files_flow_through_workspace_and_clear() {
        let root = fixture();
        let indexed = index_files(&root, &[PathBuf::from("one")]).unwrap();
        let source = Arc::new(LocalSource::default());
        source.replace(indexed.sources).unwrap();
        let workspace = Workspace::open_with_config(
            &root.join("state"),
            source,
            CacheConfig {
                max_bytes: 1024 * 1024,
            },
        )
        .unwrap();
        workspace
            .apply(Manifest {
                session_id: "human-selection".into(),
                generation: 1,
                entries: indexed.entries,
            })
            .unwrap();
        let inode = workspace
            .snapshot()
            .by_path("one/nested/b.txt")
            .unwrap()
            .inode;
        assert_eq!(
            workspace.open_inode(inode).unwrap().read(0, 16).unwrap(),
            b"bee"
        );

        workspace
            .apply(Manifest {
                session_id: "human-selection".into(),
                generation: 2,
                entries: Vec::new(),
            })
            .unwrap();
        assert!(workspace.snapshot().by_path("one/nested/b.txt").is_none());
        drop(workspace);
        fs::remove_dir_all(root).unwrap();
    }
}

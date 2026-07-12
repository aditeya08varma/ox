use crate::content::{ContentRef, ContentSource, FetchError};
use crate::sha256::Sha256;
use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

pub const DEFAULT_CACHE_MAX_BYTES: u64 = 1024 * 1024 * 1024;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct CacheConfig {
    pub max_bytes: u64,
}

impl Default for CacheConfig {
    fn default() -> Self {
        Self {
            max_bytes: DEFAULT_CACHE_MAX_BYTES,
        }
    }
}

#[derive(Clone, Debug)]
pub struct ResidentContent {
    pub path: PathBuf,
    pub size: u64,
}

#[derive(Clone, Debug)]
struct Record {
    size: u64,
    access: u64,
}

#[derive(Default)]
struct State {
    records: BTreeMap<String, Record>,
    reserved: BTreeMap<String, u64>,
    clock: u64,
}

pub struct ContentCache {
    root: PathBuf,
    source: Arc<dyn ContentSource>,
    config: CacheConfig,
    state: Mutex<State>,
    validated: Mutex<BTreeSet<String>>,
}

impl ContentCache {
    pub fn open(
        root: impl Into<PathBuf>,
        source: Arc<dyn ContentSource>,
        config: CacheConfig,
    ) -> io::Result<Self> {
        if config.max_bytes == 0 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "cache max_bytes must be positive",
            ));
        }
        let root = root.into();
        fs::create_dir_all(root.join("objects"))?;
        fs::create_dir_all(root.join("tmp"))?;
        let cache = Self {
            root,
            source,
            config,
            state: Mutex::new(State::default()),
            validated: Mutex::new(BTreeSet::new()),
        };
        cache.remove_orphan_temps()?;
        cache.load_and_reconcile()?;
        Ok(cache)
    }

    pub fn capacity(&self) -> u64 {
        self.config.max_bytes
    }
    pub fn used(&self) -> u64 {
        self.state
            .lock()
            .map(|s| s.records.values().map(|r| r.size).sum())
            .unwrap_or(self.config.max_bytes)
    }
    pub fn remaining(&self) -> u64 {
        self.config.max_bytes.saturating_sub(self.used())
    }

    fn key(&self, r: &ContentRef) -> String {
        let mut h = Sha256::new();
        h.update(r.tenant.as_bytes());
        format!("{}/{}", h.hex_digest(), r.storage_key())
    }
    fn object_path(&self, r: &ContentRef) -> PathBuf {
        self.root.join("objects").join(self.key(r))
    }

    pub fn resident(&self, r: &ContentRef) -> io::Result<Option<ResidentContent>> {
        let key = self.key(r);
        let path = self.object_path(r);
        match fs::metadata(&path) {
            Ok(m) if m.is_file() && m.len() == r.size => {
                let valid = self
                    .validated
                    .lock()
                    .map_err(|_| io::Error::other("cache validation lock poisoned"))?
                    .contains(&key);
                if !valid {
                    if r.algorithm != "sha256" || hash_file(&path)? != r.digest.to_ascii_lowercase()
                    {
                        let _ = fs::remove_file(&path);
                        self.remove_record(&key)?;
                        return Ok(None);
                    }
                    self.validated
                        .lock()
                        .map_err(|_| io::Error::other("cache validation lock poisoned"))?
                        .insert(key.clone());
                }
                self.ensure_record(&key, r.size)?;
                Ok(Some(ResidentContent { path, size: r.size }))
            }
            Ok(_) => {
                let _ = fs::remove_file(path);
                self.remove_record(&key)?;
                Ok(None)
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                self.remove_record(&key)?;
                Ok(None)
            }
            Err(e) => Err(e),
        }
    }

    pub fn materialize(
        &self,
        r: &ContentRef,
        protected: &BTreeSet<String>,
    ) -> Result<ResidentContent, FetchError> {
        if let Some(v) = self.resident(r)? {
            return Ok(v);
        }
        let key = self.key(r);
        self.reserve(&key, r.size, protected)?;
        let result = self.fetch_reserved(r, &key);
        if result.is_err() {
            let _ = self.release(&key);
        }
        result
    }

    fn reserve(&self, key: &str, size: u64, protected: &BTreeSet<String>) -> io::Result<()> {
        if size > self.config.max_bytes {
            return Err(io::Error::new(
                io::ErrorKind::StorageFull,
                "object exceeds cache limit",
            ));
        }
        let mut state = self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))?;
        if state.records.contains_key(key) || state.reserved.contains_key(key) {
            return Ok(());
        }
        while total(&state).saturating_add(size) > self.config.max_bytes {
            let victim = state
                .records
                .iter()
                .filter(|(k, _)| !protected.contains(*k) && !state.reserved.contains_key(*k))
                .min_by(|a, b| (a.1.access, a.0).cmp(&(b.1.access, b.0)))
                .map(|(k, _)| k.clone());
            let Some(victim) = victim else {
                return Err(io::Error::new(
                    io::ErrorKind::StorageFull,
                    "cache limit reached",
                ));
            };
            fs::remove_file(self.root.join("objects").join(&victim))?;
            state.records.remove(&victim);
            self.validated
                .lock()
                .map_err(|_| io::Error::other("cache validation lock poisoned"))?
                .remove(&victim);
        }
        state.reserved.insert(key.to_owned(), size);
        self.persist_locked(&state)
    }

    fn fetch_reserved(&self, r: &ContentRef, key: &str) -> Result<ResidentContent, FetchError> {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let temp_path = self
            .root
            .join("tmp")
            .join(format!("{}-{nonce}.part", std::process::id()));
        let final_path = self.object_path(r);
        fs::create_dir_all(final_path.parent().unwrap())?;
        let temp = OpenOptions::new()
            .create_new(true)
            .write(true)
            .open(&temp_path)?;
        let mut verified = VerifyingWriter::new(temp, r.size);
        if let Err(e) = self.source.fetch(r, &mut verified) {
            let _ = fs::remove_file(&temp_path);
            return Err(e);
        }
        let (mut file, size, digest) = match verified.finish() {
            Ok(v) => v,
            Err(e) => {
                let _ = fs::remove_file(&temp_path);
                return Err(e.into());
            }
        };
        if size != r.size {
            drop(file);
            let _ = fs::remove_file(&temp_path);
            return Err(FetchError::WrongSize {
                expected: r.size,
                actual: size,
            });
        }
        if r.algorithm != "sha256" || !digest.eq_ignore_ascii_case(&r.digest) {
            drop(file);
            let _ = fs::remove_file(&temp_path);
            return Err(FetchError::WrongDigest {
                expected: r.digest.clone(),
                actual: digest,
            });
        }
        file.flush()?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temp_path, &final_path)?;
        sync_directory(final_path.parent().unwrap())?;
        let mut state = self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))?;
        state.reserved.remove(key);
        state.clock = state.clock.saturating_add(1);
        let access = state.clock;
        state
            .records
            .insert(key.to_owned(), Record { size, access });
        self.persist_locked(&state)?;
        self.validated
            .lock()
            .map_err(|_| io::Error::other("cache validation lock poisoned"))?
            .insert(key.to_owned());
        Ok(ResidentContent {
            path: final_path,
            size,
        })
    }

    pub fn open_file(&self, r: &ContentRef) -> io::Result<File> {
        let resident = self
            .resident(r)?
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "content is not resident"))?;
        let key = self.key(r);
        let mut state = self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))?;
        state.clock = state.clock.saturating_add(1);
        let access = state.clock;
        if let Some(record) = state.records.get_mut(&key) {
            record.access = access;
        }
        self.persist_locked(&state)?;
        File::open(resident.path)
    }

    pub fn storage_key(&self, r: &ContentRef) -> String {
        self.key(r)
    }
    fn release(&self, key: &str) -> io::Result<()> {
        let mut s = self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))?;
        s.reserved.remove(key);
        self.persist_locked(&s)
    }
    fn ensure_record(&self, key: &str, size: u64) -> io::Result<()> {
        let mut s = self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))?;
        if !s.records.contains_key(key) {
            s.clock += 1;
            let access = s.clock;
            s.records.insert(key.into(), Record { size, access });
            self.persist_locked(&s)?;
        }
        Ok(())
    }
    fn remove_record(&self, key: &str) -> io::Result<()> {
        let mut s = self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))?;
        if s.records.remove(key).is_some() {
            self.persist_locked(&s)?;
        }
        Ok(())
    }
    fn remove_orphan_temps(&self) -> io::Result<()> {
        for e in fs::read_dir(self.root.join("tmp"))? {
            let e = e?;
            if e.file_type()?.is_file() {
                fs::remove_file(e.path())?;
            }
        }
        Ok(())
    }
    fn load_and_reconcile(&self) -> io::Result<()> {
        let mut state = State::default();
        let path = self.root.join("metadata.v1");
        if let Ok(file) = File::open(&path) {
            for line in io::BufRead::lines(io::BufReader::new(file)) {
                let line = line?;
                let f: Vec<_> = line.split('\t').collect();
                if f.len() == 3
                    && let (Ok(size), Ok(access)) = (f[1].parse(), f[2].parse())
                {
                    let p = self.root.join("objects").join(f[0]);
                    if fs::metadata(p).is_ok_and(|m| m.is_file() && m.len() == size) {
                        state.clock = state.clock.max(access);
                        state.records.insert(f[0].into(), Record { size, access });
                    }
                }
            }
        }
        for tenant in read_dirs(&self.root.join("objects"))? {
            for object in read_files(&tenant)? {
                let rel = object
                    .strip_prefix(self.root.join("objects"))
                    .unwrap()
                    .to_string_lossy()
                    .into_owned();
                if !state.records.contains_key(&rel) {
                    state.clock += 1;
                    state.records.insert(
                        rel,
                        Record {
                            size: fs::metadata(object)?.len(),
                            access: state.clock,
                        },
                    );
                }
            }
        }
        *self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))? = state;
        let s = self
            .state
            .lock()
            .map_err(|_| io::Error::other("cache state lock poisoned"))?;
        self.persist_locked(&s)
    }
    fn persist_locked(&self, s: &State) -> io::Result<()> {
        let path = self.root.join("metadata.v1");
        let tmp = self
            .root
            .join(format!("metadata.tmp-{}", std::process::id()));
        let mut f = File::create(&tmp)?;
        for (k, r) in &s.records {
            writeln!(f, "{k}\t{}\t{}", r.size, r.access)?;
        }
        f.flush()?;
        f.sync_all()?;
        fs::rename(tmp, path)
    }
}

fn total(s: &State) -> u64 {
    s.records
        .values()
        .map(|r| r.size)
        .sum::<u64>()
        .saturating_add(s.reserved.values().sum())
}
fn read_dirs(p: &Path) -> io::Result<Vec<PathBuf>> {
    Ok(fs::read_dir(p)?
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_ok_and(|t| t.is_dir()))
        .map(|e| e.path())
        .collect())
}
fn read_files(p: &Path) -> io::Result<Vec<PathBuf>> {
    Ok(fs::read_dir(p)?
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_ok_and(|t| t.is_file()))
        .map(|e| e.path())
        .collect())
}
fn hash_file(path: &Path) -> io::Result<String> {
    let mut f = File::open(path)?;
    let mut h = Sha256::new();
    let mut b = [0; 65536];
    loop {
        let n = f.read(&mut b)?;
        if n == 0 {
            break;
        }
        h.update(&b[..n]);
    }
    Ok(h.hex_digest())
}
struct VerifyingWriter<W> {
    inner: W,
    hash: Sha256,
    size: u64,
    limit: u64,
}
impl<W> VerifyingWriter<W> {
    fn new(inner: W, limit: u64) -> Self {
        Self {
            inner,
            hash: Sha256::new(),
            size: 0,
            limit,
        }
    }
    fn finish(self) -> io::Result<(W, u64, String)> {
        Ok((self.inner, self.size, self.hash.hex_digest()))
    }
}
impl<W: Write> Write for VerifyingWriter<W> {
    fn write(&mut self, b: &[u8]) -> io::Result<usize> {
        let remaining = self.limit.saturating_sub(self.size);
        if b.len() as u64 > remaining {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "source exceeded declared size",
            ));
        }
        let n = self.inner.write(b)?;
        self.hash.update(&b[..n]);
        self.size += n as u64;
        Ok(n)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.inner.flush()
    }
}
fn sync_directory(path: &Path) -> io::Result<()> {
    File::open(path)?.sync_all()
}
pub fn read_range(file: &File, offset: u64, count: usize) -> io::Result<Vec<u8>> {
    let mut f = file.try_clone()?;
    use std::io::{Seek, SeekFrom};
    f.seek(SeekFrom::Start(offset))?;
    let mut out = vec![0; count];
    let n = f.read(&mut out)?;
    out.truncate(n);
    Ok(out)
}

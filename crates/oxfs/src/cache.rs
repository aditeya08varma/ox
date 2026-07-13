use crate::cache_catalog::{Catalog, Reservation};
use crate::content::{ContentRef, ContentSource, FetchError};
use crate::sha256::Sha256;
use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, BufWriter, Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Instant;

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

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct CacheTelemetrySnapshot {
    pub catalog_lookups: u64,
    pub resident_hits: u64,
    pub resident_misses: u64,
    pub opened_objects: u64,
    pub opened_bytes: u64,
    pub fetched_objects: u64,
    pub fetched_bytes: u64,
    pub refetched_objects: u64,
    pub refetched_bytes: u64,
    pub evicted_objects: u64,
    pub evicted_bytes: u64,
    pub evict_blocked: u64,
    pub catalog_transactions: u64,
    pub catalog_commit_us: u64,
    pub object_sync_us: u64,
    pub directory_syncs: u64,
    pub directory_sync_us: u64,
    pub resident_objects: u64,
    pub resident_bytes: u64,
    pub pending_objects: u64,
    pub catalog_bytes: u64,
    pub wal_bytes: u64,
}

#[derive(Default)]
struct Telemetry {
    catalog_lookups: AtomicU64,
    resident_hits: AtomicU64,
    resident_misses: AtomicU64,
    opened_objects: AtomicU64,
    opened_bytes: AtomicU64,
    fetched_objects: AtomicU64,
    fetched_bytes: AtomicU64,
    refetched_objects: AtomicU64,
    refetched_bytes: AtomicU64,
    evicted_objects: AtomicU64,
    evicted_bytes: AtomicU64,
    evict_blocked: AtomicU64,
    catalog_transactions: AtomicU64,
    catalog_commit_us: AtomicU64,
    object_sync_us: AtomicU64,
    directory_syncs: AtomicU64,
    directory_sync_us: AtomicU64,
}

pub struct ContentCache {
    root: PathBuf,
    source: Arc<dyn ContentSource>,
    config: CacheConfig,
    catalog: Mutex<Catalog>,
    batch_directories: Mutex<Option<BTreeSet<PathBuf>>>,
    validated: Mutex<BTreeSet<String>>,
    temp_nonce: AtomicU64,
    telemetry: Telemetry,
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
        let catalog = Catalog::open(&root.join("catalog.sqlite"))?;
        let cache = Self {
            root,
            source,
            config,
            catalog: Mutex::new(catalog),
            batch_directories: Mutex::new(None),
            validated: Mutex::new(BTreeSet::new()),
            temp_nonce: AtomicU64::new(0),
            telemetry: Telemetry::default(),
        };
        cache.remove_orphan_temps()?;
        cache.migrate_metadata_v1()?;
        cache.recover_pending()?;
        Ok(cache)
    }

    pub fn capacity(&self) -> u64 {
        self.config.max_bytes
    }
    pub fn used(&self) -> u64 {
        self.catalog
            .lock()
            .ok()
            .and_then(|catalog| catalog.gauges().ok())
            .map(|gauges| gauges.resident_bytes)
            .unwrap_or(self.config.max_bytes)
    }
    pub fn remaining(&self) -> u64 {
        self.config.max_bytes.saturating_sub(self.used())
    }

    /// Cumulative (objects evicted, reservations blocked by all-pinned).
    pub fn eviction_counters(&self) -> (u64, u64) {
        (
            self.telemetry.evicted_objects.load(Ordering::Relaxed),
            self.telemetry.evict_blocked.load(Ordering::Relaxed),
        )
    }

    pub fn telemetry(&self) -> io::Result<CacheTelemetrySnapshot> {
        let gauges = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .gauges()?;
        Ok(CacheTelemetrySnapshot {
            catalog_lookups: self.telemetry.catalog_lookups.load(Ordering::Relaxed),
            resident_hits: self.telemetry.resident_hits.load(Ordering::Relaxed),
            resident_misses: self.telemetry.resident_misses.load(Ordering::Relaxed),
            opened_objects: self.telemetry.opened_objects.load(Ordering::Relaxed),
            opened_bytes: self.telemetry.opened_bytes.load(Ordering::Relaxed),
            fetched_objects: self.telemetry.fetched_objects.load(Ordering::Relaxed),
            fetched_bytes: self.telemetry.fetched_bytes.load(Ordering::Relaxed),
            refetched_objects: self.telemetry.refetched_objects.load(Ordering::Relaxed),
            refetched_bytes: self.telemetry.refetched_bytes.load(Ordering::Relaxed),
            evicted_objects: self.telemetry.evicted_objects.load(Ordering::Relaxed),
            evicted_bytes: self.telemetry.evicted_bytes.load(Ordering::Relaxed),
            evict_blocked: self.telemetry.evict_blocked.load(Ordering::Relaxed),
            catalog_transactions: self.telemetry.catalog_transactions.load(Ordering::Relaxed),
            catalog_commit_us: self.telemetry.catalog_commit_us.load(Ordering::Relaxed),
            object_sync_us: self.telemetry.object_sync_us.load(Ordering::Relaxed),
            directory_syncs: self.telemetry.directory_syncs.load(Ordering::Relaxed),
            directory_sync_us: self.telemetry.directory_sync_us.load(Ordering::Relaxed),
            resident_objects: gauges.resident_objects,
            resident_bytes: gauges.resident_bytes,
            pending_objects: gauges.pending_objects,
            catalog_bytes: file_len(&self.root.join("catalog.sqlite")),
            wal_bytes: file_len(&self.root.join("catalog.sqlite-wal")),
        })
    }

    fn key(&self, r: &ContentRef) -> String {
        let mut h = Sha256::new();
        h.update(r.tenant.as_bytes());
        format!("{}/{}", h.hex_digest(), r.storage_key())
    }
    fn object_path(&self, r: &ContentRef) -> PathBuf {
        self.root.join("objects").join(self.key(r))
    }

    fn key_path(&self, key: &str) -> PathBuf {
        self.root.join("objects").join(key)
    }

    pub fn resident(&self, r: &ContentRef) -> io::Result<Option<ResidentContent>> {
        let key = self.key(r);
        let path = self.object_path(r);
        self.telemetry
            .catalog_lookups
            .fetch_add(1, Ordering::Relaxed);
        let catalog_resident = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .is_resident(&key, r.size)?;
        if !catalog_resident {
            self.telemetry
                .resident_misses
                .fetch_add(1, Ordering::Relaxed);
            return Ok(None);
        }
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
                        self.mark_missing(&key)?;
                        return Ok(None);
                    }
                    self.validated
                        .lock()
                        .map_err(|_| io::Error::other("cache validation lock poisoned"))?
                        .insert(key.clone());
                }
                self.telemetry.resident_hits.fetch_add(1, Ordering::Relaxed);
                Ok(Some(ResidentContent { path, size: r.size }))
            }
            Ok(_) => {
                let _ = fs::remove_file(path);
                self.mark_missing(&key)?;
                self.telemetry
                    .resident_misses
                    .fetch_add(1, Ordering::Relaxed);
                Ok(None)
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                self.mark_missing(&key)?;
                self.telemetry
                    .resident_misses
                    .fetch_add(1, Ordering::Relaxed);
                Ok(None)
            }
            Err(e) => Err(e),
        }
    }

    /// Start one manifest-sized catalog transaction. The recovery journal is
    /// fsynced first, so a crash only requires checking this bounded key set.
    pub fn begin_batch(
        &self,
        admissions: &[ContentRef],
        protected: &BTreeMap<String, u64>,
    ) -> io::Result<()> {
        let journal_path = self.root.join("active-apply.v1");
        let mut journal = BufWriter::new(File::create(&journal_path)?);
        for reference in admissions {
            writeln!(journal, "{}", self.key(reference))?;
        }
        journal.flush()?;
        journal.get_ref().sync_all()?;
        sync_directory(&self.root)?;

        *self
            .batch_directories
            .lock()
            .map_err(|_| io::Error::other("cache directory batch lock poisoned"))? =
            Some(BTreeSet::new());
        let mut catalog = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?;
        if let Err(error) = catalog.begin_batch(protected.keys().map(String::as_str)) {
            if let Ok(mut directories) = self.batch_directories.lock() {
                *directories = None;
            }
            let _ = fs::remove_file(journal_path);
            return Err(error);
        }
        self.telemetry
            .catalog_transactions
            .fetch_add(1, Ordering::Relaxed);
        Ok(())
    }

    pub fn commit_batch(&self) -> io::Result<()> {
        {
            let directories = self
                .batch_directories
                .lock()
                .map_err(|_| io::Error::other("cache directory batch lock poisoned"))?;
            let directories = directories
                .as_ref()
                .ok_or_else(|| io::Error::other("cache directory batch not active"))?;
            let started = Instant::now();
            for directory in directories {
                sync_directory(directory)?;
            }
            self.telemetry
                .directory_syncs
                .fetch_add(directories.len() as u64, Ordering::Relaxed);
            self.telemetry.directory_sync_us.fetch_add(
                saturating_u64(started.elapsed().as_micros()),
                Ordering::Relaxed,
            );
        }
        let commit_us = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .commit()?;
        self.telemetry
            .catalog_commit_us
            .fetch_add(saturating_u64(commit_us), Ordering::Relaxed);
        *self
            .batch_directories
            .lock()
            .map_err(|_| io::Error::other("cache directory batch lock poisoned"))? = None;
        remove_if_exists(&self.root.join("active-apply.v1"))
    }

    pub fn rollback_batch(&self) -> io::Result<()> {
        self.catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .rollback()?;
        let recovered = self.recover_apply_journal();
        *self
            .batch_directories
            .lock()
            .map_err(|_| io::Error::other("cache directory batch lock poisoned"))? = None;
        recovered
    }

    pub fn materialize(
        &self,
        r: &ContentRef,
        protected: &BTreeMap<String, u64>,
    ) -> Result<ResidentContent, FetchError> {
        if let Some(v) = self.resident(r)? {
            return Ok(v);
        }
        self.materialize_missing(r, protected)
    }

    /// Materialize content already proven absent by the caller's current
    /// reconciliation pass, avoiding a duplicate catalog and metadata probe.
    pub(crate) fn materialize_missing(
        &self,
        r: &ContentRef,
        protected: &BTreeMap<String, u64>,
    ) -> Result<ResidentContent, FetchError> {
        let key = self.key(r);
        let refetch = self.reserve(&key, r.size, protected)?;
        let result = self.fetch_reserved(r, &key);
        if result.is_err() {
            let _ = self.release(&key);
        } else if refetch {
            self.telemetry
                .refetched_objects
                .fetch_add(1, Ordering::Relaxed);
            self.telemetry
                .refetched_bytes
                .fetch_add(r.size, Ordering::Relaxed);
        }
        result
    }

    /// Reserve in manifest order, then fetch and durably write independent
    /// immutable objects concurrently. All workers finish before publication.
    pub(crate) fn materialize_missing_batch(
        &self,
        references: &[ContentRef],
        protected: &BTreeMap<String, u64>,
    ) -> Result<Vec<String>, FetchError> {
        let mut reserved = Vec::with_capacity(references.len());
        for reference in references {
            let key = self.key(reference);
            let refetch = match self.reserve(&key, reference.size, protected) {
                Ok(refetch) => refetch,
                Err(error) if error.kind() == io::ErrorKind::StorageFull => {
                    break;
                }
                Err(error) => return Err(error.into()),
            };
            reserved.push((reference, key, refetch));
        }
        if reserved.is_empty() {
            return Ok(Vec::new());
        }

        let next = AtomicUsize::new(0);
        let results = Mutex::new(Vec::with_capacity(reserved.len()));
        let workers = std::thread::available_parallelism()
            .map(usize::from)
            .unwrap_or(1)
            .min(8)
            .min(reserved.len());
        std::thread::scope(|scope| {
            for _ in 0..workers {
                scope.spawn(|| {
                    loop {
                        let index = next.fetch_add(1, Ordering::Relaxed);
                        let Some((reference, key, _)) = reserved.get(index) else {
                            break;
                        };
                        let result = self.fetch_reserved(reference, key).map(|_| ());
                        if result.is_err() {
                            let _ = self.release(key);
                        }
                        results
                            .lock()
                            .expect("cache batch result lock poisoned")
                            .push((index, result));
                    }
                });
            }
        });

        let mut results = results
            .into_inner()
            .map_err(|_| io::Error::other("cache batch result lock poisoned"))?;
        results.sort_unstable_by_key(|(index, _)| *index);
        let mut materialized = Vec::with_capacity(results.len());
        for (index, result) in results {
            result?;
            let (_, key, refetch) = &reserved[index];
            if *refetch {
                self.telemetry
                    .refetched_objects
                    .fetch_add(1, Ordering::Relaxed);
                self.telemetry
                    .refetched_bytes
                    .fetch_add(reserved[index].0.size, Ordering::Relaxed);
            }
            materialized.push(key.clone());
        }
        Ok(materialized)
    }

    fn reserve(&self, key: &str, size: u64, protected: &BTreeMap<String, u64>) -> io::Result<bool> {
        if size > self.config.max_bytes {
            return Err(io::Error::new(
                io::ErrorKind::StorageFull,
                "object exceeds cache limit",
            ));
        }
        let mut catalog = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?;
        let reservation = catalog.reserve(key, size, self.config.max_bytes);
        let (reservation, victims) = match reservation {
            Ok(value) => value,
            Err(error) if error.kind() == io::ErrorKind::StorageFull => {
                self.telemetry.evict_blocked.fetch_add(1, Ordering::Relaxed);
                eprintln!(
                    "level=WARN action=evict_blocked reason=all_pinned need={size} occupancy={} capacity={} protected={}",
                    catalog.gauges()?.resident_bytes,
                    self.config.max_bytes,
                    protected.len(),
                );
                return Err(error);
            }
            Err(error) => return Err(error),
        };
        for victim in victims {
            remove_if_exists(&self.key_path(&victim.key))?;
            self.validated
                .lock()
                .map_err(|_| io::Error::other("cache validation lock poisoned"))?
                .remove(&victim.key);
            self.telemetry
                .evicted_objects
                .fetch_add(1, Ordering::Relaxed);
            self.telemetry
                .evicted_bytes
                .fetch_add(victim.size, Ordering::Relaxed);
            if std::env::var_os("OXFS_CACHE_LOG").is_some() {
                eprintln!(
                    "level=INFO action=evict key={} size={} access={} occupancy={} capacity={}",
                    victim.key,
                    victim.size,
                    victim.access,
                    catalog.gauges()?.resident_bytes,
                    self.config.max_bytes,
                );
            }
        }
        Ok(reservation == Reservation::Refetch)
    }

    fn fetch_reserved(&self, r: &ContentRef, key: &str) -> Result<ResidentContent, FetchError> {
        let nonce = self.temp_nonce.fetch_add(1, Ordering::Relaxed);
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
        let sync_started = Instant::now();
        file.sync_all()?;
        self.telemetry.object_sync_us.fetch_add(
            saturating_u64(sync_started.elapsed().as_micros()),
            Ordering::Relaxed,
        );
        drop(file);
        fs::rename(&temp_path, &final_path)?;
        self.sync_or_defer_directory(final_path.parent().unwrap())?;
        self.catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .finish(key, size)?;
        self.validated
            .lock()
            .map_err(|_| io::Error::other("cache validation lock poisoned"))?
            .insert(key.to_owned());
        self.telemetry
            .fetched_objects
            .fetch_add(1, Ordering::Relaxed);
        self.telemetry
            .fetched_bytes
            .fetch_add(size, Ordering::Relaxed);
        Ok(ResidentContent {
            path: final_path,
            size,
        })
    }

    fn sync_or_defer_directory(&self, directory: &Path) -> io::Result<()> {
        let mut batch = self
            .batch_directories
            .lock()
            .map_err(|_| io::Error::other("cache directory batch lock poisoned"))?;
        if let Some(directories) = batch.as_mut() {
            directories.insert(directory.to_owned());
            return Ok(());
        }
        let started = Instant::now();
        sync_directory(directory)?;
        self.telemetry
            .directory_syncs
            .fetch_add(1, Ordering::Relaxed);
        self.telemetry.directory_sync_us.fetch_add(
            saturating_u64(started.elapsed().as_micros()),
            Ordering::Relaxed,
        );
        Ok(())
    }

    pub fn open_file(&self, r: &ContentRef) -> io::Result<File> {
        let resident = self
            .resident(r)?
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "content is not resident"))?;
        self.telemetry
            .opened_objects
            .fetch_add(1, Ordering::Relaxed);
        self.telemetry
            .opened_bytes
            .fetch_add(r.size, Ordering::Relaxed);
        File::open(resident.path)
    }

    pub fn storage_key(&self, r: &ContentRef) -> String {
        self.key(r)
    }
    fn release(&self, key: &str) -> io::Result<()> {
        self.catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .release(key)
    }

    fn mark_missing(&self, key: &str) -> io::Result<()> {
        self.catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .mark_missing(key)
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

    fn migrate_metadata_v1(&self) -> io::Result<()> {
        let path = self.root.join("metadata.v1");
        let file = match File::open(&path) {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(error) => return Err(error),
        };
        let mut catalog = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?;
        for line in BufReader::new(file).lines() {
            let line = line?;
            let fields: Vec<_> = line.split('\t').collect();
            if fields.len() != 3 {
                continue;
            }
            let (Ok(size), Ok(access)) = (fields[1].parse(), fields[2].parse()) else {
                continue;
            };
            let object = self.key_path(fields[0]);
            if fs::metadata(object)
                .is_ok_and(|metadata| metadata.is_file() && metadata.len() == size)
            {
                catalog.import_resident(fields[0], size, access)?;
            }
        }
        drop(catalog);
        remove_if_exists(&path)
    }

    fn recover_pending(&self) -> io::Result<()> {
        self.recover_apply_journal()?;
        let mut catalog = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?;
        let pending = catalog.pending_keys()?;
        for key in &pending {
            remove_if_exists(&self.key_path(key))?;
        }
        catalog.clear_pending()
    }

    fn recover_apply_journal(&self) -> io::Result<()> {
        let path = self.root.join("active-apply.v1");
        let file = match File::open(&path) {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(error) => return Err(error),
        };
        let catalog = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?;
        for line in BufReader::new(file).lines() {
            let key = line?;
            if !catalog.is_resident(&key, file_len(&self.key_path(&key)))? {
                remove_if_exists(&self.key_path(&key))?;
            }
        }
        drop(catalog);
        remove_if_exists(&path)
    }
}

fn file_len(path: &Path) -> u64 {
    fs::metadata(path)
        .map(|metadata| metadata.len())
        .unwrap_or(0)
}

fn remove_if_exists(path: &Path) -> io::Result<()> {
    match fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error),
    }
}

fn saturating_u64(value: u128) -> u64 {
    u64::try_from(value).unwrap_or(u64::MAX)
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

use crate::cache_catalog::{Catalog, Reservation, Victim};
use crate::cache_policy::EvictionOrder;
use crate::content::{ContentRef, ContentSource, FetchError};
use crate::sha256::Sha256;
use std::collections::BTreeSet;
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, BufWriter, Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Instant;

pub const DEFAULT_CACHE_MAX_BYTES: u64 = 1024 * 1024 * 1024;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct CacheConfig {
    pub max_bytes: u64,
    pub eviction: EvictionOrder,
}

impl Default for CacheConfig {
    fn default() -> Self {
        Self {
            max_bytes: DEFAULT_CACHE_MAX_BYTES,
            eviction: EvictionOrder::default(),
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
    // Victims chosen during a batch, unlinked only AFTER the catalog commit
    // that marks them evicted is durable. Deleting them at reserve() time (while
    // the marking transaction is still open) meant a crash before commit rolled
    // the rows back to resident while their files were already gone — permanent
    // phantom-resident accounting. `None` outside a batch: the non-batch path
    // commits per op in SQLite autocommit and unlinks inline.
    batch_victims: Mutex<Option<Vec<Victim>>>,
    validated: Mutex<BTreeSet<String>>,
    temp_nonce: AtomicU64,
    fail_next_commit: AtomicBool,
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
        let catalog = Catalog::open(&root.join("catalog.sqlite"), config.eviction)?;
        let cache = Self {
            root,
            source,
            config,
            catalog: Mutex::new(catalog),
            batch_directories: Mutex::new(None),
            batch_victims: Mutex::new(None),
            validated: Mutex::new(BTreeSet::new()),
            temp_nonce: AtomicU64::new(0),
            fail_next_commit: AtomicBool::new(false),
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

    /// Cumulative (objects evicted, reservations blocked with no evictable object).
    pub fn eviction_counters(&self) -> (u64, u64) {
        (
            self.telemetry.evicted_objects.load(Ordering::Relaxed),
            self.telemetry.evict_blocked.load(Ordering::Relaxed),
        )
    }

    /// Resident keys in deterministic order — differential-oracle ground truth.
    /// O(rows) scan; test/introspection only, never on the hot path.
    pub fn resident_keys(&self) -> io::Result<Vec<String>> {
        self.catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .resident_keys()
    }

    /// Full resident+scalar snapshot for the differential oracle.
    pub fn snapshot_state(&self) -> io::Result<crate::cache_catalog::CatalogSnapshot> {
        self.catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .snapshot_state()
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
    pub fn begin_batch(&self, admissions: &[ContentRef]) -> io::Result<()> {
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
        *self
            .batch_victims
            .lock()
            .map_err(|_| io::Error::other("cache victim batch lock poisoned"))? = Some(Vec::new());
        let mut catalog = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?;
        if let Err(error) = catalog.begin_batch() {
            if let Ok(mut directories) = self.batch_directories.lock() {
                *directories = None;
            }
            if let Ok(mut victims) = self.batch_victims.lock() {
                *victims = None;
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
        if self.fail_next_commit.swap(false, Ordering::SeqCst) {
            return Err(io::Error::other("injected cache commit failure"));
        }
        // Take the deferred victims and record their keys in the apply journal
        // BEFORE the commit makes their evicted state durable. Recovery removes
        // any journalled key that is not resident after restart, so:
        //   crash before commit -> catalog rolls back to resident, file still
        //     present (unlink is post-commit) -> key is resident -> file kept.
        //   crash after commit  -> catalog is evicted, key is not resident ->
        //     recovery reclaims any file the unlink below did not reach.
        // Either way the catalog and the object tree stay consistent.
        let victims = self
            .batch_victims
            .lock()
            .map_err(|_| io::Error::other("cache victim batch lock poisoned"))?
            .take()
            .unwrap_or_default();
        if !victims.is_empty() {
            let journal_path = self.root.join("active-apply.v1");
            let mut journal = BufWriter::new(
                OpenOptions::new()
                    .append(true)
                    .create(true)
                    .open(&journal_path)?,
            );
            for victim in &victims {
                writeln!(journal, "{}", victim.key)?;
            }
            journal.flush()?;
            journal.get_ref().sync_all()?;
        }
        let commit_us = self
            .catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .commit()?;
        self.telemetry
            .catalog_commit_us
            .fetch_add(saturating_u64(commit_us), Ordering::Relaxed);
        {
            let catalog = self
                .catalog
                .lock()
                .map_err(|_| io::Error::other("cache catalog lock poisoned"))?;
            for victim in &victims {
                self.evict_victim(victim, &catalog)?;
            }
        }
        *self
            .batch_directories
            .lock()
            .map_err(|_| io::Error::other("cache directory batch lock poisoned"))? = None;
        remove_if_exists(&self.root.join("active-apply.v1"))
    }

    /// Arm one deterministic failure immediately before the next batch commit.
    /// Used by the joint test harness to exercise the production rollback path.
    pub fn fail_next_commit(&self) {
        self.fail_next_commit.store(true, Ordering::SeqCst);
    }

    pub fn rollback_batch(&self) -> io::Result<()> {
        // Drop the deferred victims WITHOUT unlinking: the rollback restores
        // their rows to resident, and their files were never deleted, so the two
        // stay in agreement.
        *self
            .batch_victims
            .lock()
            .map_err(|_| io::Error::other("cache victim batch lock poisoned"))? = None;
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

    pub fn materialize(&self, r: &ContentRef) -> Result<ResidentContent, FetchError> {
        if let Some(v) = self.resident(r)? {
            return Ok(v);
        }
        self.materialize_missing(r)
    }

    /// Materialize content already proven absent by the caller's current
    /// reconciliation pass, avoiding a duplicate catalog and metadata probe.
    pub(crate) fn materialize_missing(
        &self,
        r: &ContentRef,
    ) -> Result<ResidentContent, FetchError> {
        let key = self.key(r);
        let refetch = self.reserve(&key, r.size)?;
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
    ) -> Result<Vec<String>, FetchError> {
        let mut reserved = Vec::with_capacity(references.len());
        for reference in references {
            let key = self.key(reference);
            let refetch = match self.reserve(&key, reference.size) {
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

    fn reserve(&self, key: &str, size: u64) -> io::Result<bool> {
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
                    "level=WARN action=evict_blocked reason=no_evictable_object need={size} occupancy={} capacity={}",
                    catalog.gauges()?.resident_bytes,
                    self.config.max_bytes,
                );
                return Err(error);
            }
            Err(error) => return Err(error),
        };
        // In a batch, defer every victim side effect — file unlink, validation
        // drop, telemetry, log — until commit_batch, so the catalog marking and
        // the file deletion become durable together. Outside a batch the marking
        // already committed (autocommit), so evict inline as before.
        let mut deferred = self
            .batch_victims
            .lock()
            .map_err(|_| io::Error::other("cache victim batch lock poisoned"))?;
        if let Some(pending) = deferred.as_mut() {
            pending.extend(victims);
        } else {
            drop(deferred);
            for victim in victims {
                self.evict_victim(&victim, &catalog)?;
            }
        }
        Ok(reservation == Reservation::Refetch)
    }

    /// Physically evict one victim: unlink its object, drop its verified flag,
    /// and record telemetry. The caller guarantees the catalog row is already,
    /// or is about to be, durably marked evicted.
    fn evict_victim(&self, victim: &Victim, catalog: &Catalog) -> io::Result<()> {
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
        Ok(())
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
        let key = self.key(r);
        let resident = self
            .resident(r)?
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "content is not resident"))?;
        self.telemetry
            .opened_objects
            .fetch_add(1, Ordering::Relaxed);
        self.telemetry
            .opened_bytes
            .fetch_add(r.size, Ordering::Relaxed);
        let file = File::open(resident.path)?;
        self.catalog
            .lock()
            .map_err(|_| io::Error::other("cache catalog lock poisoned"))?
            .touch(&key)?;
        Ok(file)
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::content::ContentRef;
    use std::collections::BTreeMap;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};

    struct MemorySource(BTreeMap<String, Vec<u8>>);
    impl ContentSource for MemorySource {
        fn fetch(&self, r: &ContentRef, w: &mut dyn Write) -> Result<(), FetchError> {
            w.write_all(self.0.get(&r.digest).ok_or(FetchError::NotFound)?)?;
            Ok(())
        }
    }

    fn temp() -> PathBuf {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        std::env::temp_dir().join(format!(
            "oxfs-cache-crash-{}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ))
    }

    // A SIGKILL after a victim is chosen but before the apply commits must not
    // leave the catalog claiming the victim is resident while its file is gone.
    // Failure prevented: eviction unlinked the victim inside the still-open
    // marking transaction; a crash before commit rolled the row back to resident
    // while the file stayed deleted, so used()/resident() lied about ~victim-size
    // bytes forever and the recovery journal (admissions only) never healed it.
    #[test]
    fn crash_before_commit_keeps_evicted_victim_resident_and_on_disk() {
        let root = temp();
        let a_bytes = vec![b'A'; 4000];
        let b_bytes = vec![b'B'; 4000];
        let a = ContentRef::for_sha256("t", &a_bytes);
        let b = ContentRef::for_sha256("t", &b_bytes);
        let source = Arc::new(MemorySource(BTreeMap::from([
            (a.digest.clone(), a_bytes.clone()),
            (b.digest.clone(), b_bytes),
        ])));
        // Capacity holds exactly one object, so admitting B must evict A.
        let config = CacheConfig {
            max_bytes: 4000,
            ..CacheConfig::default()
        };
        // Admit A and commit it.
        let cache = ContentCache::open(&root, source.clone(), config).unwrap();
        cache.begin_batch(std::slice::from_ref(&a)).unwrap();
        cache
            .materialize_missing_batch(std::slice::from_ref(&a))
            .unwrap();
        cache.commit_batch().unwrap();
        assert!(cache.resident(&a).unwrap().is_some());
        assert!(cache.object_path(&a).exists());

        // Start admitting B — this evicts A — but simulate a crash by dropping the
        // cache before commit_batch, which rolls back the open catalog transaction.
        cache.begin_batch(std::slice::from_ref(&b)).unwrap();
        cache
            .materialize_missing_batch(std::slice::from_ref(&b))
            .unwrap();
        assert!(
            cache.object_path(&a).exists(),
            "victim file must survive until the eviction commits"
        );
        drop(cache);

        // Restart through the real recovery path.
        let cache = ContentCache::open(&root, source, config).unwrap();
        assert!(
            cache.resident(&a).unwrap().is_some(),
            "A rolled back to resident, so it must still be resident after recovery"
        );
        assert!(
            cache.object_path(&a).exists(),
            "A's object file must exist: the catalog says it is resident"
        );
        assert_eq!(
            cache.used(),
            4000,
            "used() must match the one resident object"
        );
        assert!(
            cache.resident(&b).unwrap().is_none(),
            "B never committed, so it must not be resident"
        );

        drop(cache);
        fs::remove_dir_all(root).unwrap();
    }
}

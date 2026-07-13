use oxfs::mount;
use oxfs::nfs::{NfsServer, ServerConfig};
use oxfs::{
    CacheConfig, ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace,
};
use std::collections::{BTreeMap, BTreeSet};
use std::fmt::Write as _;
use std::fs::{self, File};
use std::io::{self, IsTerminal, Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

const VIRTUAL_FILES: u64 = 1_000_000;
const SMALL_BYTES: usize = 606;
const P99_BYTES: usize = 1_400_000;
const MAX_BYTES: usize = 12_700_000;

#[derive(Clone, Debug)]
struct Config {
    seed: u64,
    generations: u64,
    readers: usize,
    reads_per_reader: usize,
    source_delay_ms: u64,
    artifact: PathBuf,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            seed: 1,
            generations: 12,
            readers: 4,
            reads_per_reader: 40,
            source_delay_ms: 2,
            artifact: PathBuf::from("oxjtest-results.json"),
        }
    }
}

#[derive(Clone, Debug)]
struct ServeConfig {
    seed: u64,
    mountpoint: PathBuf,
    /// Total generations to evolve through, or `None` for `--evolve-forever`.
    generations: Option<u64>,
    /// Number of files in the working set (roughly held constant by churn).
    files: usize,
    /// Percent of the working set that turns over each generation: this many
    /// files are removed and this many fresh files are added.
    churn_pct: u64,
    /// Cache capacity. `None` derives a generous default from `files`.
    cache_bytes: Option<u64>,
    source_delay_ms: u64,
    /// Wall-clock delay between each generation.
    interval_ms: u64,
}

impl Default for ServeConfig {
    fn default() -> Self {
        Self {
            seed: 1,
            mountpoint: PathBuf::from("/tmp/oxjfs"),
            generations: Some(24),
            files: 1000,
            churn_pct: 10,
            cache_bytes: None,
            source_delay_ms: 0,
            interval_ms: 1000,
        }
    }
}

#[derive(Default)]
struct SyntheticSource {
    objects: Mutex<BTreeMap<String, Arc<[u8]>>>,
    fail_once: Mutex<BTreeSet<String>>,
    calls: Mutex<BTreeMap<String, usize>>,
    /// Cumulative fetch count, kept O(1) so `serve` can prune `calls` (bounded
    /// memory over a long run) without losing the running total.
    fetch_total: AtomicU64,
    delay: Duration,
}

impl SyntheticSource {
    fn new(delay: Duration) -> Self {
        Self {
            delay,
            ..Self::default()
        }
    }

    fn insert(&self, tenant: &str, bytes: Vec<u8>) -> ContentRef {
        let reference = ContentRef::for_sha256(tenant, &bytes);
        self.objects
            .lock()
            .expect("synthetic object lock poisoned")
            .insert(reference.digest.clone(), Arc::from(bytes));
        reference
    }

    fn fail_next(&self, reference: &ContentRef) {
        self.fail_once
            .lock()
            .expect("synthetic fault lock poisoned")
            .insert(reference.digest.clone());
    }

    /// Drop stored objects (and per-digest call tallies) whose digest is no
    /// longer referenced. Keeps the in-memory origin bounded across a long
    /// `serve` run as files churn out; the cumulative `fetch_total` is
    /// unaffected, so running totals stay monotonic.
    fn retain(&self, live: &BTreeSet<String>) {
        if let Ok(mut objects) = self.objects.lock() {
            objects.retain(|digest, _| live.contains(digest));
        }
        if let Ok(mut calls) = self.calls.lock() {
            calls.retain(|digest, _| live.contains(digest));
        }
    }

    fn fetch_total(&self) -> u64 {
        self.fetch_total.load(Ordering::Relaxed)
    }
}

impl ContentSource for SyntheticSource {
    fn fetch(&self, reference: &ContentRef, output: &mut dyn Write) -> Result<(), FetchError> {
        if !self.delay.is_zero() {
            std::thread::sleep(self.delay);
        }
        self.fetch_total.fetch_add(1, Ordering::Relaxed);
        *self
            .calls
            .lock()
            .map_err(|_| io::Error::other("synthetic call lock poisoned"))?
            .entry(reference.digest.clone())
            .or_default() += 1;
        let bytes = self
            .objects
            .lock()
            .map_err(|_| io::Error::other("synthetic object lock poisoned"))?
            .get(&reference.digest)
            .cloned()
            .ok_or(FetchError::NotFound)?;
        if self
            .fail_once
            .lock()
            .map_err(|_| io::Error::other("synthetic fault lock poisoned"))?
            .remove(&reference.digest)
        {
            output.write_all(&bytes[..bytes.len() / 2])?;
            return Err(FetchError::Cancelled);
        }
        output.write_all(&bytes)?;
        Ok(())
    }
}

struct Report {
    config: Config,
    checks: Vec<(&'static str, bool)>,
    latencies_us: Vec<u128>,
    source_calls: usize,
    cache_capacity: u64,
    cache_remaining: u64,
    error: Option<String>,
}

fn main() {
    let mut args = std::env::args().skip(1);
    match args.next().as_deref() {
        Some("gate") => run_gate(args.collect()),
        Some("serve") => run_serve(args.collect()),
        other => {
            if let Some(other) = other {
                eprintln!("oxjtest: unknown subcommand {other}");
            }
            eprintln!(
                "usage: oxjtest <gate|serve> [options]\n  \
                 gate   run the deterministic release gate (in-process mount, self-unmounts)\n  \
                 serve  mount an evolving synthetic filesystem for manual inspection"
            );
            std::process::exit(2);
        }
    }
}

fn run_gate(args: Vec<String>) {
    let config = match parse_gate_args(args) {
        Ok(value) => value,
        Err(error) => {
            eprintln!("oxjtest: {error}");
            std::process::exit(2);
        }
    };
    let artifact = config.artifact.clone();
    let report = match run(config.clone()) {
        Ok(value) => value,
        Err(error) => Report {
            config,
            checks: Vec::new(),
            latencies_us: Vec::new(),
            source_calls: 0,
            cache_capacity: 0,
            cache_remaining: 0,
            error: Some(error.to_string()),
        },
    };
    if let Err(error) = write_report(&artifact, &report) {
        eprintln!("oxjtest: cannot write {}: {error}", artifact.display());
        std::process::exit(1);
    }
    let passed = report.error.is_none() && report.checks.iter().all(|(_, passed)| *passed);
    eprintln!(
        "oxjtest: {} (artifact={})",
        if passed { "PASS" } else { "FAIL" },
        artifact.display()
    );
    if !passed {
        std::process::exit(1);
    }
}

fn run_serve(args: Vec<String>) {
    let config = match parse_serve_args(args) {
        Ok(value) => value,
        Err(error) => {
            eprintln!("oxjtest: {error}");
            std::process::exit(2);
        }
    };
    if let Err(error) = serve(config) {
        eprintln!("oxjtest: serve failed: {error}");
        std::process::exit(1);
    }
}

fn run(config: Config) -> Result<Report, Box<dyn std::error::Error>> {
    if !cfg!(target_os = "macos") {
        return Err("the oxjtest release gate requires macOS mount_nfs".into());
    }
    let root = temp_root(config.seed);
    let mountpoint = root.join("mount");
    let source = Arc::new(SyntheticSource::new(Duration::from_millis(
        config.source_delay_ms,
    )));
    let stable_bytes = object_bytes(config.seed, 0);
    let stable_ref = source.insert("oxjtest", stable_bytes.clone());
    let first_bytes = object_bytes(config.seed, 1);
    let first_ref = source.insert("oxjtest", first_bytes.clone());
    let largest_current = (1..=config.generations.max(2))
        .chain([99, config.generations + 1000])
        .map(|object| object_size(config.seed, object))
        .max()
        .expect("oxjtest scenario always has objects");
    let cache_bytes = stable_bytes.len().saturating_add(largest_current) as u64;
    let workspace = Workspace::open_with_config(
        &root.join("workspace"),
        source.clone(),
        CacheConfig {
            max_bytes: cache_bytes,
        },
    )?;
    workspace.apply(manifest(
        "stable",
        1,
        "shared.bin",
        stable_ref,
        "stable reader",
    ))?;

    let server = NfsServer::new(workspace.clone(), ServerConfig::default()).spawn()?;
    let mounted = mount::mount_server(server.address().port(), &mountpoint)?;
    let mut checks = Vec::new();
    check(
        &mut checks,
        "initial mounted read integrity",
        fs::read(mountpoint.join("shared.bin"))? == stable_bytes,
    );

    source.fail_next(&first_ref);
    let failed = workspace
        .apply(manifest(
            "moving",
            1,
            "current.bin",
            first_ref.clone(),
            "fault recovery",
        ))
        .is_err();
    check(&mut checks, "injected partial fetch fails", failed);
    check(
        &mut checks,
        "failed content remains invisible",
        !mountpoint.join("current.bin").exists(),
    );
    workspace.apply(manifest(
        "moving",
        1,
        "current.bin",
        first_ref.clone(),
        "fault recovery",
    ))?;
    check(
        &mut checks,
        "fault retry publishes whole content",
        fs::read(mountpoint.join("current.bin"))? == first_bytes,
    );

    let stale = workspace.apply(manifest(
        "moving",
        1,
        "current.bin",
        source.insert("oxjtest", object_bytes(config.seed, 99)),
        "stale",
    ))?;
    check(&mut checks, "stale generation ignored", !stale.applied);

    let reader_path = mountpoint.join("shared.bin");
    let mut readers = Vec::new();
    for _ in 0..config.readers {
        let path = reader_path.clone();
        let expected = stable_bytes.clone();
        let reads = config.reads_per_reader;
        readers.push(std::thread::spawn(move || -> io::Result<()> {
            for _ in 0..reads {
                if fs::read(&path)? != expected {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "reader saw invalid bytes",
                    ));
                }
            }
            Ok(())
        }));
    }

    let mut latencies = Vec::new();
    let mut current_bytes = first_bytes;
    let mut previous_ref = first_ref;
    for generation in 2..=config.generations.max(2) {
        current_bytes = object_bytes(config.seed, generation);
        let current_ref = source.insert("oxjtest", current_bytes.clone());
        let started = Instant::now();
        workspace.apply(manifest(
            "moving",
            generation,
            "current.bin",
            current_ref.clone(),
            "evolving hotset",
        ))?;
        latencies.push(started.elapsed().as_micros());
        check(
            &mut checks,
            "drift evicts prior unselected content",
            !cached_object_exists(&root.join("workspace/cache/objects"), &previous_ref)?,
        );
        check(
            &mut checks,
            "evolving mounted read integrity",
            fs::read(mountpoint.join("current.bin"))? == current_bytes,
        );
        previous_ref = current_ref;
    }
    for reader in readers {
        reader
            .join()
            .map_err(|_| "mounted reader thread panicked")??;
    }
    checks.push(("concurrent mounted readers", true));

    let mut open = File::open(mountpoint.join("current.bin"))?;
    let mut prefix = vec![0; current_bytes.len() / 2];
    open.read_exact(&mut prefix)?;
    let replacement_bytes = object_bytes(config.seed, config.generations + 1000);
    let replacement_ref = source.insert("oxjtest", replacement_bytes.clone());
    workspace.apply(manifest(
        "moving",
        config.generations.max(2) + 1,
        "current.bin",
        replacement_ref.clone(),
        "open under mutation",
    ))?;
    let mut suffix = Vec::new();
    open.read_to_end(&mut suffix)?;
    prefix.extend(suffix);
    check(
        &mut checks,
        "open descriptor pins original bytes",
        prefix == current_bytes,
    );
    check(
        &mut checks,
        "new open sees replacement",
        fs::read(mountpoint.join("current.bin"))? == replacement_bytes,
    );
    let index = fs::read_to_string(mountpoint.join(".sageox/INDEX.json"))?;
    check(
        &mut checks,
        "index matches visible working set",
        index.contains("shared.bin")
            && index.contains("current.bin")
            && index.matches("\"status\":\"available\"").count() == 2,
    );

    mounted.unmount()?;
    server.shutdown()?;
    drop(workspace);
    corrupt_cached_object(&root.join("workspace/cache/objects"), &replacement_ref)?;
    let workspace = Workspace::open_with_config(
        &root.join("workspace"),
        source.clone(),
        CacheConfig {
            max_bytes: cache_bytes,
        },
    )?;
    let server = NfsServer::new(workspace.clone(), ServerConfig::default()).spawn()?;
    let mounted = mount::mount_server(server.address().port(), &mountpoint)?;
    check(
        &mut checks,
        "same-size corruption heals after restart",
        fs::read(mountpoint.join("current.bin"))? == replacement_bytes,
    );

    let oversized = vec![7; usize::try_from(cache_bytes).unwrap_or(usize::MAX - 1) + 1];
    let oversized_ref = source.insert("oxjtest", oversized);
    let outcome = workspace.apply(manifest(
        "oversized",
        1,
        "too-big.bin",
        oversized_ref,
        "capacity gate",
    ))?;
    check(
        &mut checks,
        "oversized object is stopped",
        outcome.stopped == 1,
    );
    check(
        &mut checks,
        "oversized object remains invisible",
        !mountpoint.join("too-big.bin").exists(),
    );
    let (capacity, remaining) = workspace.cache_capacity();
    let physical_bytes = cache_object_bytes(&root.join("workspace/cache/objects"))?;
    check(
        &mut checks,
        "cache usage stays within capacity",
        physical_bytes <= capacity,
    );
    mounted.unmount()?;
    server.shutdown()?;
    check(
        &mut checks,
        "mount lifecycle cleanup",
        !is_mounted(&mountpoint)?,
    );

    let source_calls = source
        .calls
        .lock()
        .map_err(|_| "synthetic call lock poisoned")?
        .values()
        .sum();
    fs::remove_dir_all(&root)?;
    Ok(Report {
        config,
        checks,
        latencies_us: latencies,
        source_calls,
        cache_capacity: capacity,
        cache_remaining: remaining,
        error: None,
    })
}

/// A tiny xorshift64 PRNG. The corpus and its churn are driven entirely from
/// `seed`, so a `(seed, generation)` sequence reproduces byte-for-byte.
struct Rng(u64);

impl Rng {
    fn new(seed: u64) -> Self {
        Self(seed | 1)
    }

    fn next_u64(&mut self) -> u64 {
        let mut state = self.0;
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        self.0 = state;
        state
    }

    fn below(&mut self, bound: usize) -> usize {
        if bound == 0 {
            0
        } else {
            (self.next_u64() % bound as u64) as usize
        }
    }
}

/// One synthetic file: a stable id fixes its path; its content is immutable
/// (content-addressed, write-once — files never change in place, matching
/// ox-fs semantics). Churn adds and removes files, never rewrites them.
#[derive(Clone)]
struct Slot {
    content: ContentRef,
    size: u64,
}

/// How many files entered and left the working set this generation.
struct Churn {
    added: usize,
    removed: usize,
}

/// The evolving working set. `advance` removes and adds a churn fraction of
/// files so the mounted tree visibly turns over generation to generation
/// while staying near `files` in size.
struct Corpus {
    seed: u64,
    churn_pct: u64,
    next_id: u64,
    slots: BTreeMap<u64, Slot>,
    rng: Rng,
}

/// Spread sequential ids across the weighted size distribution. Collisions
/// only mean two files share bytes, which is harmless for the mount.
fn corpus_token(id: u64) -> u64 {
    id.wrapping_mul(1_000_003)
}

fn make_slot(seed: u64, id: u64, source: &SyntheticSource) -> Slot {
    let bytes = object_bytes(seed, corpus_token(id));
    let size = bytes.len() as u64;
    let content = source.insert("oxjtest", bytes);
    Slot { content, size }
}

fn slot_path(id: u64) -> String {
    format!("shelf-{:02}/obj-{:06}.bin", id % 64, id)
}

impl Corpus {
    fn seed(config: &ServeConfig, source: &SyntheticSource) -> Self {
        let mut corpus = Self {
            seed: config.seed,
            churn_pct: config.churn_pct,
            next_id: 0,
            slots: BTreeMap::new(),
            rng: Rng::new(config.seed ^ 0x51ed_270b_2f24_9c73),
        };
        for _ in 0..config.files {
            corpus.spawn(source);
        }
        corpus
    }

    fn spawn(&mut self, source: &SyntheticSource) -> u64 {
        let id = self.next_id;
        self.next_id += 1;
        self.slots.insert(id, make_slot(self.seed, id, source));
        id
    }

    fn advance(&mut self, source: &SyntheticSource) -> Churn {
        let k = (self.slots.len() * self.churn_pct as usize)
            .div_ceil(100)
            .clamp(1, self.slots.len());
        let mut pool: Vec<u64> = self.slots.keys().copied().collect();

        let removed = self.pick(&mut pool, k);
        for id in &removed {
            self.slots.remove(id);
        }
        for _ in 0..k {
            self.spawn(source);
        }
        Churn {
            added: k,
            removed: removed.len(),
        }
    }

    /// Draw up to `k` distinct ids from `pool`, removing them from it.
    fn pick(&mut self, pool: &mut Vec<u64>, k: usize) -> Vec<u64> {
        let mut chosen = Vec::with_capacity(k);
        for _ in 0..k {
            if pool.is_empty() {
                break;
            }
            let index = self.rng.below(pool.len());
            chosen.push(pool.swap_remove(index));
        }
        chosen
    }

    fn manifest(&self, generation: u64) -> Manifest {
        let entries = self
            .slots
            .iter()
            .map(|(id, slot)| {
                ManifestEntry::new(
                    slot_path(*id),
                    "oxjtest",
                    "Synthetic",
                    0o444,
                    generation,
                    slot.content.clone(),
                    "evolving corpus",
                )
                .expect("generated corpus path is valid")
            })
            .collect();
        Manifest {
            session_id: "evolving".into(),
            generation,
            entries,
        }
    }

    fn live_digests(&self) -> BTreeSet<String> {
        self.slots
            .values()
            .map(|slot| slot.content.digest.clone())
            .collect()
    }

    fn len(&self) -> usize {
        self.slots.len()
    }

    fn total_bytes(&self) -> u64 {
        self.slots.values().map(|slot| slot.size).sum()
    }
}

/// Mount an evolving synthetic filesystem and hold it for manual inspection.
///
/// Unlike `gate`, this does not run assertions — it publishes a generated
/// working set of `files` files and churns a `churn_pct` fraction of them each
/// generation, emitting a single-line JSON stats record per generation on
/// stdout. Ctrl-C (or SIGTERM) unmounts cleanly and removes the workspace.
fn serve(config: ServeConfig) -> Result<(), Box<dyn std::error::Error>> {
    if !cfg!(target_os = "macos") {
        return Err("oxjtest serve requires macOS mount_nfs".into());
    }
    // `root` is owned here so it is removed on *every* exit path, including a
    // failure before the mount is established (macOS temp dirs live under
    // $TMPDIR, not /tmp, and would otherwise leak).
    let root = temp_root(config.seed);
    let outcome = serve_session(&config, &root);
    match fs::remove_dir_all(&root) {
        Ok(()) => {}
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => eprintln!(
            "oxjtest: warning: cannot remove {}: {error}",
            root.display()
        ),
    }
    outcome
}

fn serve_session(config: &ServeConfig, root: &Path) -> Result<(), Box<dyn std::error::Error>> {
    let source = Arc::new(SyntheticSource::new(Duration::from_millis(
        config.source_delay_ms,
    )));

    // A generous per-file allowance keeps the eager capacity gate from hiding
    // files: the synthetic corpus averages ~36 KiB/file, so 256 KiB each leaves
    // headroom for churn spikes. `--cache-bytes` overrides.
    let cache_bytes = config
        .cache_bytes
        .unwrap_or_else(|| (config.files as u64 * 256 * 1024).max(256 * 1024 * 1024));
    let workspace = Workspace::open_with_config(
        &root.join("workspace"),
        source.clone(),
        CacheConfig {
            max_bytes: cache_bytes,
        },
    )?;

    // Install the stop handler before mounting so a handler failure can never
    // strand a live mount.
    let stop = Arc::new(AtomicBool::new(false));
    install_stop_handler(stop.clone())?;

    let server = NfsServer::new(workspace.clone(), ServerConfig::default()).spawn()?;
    let mounted = mount::mount_server(server.address().port(), &config.mountpoint)?;

    eprintln!(
        "level=INFO action=serving mountpoint={} seed={} files={} churn_pct={} cache_bytes={} interval_ms={} generations={}",
        config.mountpoint.display(),
        config.seed,
        config.files,
        config.churn_pct,
        cache_bytes,
        config.interval_ms,
        config
            .generations
            .map(|g| g.to_string())
            .unwrap_or_else(|| "forever".into()),
    );
    eprintln!(
        "oxjtest: watch the working set evolve from another terminal, e.g.\n  \
         find {mp} -type f | wc -l\n  \
         du -sh {mp}\n  \
         cat {mp}/.sageox/INDEX.json | jq '.entries | length'\n  \
         while true; do find {mp} -type f | wc -l; sleep 1; done\n\
         oxjtest: Ctrl-C to unmount cleanly.",
        mp = config.mountpoint.display(),
    );

    // Evolve the generated working set, one generation per interval, until
    // stopped or the requested generation count is reached. A tear-down error
    // takes priority over an evolve error so we never leave a mount stranded.
    let evolve = evolve_loop(config, &source, &workspace, &stop);
    eprintln!(
        "\noxjtest: tearing down (unmount {})...",
        config.mountpoint.display()
    );
    let teardown = mounted
        .unmount()
        .and_then(|()| server.shutdown())
        .map_err(Box::<dyn std::error::Error>::from);
    drop(workspace);
    teardown?;
    evolve?;
    eprintln!("oxjtest: clean shutdown");
    Ok(())
}

/// One generation's measurements. Per-generation counts (`added`, `evicted`,
/// `fetched`, …) are deltas since the previous generation; `source_fetches`
/// and the cache figures are point-in-time totals.
struct GenStats {
    generation: u64,
    files: usize,
    total_bytes: u64,
    added: usize,
    removed: usize,
    evicted: u64,
    evict_blocked: u64,
    stopped: usize,
    applied: bool,
    materialize_us: u128,
    fetched: u64,
    source_fetches: u64,
    cache_capacity: u64,
    cache_remaining: u64,
}

fn evolve_loop(
    config: &ServeConfig,
    source: &Arc<SyntheticSource>,
    workspace: &Arc<Workspace>,
    stop: &AtomicBool,
) -> Result<(), Box<dyn std::error::Error>> {
    let mut corpus = Corpus::seed(config, source);
    let table = io::stdout().is_terminal();
    let mut generation = 0u64;
    // Previous point-in-time totals, so we can report per-generation deltas.
    let mut prev_fetches = 0u64;
    let mut prev_evicted = 0u64;
    let mut prev_blocked = 0u64;
    while !stop.load(Ordering::SeqCst) {
        generation += 1;
        // Generation 1 publishes the whole freshly seeded set; later
        // generations churn a fraction of it.
        let churn = if generation == 1 {
            Churn {
                added: corpus.len(),
                removed: 0,
            }
        } else {
            corpus.advance(source)
        };
        let started = Instant::now();
        let outcome = workspace.apply(corpus.manifest(generation))?;
        let elapsed = started.elapsed();
        // Keep the in-memory origin bounded to the live set as files rotate out.
        source.retain(&corpus.live_digests());

        let (capacity, remaining) = workspace.cache_capacity();
        let (evicted_total, blocked_total) = workspace.eviction_counters();
        let fetches_total = source.fetch_total();
        let stats = GenStats {
            generation,
            files: corpus.len(),
            total_bytes: corpus.total_bytes(),
            added: churn.added,
            removed: churn.removed,
            evicted: evicted_total - prev_evicted,
            evict_blocked: blocked_total - prev_blocked,
            stopped: outcome.stopped,
            applied: outcome.applied,
            materialize_us: elapsed.as_micros(),
            fetched: fetches_total - prev_fetches,
            source_fetches: fetches_total,
            cache_capacity: capacity,
            cache_remaining: remaining,
        };
        prev_fetches = fetches_total;
        prev_evicted = evicted_total;
        prev_blocked = blocked_total;
        emit_serve_stats(config, &stats, table);

        if config.generations.is_some_and(|total| generation >= total) {
            break;
        }
        sleep_interruptible(config.interval_ms, stop);
    }
    Ok(())
}

const TABLE_HEADER: &str =
    "  GEN   FILES    BYTES  ADD  RMV EVICT  BLK STOP  FETCH   MAT_MS         CACHE";

fn emit_serve_stats(config: &ServeConfig, stats: &GenStats, table: bool) {
    if table {
        // Human-readable columns for a live terminal; reprint the header
        // periodically so a long stream stays legible.
        if (stats.generation - 1).is_multiple_of(20) {
            println!("{TABLE_HEADER}");
        }
        let used = stats.cache_capacity.saturating_sub(stats.cache_remaining);
        println!(
            "{:>5} {:>7} {:>8} {:>4} {:>4} {:>5} {:>4} {:>4} {:>6} {:>8.1} {:>13}",
            stats.generation,
            stats.files,
            human_bytes(stats.total_bytes),
            stats.added,
            stats.removed,
            stats.evicted,
            stats.evict_blocked,
            stats.stopped,
            stats.fetched,
            stats.materialize_us as f64 / 1000.0,
            format!(
                "{}/{}",
                human_bytes(used),
                human_bytes(stats.cache_capacity)
            ),
        );
    } else {
        let ts = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        println!(
            "{{\"schema\":\"oxjtest.serve.v1\",\"ts_unix\":{ts},\"generation\":{},\"mountpoint\":\"{}\",\"files\":{},\"total_bytes\":{},\"added\":{},\"removed\":{},\"evicted\":{},\"evict_blocked\":{},\"stopped\":{},\"applied\":{},\"materialize_us\":{},\"fetched\":{},\"source_fetches\":{},\"cache_capacity\":{},\"cache_remaining\":{}}}",
            stats.generation,
            json_escape(&config.mountpoint.display().to_string()),
            stats.files,
            stats.total_bytes,
            stats.added,
            stats.removed,
            stats.evicted,
            stats.evict_blocked,
            stats.stopped,
            stats.applied,
            stats.materialize_us,
            stats.fetched,
            stats.source_fetches,
            stats.cache_capacity,
            stats.cache_remaining,
        );
    }
    let _ = io::stdout().flush();
}

fn human_bytes(bytes: u64) -> String {
    const UNITS: [&str; 5] = ["B", "K", "M", "G", "T"];
    let mut value = bytes as f64;
    let mut unit = 0;
    while value >= 1024.0 && unit < UNITS.len() - 1 {
        value /= 1024.0;
        unit += 1;
    }
    if unit == 0 {
        format!("{bytes}B")
    } else {
        format!("{value:.1}{}", UNITS[unit])
    }
}

fn sleep_interruptible(total_ms: u64, stop: &AtomicBool) {
    let mut remaining = total_ms;
    while remaining > 0 && !stop.load(Ordering::SeqCst) {
        let slice = remaining.min(100);
        std::thread::sleep(Duration::from_millis(slice));
        remaining -= slice;
    }
}

#[cfg(unix)]
fn install_stop_handler(stop: Arc<AtomicBool>) -> Result<(), Box<dyn std::error::Error>> {
    ctrlc::set_handler(move || stop.store(true, Ordering::SeqCst))
        .map_err(|error| format!("cannot install signal handler: {error}").into())
}

#[cfg(not(unix))]
fn install_stop_handler(_stop: Arc<AtomicBool>) -> Result<(), Box<dyn std::error::Error>> {
    Err("oxjtest serve signal handling is only available on unix".into())
}

fn manifest(
    session: &str,
    generation: u64,
    path: &str,
    content: ContentRef,
    reason: &str,
) -> Manifest {
    Manifest {
        session_id: session.into(),
        generation,
        entries: vec![
            ManifestEntry::new(path, path, "Synthetic", 0o444, generation, content, reason)
                .expect("static oxjtest path is valid"),
        ],
    }
}

fn object_bytes(seed: u64, object: u64) -> Vec<u8> {
    let size = object_size(seed, object % VIRTUAL_FILES);
    let mut state = seed ^ object.wrapping_mul(0x9e37_79b9_7f4a_7c15);
    (0..size)
        .map(|_| {
            state ^= state << 13;
            state ^= state >> 7;
            state ^= state << 17;
            state as u8
        })
        .collect()
}

/// Deterministic weighted corpus: 68% tiny context files, a long middle tail,
/// 0.99% p99-sized media, and 0.01% max-sized media. The universe is virtual;
/// only selected objects are allocated.
fn object_size(seed: u64, object: u64) -> usize {
    let mut mixed = seed ^ object.wrapping_mul(0x9e37_79b9_7f4a_7c15);
    mixed ^= mixed >> 30;
    mixed = mixed.wrapping_mul(0xbf58_476d_1ce4_e5b9);
    mixed ^= mixed >> 27;
    let bucket = mixed % 10_000;
    match bucket {
        0..=6799 => SMALL_BYTES,
        6800..=9899 => 4096 + (mixed as usize % (128 * 1024 - 4096)),
        9900..=9998 => P99_BYTES,
        _ => MAX_BYTES,
    }
}

fn corrupt_cached_object(root: &Path, reference: &ContentRef) -> io::Result<()> {
    let target = reference.storage_key();
    for tenant in fs::read_dir(root)? {
        let path = tenant?.path().join(&target);
        if path.exists() {
            return fs::write(path, vec![0; reference.size as usize]);
        }
    }
    Err(io::Error::new(
        io::ErrorKind::NotFound,
        "cached object not found",
    ))
}

fn cached_object_exists(root: &Path, reference: &ContentRef) -> io::Result<bool> {
    let target = reference.storage_key();
    for tenant in fs::read_dir(root)? {
        if tenant?.path().join(&target).exists() {
            return Ok(true);
        }
    }
    Ok(false)
}

fn cache_object_bytes(root: &Path) -> io::Result<u64> {
    let mut bytes = 0u64;
    for tenant in fs::read_dir(root)? {
        for object in fs::read_dir(tenant?.path())? {
            bytes = bytes.saturating_add(object?.metadata()?.len());
        }
    }
    Ok(bytes)
}

fn is_mounted(mountpoint: &Path) -> io::Result<bool> {
    let mounts = fs::read_to_string("/etc/mtab")
        .or_else(|_| fs::read_to_string("/proc/mounts"))
        .or_else(|_| {
            std::process::Command::new("/sbin/mount")
                .output()
                .map(|output| String::from_utf8_lossy(&output.stdout).into_owned())
        })?;
    Ok(mounts
        .lines()
        .any(|line| line.contains(&mountpoint.display().to_string())))
}

fn check(checks: &mut Vec<(&'static str, bool)>, name: &'static str, passed: bool) {
    checks.push((name, passed));
}

fn parse_gate_args(args: Vec<String>) -> Result<Config, String> {
    let mut config = Config::default();
    let mut args = args.into_iter();
    while let Some(flag) = args.next() {
        let value = args
            .next()
            .ok_or_else(|| format!("missing value for {flag}"))?;
        match flag.as_str() {
            "--seed" => config.seed = parse(&flag, &value)?,
            "--generations" => config.generations = parse(&flag, &value)?,
            "--readers" => config.readers = parse(&flag, &value)?,
            "--reads-per-reader" => config.reads_per_reader = parse(&flag, &value)?,
            "--source-delay-ms" => config.source_delay_ms = parse(&flag, &value)?,
            "--artifact" => config.artifact = PathBuf::from(value),
            _ => return Err(format!("unknown option {flag}")),
        }
    }
    if config.generations < 2 || config.readers == 0 || config.reads_per_reader == 0 {
        return Err("generations must be at least 2 and reader counts must be positive".into());
    }
    Ok(config)
}

fn parse_serve_args(args: Vec<String>) -> Result<ServeConfig, String> {
    let mut config = ServeConfig::default();
    let mut saw_mountpoint = false;
    let mut args = args.into_iter();
    while let Some(flag) = args.next() {
        if flag == "--evolve-forever" {
            config.generations = None;
            continue;
        }
        let value = args
            .next()
            .ok_or_else(|| format!("missing value for {flag}"))?;
        match flag.as_str() {
            "--seed" => config.seed = parse(&flag, &value)?,
            "--mountpoint" => {
                config.mountpoint = PathBuf::from(value);
                saw_mountpoint = true;
            }
            "--generations" => config.generations = Some(parse(&flag, &value)?),
            "--files" => config.files = parse(&flag, &value)?,
            "--churn" => config.churn_pct = parse(&flag, &value)?,
            "--cache-bytes" => config.cache_bytes = Some(parse(&flag, &value)?),
            "--source-delay-ms" => config.source_delay_ms = parse(&flag, &value)?,
            "--interval-ms" => config.interval_ms = parse(&flag, &value)?,
            _ => return Err(format!("unknown option {flag}")),
        }
    }
    if !saw_mountpoint {
        return Err("usage: oxjtest serve --mountpoint PATH [--seed N] [--generations N | --evolve-forever] [--files N] [--churn PCT] [--cache-bytes N] [--source-delay-ms N] [--interval-ms N]".into());
    }
    if config.generations == Some(0) {
        return Err("generations must be at least 1 (or use --evolve-forever)".into());
    }
    if config.files == 0 {
        return Err("--files must be at least 1".into());
    }
    if config.churn_pct > 100 {
        return Err("--churn must be a percentage between 0 and 100".into());
    }
    if config.cache_bytes == Some(0) {
        return Err("--cache-bytes must be positive".into());
    }
    Ok(config)
}

fn parse<T: std::str::FromStr>(flag: &str, value: &str) -> Result<T, String> {
    value
        .parse()
        .map_err(|_| format!("invalid value for {flag}: {value}"))
}

fn temp_root(seed: u64) -> PathBuf {
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    std::env::temp_dir().join(format!("oxjtest-{}-{seed}-{nonce}", std::process::id()))
}

fn write_report(path: &Path, report: &Report) -> io::Result<()> {
    if let Some(parent) = path.parent().filter(|path| !path.as_os_str().is_empty()) {
        fs::create_dir_all(parent)?;
    }
    let mut latencies = report.latencies_us.clone();
    latencies.sort_unstable();
    let percentile = |n: usize| -> u128 {
        if latencies.is_empty() {
            0
        } else {
            latencies[(latencies.len() - 1) * n / 100]
        }
    };
    let mut json = format!(
        "{{\n  \"schema\":\"oxjtest.v1\",\n  \"seed\":{},\n  \"config\":{{\"generations\":{},\"readers\":{},\"reads_per_reader\":{},\"source_delay_ms\":{}}},\n  \"passed\":{},\n  \"error\":{},\n  \"checks\":[",
        report.config.seed,
        report.config.generations,
        report.config.readers,
        report.config.reads_per_reader,
        report.config.source_delay_ms,
        report.error.is_none() && report.checks.iter().all(|(_, passed)| *passed),
        report
            .error
            .as_ref()
            .map(|error| format!("\"{}\"", json_escape(error)))
            .unwrap_or_else(|| "null".into())
    );
    for (index, (name, passed)) in report.checks.iter().enumerate() {
        if index != 0 {
            json.push(',');
        }
        write!(
            json,
            "{{\"name\":\"{}\",\"passed\":{passed}}}",
            json_escape(name)
        )
        .expect("writing JSON to a String cannot fail");
    }
    write!(
        json,
        "],\n  \"counters\":{{\"source_fetches\":{},\"cache_capacity\":{},\"cache_remaining\":{}}},\n  \"materialize_latency_us\":{{\"samples\":{},\"p50\":{},\"p95\":{},\"p99\":{},\"values\":[",
        report.source_calls,
        report.cache_capacity,
        report.cache_remaining,
        latencies.len(),
        percentile(50),
        percentile(95),
        percentile(99)
    )
    .expect("writing JSON to a String cannot fail");
    for (index, latency) in latencies.iter().enumerate() {
        if index != 0 {
            json.push(',');
        }
        write!(json, "{latency}").expect("writing JSON to a String cannot fail");
    }
    json.push_str("]}\n}\n");
    fs::write(path, json)
}

fn json_escape(value: &str) -> String {
    value
        .replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('\n', "\\n")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn synthetic_objects_are_seeded_and_stable() {
        assert_eq!(object_bytes(7, 2), object_bytes(7, 2));
        assert_ne!(object_bytes(7, 2), object_bytes(7, 3));
        assert!((0..1000).all(|object| object_size(7, object) <= MAX_BYTES));
    }

    #[test]
    fn report_is_valid_enough_for_consumers_without_mounting() {
        let path = temp_root(9).join("report.json");
        let report = Report {
            config: Config::default(),
            checks: vec![("example", true)],
            latencies_us: vec![30, 10, 20],
            source_calls: 2,
            cache_capacity: 10,
            cache_remaining: 3,
            error: None,
        };
        write_report(&path, &report).unwrap();
        let value = fs::read_to_string(&path).unwrap();
        assert!(value.contains("\"schema\":\"oxjtest.v1\""));
        assert!(value.contains("\"p50\":20"));
        fs::remove_dir_all(path.parent().unwrap()).unwrap();
    }

    fn serve_config(files: usize, churn_pct: u64) -> ServeConfig {
        ServeConfig {
            files,
            churn_pct,
            ..ServeConfig::default()
        }
    }

    /// A manifest fingerprint: which paths exist and the content behind each.
    fn fingerprint(manifest: &Manifest) -> Vec<(String, String)> {
        let mut pairs: Vec<_> = manifest
            .entries
            .iter()
            .map(|entry| (entry.path.as_str().to_owned(), entry.content.digest.clone()))
            .collect();
        pairs.sort();
        pairs
    }

    #[test]
    fn corpus_holds_size_and_churns_within_budget() {
        let source = SyntheticSource::new(Duration::ZERO);
        let mut corpus = Corpus::seed(&serve_config(200, 10), &source);
        assert_eq!(corpus.len(), 200);
        assert!(corpus.total_bytes() > 0);

        for _ in 0..8 {
            let churn = corpus.advance(&source);
            // 10% of 200 = 20 removed and 20 added, so size is preserved.
            assert_eq!(churn.added, 20);
            assert_eq!(churn.removed, 20);
            assert_eq!(corpus.len(), 200);
            // The origin never retains more than the live set once pruned.
            source.retain(&corpus.live_digests());
        }
    }

    #[test]
    fn corpus_evolution_is_deterministic_per_seed() {
        let replay = || {
            let source = SyntheticSource::new(Duration::ZERO);
            let mut corpus = Corpus::seed(&serve_config(64, 25), &source);
            (1..=5)
                .map(|generation| {
                    if generation > 1 {
                        corpus.advance(&source);
                    }
                    fingerprint(&corpus.manifest(generation))
                })
                .collect::<Vec<_>>()
        };
        assert_eq!(replay(), replay());
    }

    #[test]
    fn human_bytes_scales_units() {
        assert_eq!(human_bytes(0), "0B");
        assert_eq!(human_bytes(606), "606B");
        assert_eq!(human_bytes(1024), "1.0K");
        assert_eq!(human_bytes(1_400_000), "1.3M");
        assert_eq!(human_bytes(268_435_456), "256.0M");
        assert_eq!(human_bytes(1_073_741_824), "1.0G");
    }

    #[test]
    fn churn_actually_moves_the_working_set() {
        let source = SyntheticSource::new(Duration::ZERO);
        let mut corpus = Corpus::seed(&serve_config(100, 20), &source);
        let before = fingerprint(&corpus.manifest(1));
        corpus.advance(&source);
        let after = fingerprint(&corpus.manifest(2));
        assert_ne!(before, after, "a generation must change the working set");
    }
}

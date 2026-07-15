use crate::{CacheModel, EvictionOrder};
use oxfs::{
    CacheConfig, ContentRef, ContentSource, FetchError, Manifest, ManifestEntry, Workspace,
};
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

// Small deterministic PRNG (xorshift64) — no external deps.
struct Rng(u64);
impl Rng {
    fn next(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        self.0 = x;
        x
    }
    fn below(&mut self, n: usize) -> usize {
        (self.next() % n as u64) as usize
    }
}

fn tmp() -> std::path::PathBuf {
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    std::env::temp_dir().join(format!(
        "cachesim-diff-{}-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

/// The reference model must predict the exact resident set of the real cache
/// under random single-session churn with eviction pressure. This is the
/// regression baseline: any future eviction change that diverges from today's
/// least-recently-selected-generation semantics fails here with a seed+gen.
#[test]
fn generation_model_matches_impl_under_random_single_session_churn() {
    const N: usize = 40;
    for order in [
        EvictionOrder::LeastRecentlySelectedGeneration,
        EvictionOrder::ClockSecondChance,
        EvictionOrder::ApproxLeastFrequentlyUsed,
    ] {
        for seed in [1u64, 2, 7, 42, 1000, 999_983] {
            // Distinct-content corpus of varying sizes (first byte = index ensures
            // distinct digests; N < 256).
            let mut objects = BTreeMap::new();
            let mut refs = Vec::with_capacity(N);
            for i in 0..N {
                let size = 4 + (i * 7 % 25);
                let bytes: Vec<u8> = (0..size).map(|j| (i as u8).wrapping_add(j as u8)).collect();
                let r = ContentRef::for_sha256("t", &bytes);
                objects.insert(r.digest.clone(), bytes);
                refs.push(r);
            }
            let total: u64 = refs.iter().map(|r| r.size).sum();
            let capacity = (total / 3).max(refs.iter().map(|r| r.size).max().unwrap());

            let root = tmp();
            let ws = Workspace::open_with_config(
                &root,
                Arc::new(MemorySource {
                    objects: objects.clone(),
                }),
                CacheConfig {
                    max_bytes: capacity,
                    eviction: order,
                },
            )
            .unwrap();
            let mut model = CacheModel::new(capacity, order);
            let mut rng = Rng(seed ^ 0x9e37_79b9_7f4a_7c15);

            for generation in 1..=60u64 {
                // A random-size, random-order subset of the corpus, no repeats.
                let mut idx: Vec<usize> = (0..N).collect();
                let k = 1 + rng.below(N);
                for i in 0..k {
                    let j = i + rng.below(N - i);
                    idx.swap(i, j);
                }
                let chosen = &idx[0..k];

                let mut entries = Vec::with_capacity(k);
                let mut model_entries = Vec::with_capacity(k);
                for &i in chosen {
                    let r = refs[i].clone();
                    model_entries.push((ws.storage_key(&r), r.size));
                    entries.push(
                        ManifestEntry::new(format!("f{i}"), "s", "Session", 0o444, 0, r, "sel")
                            .unwrap(),
                    );
                }

                let before = ws.snapshot_state().unwrap();
                let applied = ws.apply(Manifest {
                    session_id: "s".into(),
                    generation,
                    entries,
                });
                if matches!(
                    applied,
                    Err(oxfs::WorkspaceError::ReplacementCapacity { .. })
                ) {
                    assert_eq!(ws.snapshot_state().unwrap(), before);
                    continue;
                }
                applied.unwrap();
                model.apply(&model_entries);

                assert_eq!(
                    ws.resident_keys().unwrap(),
                    model.resident_keys(),
                    "seed {seed} gen {generation}: resident set diverged",
                );
                assert_eq!(
                    ws.cache_telemetry().unwrap().resident_bytes,
                    model.used_bytes(),
                    "seed {seed} gen {generation}: used_bytes diverged",
                );
            }
            std::fs::remove_dir_all(&root).ok();
        }
    }
}

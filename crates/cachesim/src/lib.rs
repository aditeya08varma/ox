//! Pure in-memory reference model of the oxfs content cache + differential
//! oracle. See `crates/oxfs/docs/cachesim-oracle-north-star.md` and
//! `eviction-policy-design-1.1.md`.
//!
//! One spec, two mechanisms: this model realizes the cache's policy semantics
//! with plain in-memory maps; the real `oxfs::Workspace`/`ContentCache` realizes
//! them with SQLite + disk. A differential harness runs the same op stream
//! through both and asserts the resident key sets agree. The independence of the
//! two realizations is what lets the diff catch policy bugs, not just plumbing.
//!
//! v1 models the default policy: **least-recently-selected generation eviction
//! with deterministic key ordering inside a generation** (NOT read-based LRU —
//! reads do not touch recency here). CLOCK and LFU models land with those
//! policies in the impl.

pub use oxfs::EvictionOrder;
use std::collections::{BTreeMap, BTreeSet};

#[derive(Clone, Debug, Eq, PartialEq)]
struct Obj {
    size: u64,
    access_epoch: u64,
    insert_seq: u64,
    reference: bool,
    frequency: u8,
}

/// Pure reference cache. Drive it with the exact catalog keys the real cache
/// uses (`Workspace::storage_key`) and compare `resident_keys()` after each apply.
#[derive(Clone, Debug)]
pub struct CacheModel {
    capacity: u64,
    order: EvictionOrder,
    epoch: u64,
    resident: BTreeMap<String, Obj>,
    next_insert_seq: u64,
    clock_hand: u64,
}

impl CacheModel {
    pub fn new(capacity: u64, order: EvictionOrder) -> Self {
        Self {
            capacity,
            order,
            epoch: 0,
            resident: BTreeMap::new(),
            next_insert_seq: 1,
            clock_hand: 0,
        }
    }

    /// Apply one manifest's desired entries as `(key, size)` in manifest order.
    /// Mirrors `Workspace::apply` -> `materialize_missing_batch` for the default
    /// policy on a workload whose source always has the content (no fetch
    /// failures / rollbacks): epoch bumps once, admission owns dedup +
    /// residency-skip + oversize-break, reservations are sequential, victims are
    /// `(access_epoch, key)`-ordered residents, objects reserved earlier in the
    /// same batch are pending (not evictable), and a `StorageFull` reservation
    /// stops further admission without discarding what already fit.
    pub fn apply(&mut self, entries: &[(String, u64)]) {
        self.epoch += 1;

        // Admission (AdmitEverything): first-occurrence dedup, residency-skip,
        // break at the first object larger than the whole cache.
        let mut seen = BTreeSet::new();
        let mut missing: Vec<(String, u64)> = Vec::new();
        for (key, size) in entries {
            if !seen.insert(key.as_str()) {
                continue;
            }
            if self.resident.contains_key(key) {
                continue;
            }
            if *size > self.capacity {
                break;
            }
            missing.push((key.clone(), *size));
        }

        // Sequential reservation. `pending` are state=0 this batch: they occupy
        // bytes but are never eviction candidates.
        let mut pending: BTreeMap<String, (u64, u64)> = BTreeMap::new();
        let mut used: u64 = self.resident.values().map(|o| o.size).sum();
        'admit: for (key, size) in missing {
            if used + size > self.capacity {
                // Plan victims WITHOUT mutating (all-or-nothing): the real cache
                // consumes no victims when a reservation can't fit
                // (`failed_reservation_does_not_consume_partial_victims`).
                let policy_before = self.resident.clone();
                let hand_before = self.clock_hand;
                let victims = self.plan_victims(&key, used, size);
                let freed: u64 = victims.iter().map(|v| self.resident[v].size).sum();
                if used + size - freed > self.capacity {
                    self.resident = policy_before;
                    self.clock_hand = hand_before;
                    // Not enough evictable resident -> StorageFull. Evict nothing
                    // and stop admitting further objects; keep what already fit.
                    break 'admit;
                }
                for v in &victims {
                    used -= self.resident[v].size;
                    self.resident.remove(v);
                }
            }
            let insert_seq = self.next_insert_seq;
            self.next_insert_seq += 1;
            pending.insert(key, (size, insert_seq));
            used += size;
        }

        // Commit: pending -> resident at the current epoch (finish stamp).
        let epoch = self.epoch;
        for (key, (size, insert_seq)) in pending {
            self.resident.insert(
                key,
                Obj {
                    size,
                    access_epoch: epoch,
                    insert_seq,
                    reference: false,
                    frequency: 0,
                },
            );
        }
    }

    /// The ordered prefix of residents (excluding `incoming`) that would be
    /// evicted to fit `size`, under the configured order. Pure — mutates nothing.
    fn plan_victims(&mut self, incoming: &str, used: u64, size: u64) -> Vec<String> {
        let mut candidates: Vec<(String, Obj)> = self
            .resident
            .iter()
            .filter(|(k, _)| k.as_str() != incoming)
            .map(|(key, object)| (key.clone(), object.clone()))
            .collect();
        match self.order {
            EvictionOrder::LeastRecentlySelectedGeneration => {
                candidates.sort_by(|(ak, ao), (bk, bo)| {
                    (ao.access_epoch, ak.as_str()).cmp(&(bo.access_epoch, bk.as_str()))
                })
            }
            EvictionOrder::ClockSecondChance => {
                candidates.sort_by(|(ak, ao), (bk, bo)| {
                    (ao.insert_seq < self.clock_hand, ao.insert_seq, ak.as_str()).cmp(&(
                        bo.insert_seq < self.clock_hand,
                        bo.insert_seq,
                        bk.as_str(),
                    ))
                });
                let mut cold = Vec::new();
                let mut hot = Vec::new();
                for (key, obj) in candidates {
                    if obj.reference {
                        if let Some(state) = self.resident.get_mut(&key) {
                            state.reference = false;
                        }
                        hot.push((key, obj));
                    } else {
                        cold.push((key, obj));
                    }
                }
                cold.extend(hot);
                candidates = cold;
            }
            EvictionOrder::ApproxLeastFrequentlyUsed => candidates.sort_by(|(ak, ao), (bk, bo)| {
                (ao.frequency, ao.insert_seq, ak.as_str()).cmp(&(
                    bo.frequency,
                    bo.insert_seq,
                    bk.as_str(),
                ))
            }),
        }
        let mut freed = 0u64;
        let mut victims = Vec::new();
        for (key, obj) in candidates {
            if used + size - freed <= self.capacity {
                break;
            }
            freed += obj.size;
            victims.push(key);
        }
        if self.order == EvictionOrder::ClockSecondChance
            && let Some(key) = victims.last()
            && let Some(object) = self.resident.get(key)
        {
            self.clock_hand = object.insert_seq.saturating_add(1);
        }
        victims
    }

    pub fn touch(&mut self, key: &str) {
        let Some(object) = self.resident.get_mut(key) else {
            return;
        };
        match self.order {
            EvictionOrder::LeastRecentlySelectedGeneration => {}
            EvictionOrder::ClockSecondChance => object.reference = true,
            EvictionOrder::ApproxLeastFrequentlyUsed => {
                object.frequency = object.frequency.saturating_add(1)
            }
        }
    }

    /// Model catalog corruption/missing-file healing before persisted desired
    /// content is re-applied during workspace recovery.
    pub fn mark_missing(&mut self, key: &str) {
        self.resident.remove(key);
    }

    /// Resident keys in deterministic order — compare against
    /// `Workspace::resident_keys()`.
    pub fn resident_keys(&self) -> Vec<String> {
        self.resident.keys().cloned().collect()
    }

    pub fn used_bytes(&self) -> u64 {
        self.resident.values().map(|o| o.size).sum()
    }
}

#[cfg(test)]
mod tests;

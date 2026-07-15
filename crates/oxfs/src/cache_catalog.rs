use crate::cache_policy::{EvictionOrder, Touch};
use rusqlite::{Connection, OptionalExtension, params};
use std::io;
use std::path::Path;
use std::time::{Duration, Instant};

const STATE_PENDING: i64 = 0;
const STATE_RESIDENT: i64 = 1;
const STATE_EVICTED: i64 = 2;

#[derive(Clone, Debug)]
pub(crate) struct Victim {
    pub key: String,
    pub size: u64,
    pub access: u64,
}

#[derive(Clone, Copy, Debug, Default)]
pub(crate) struct Gauges {
    pub resident_objects: u64,
    pub resident_bytes: u64,
    pub pending_objects: u64,
}

/// One resident object as seen by the differential oracle.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResidentRow {
    pub key: String,
    pub size: u64,
    pub access_epoch: u64,
    pub insert_seq: u64,
    pub slot: u32,
}

/// Ground-truth resident+scalar snapshot for the differential oracle.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct CatalogSnapshot {
    pub resident: Vec<ResidentRow>,
    pub used_bytes: u64,
    pub epoch: u64,
    pub clock_hand: u64,
    pub policy_values: Vec<(String, u8)>,
}

#[derive(Clone, Debug, Default)]
struct SlotState {
    key: Option<String>,
    reference: bool,
    frequency: u8,
}

#[derive(Clone, Debug, Default)]
struct PolicyState {
    slots: Vec<SlotState>,
    clock_hand: u64,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum Reservation {
    Resident,
    Pending,
    Refetch,
}

pub(crate) struct Catalog {
    connection: Connection,
    in_batch: bool,
    epoch: u64,
    used_bytes: u64,
    batch_used_start: Option<u64>,
    order: EvictionOrder,
    policy: PolicyState,
    batch_policy_start: Option<PolicyState>,
}

impl Catalog {
    pub fn open(path: &Path, order: EvictionOrder) -> io::Result<Self> {
        let connection = Connection::open(path).map_err(sql_error)?;
        connection
            .busy_timeout(Duration::from_secs(5))
            .map_err(sql_error)?;
        connection
            .execute_batch(
                "PRAGMA journal_mode=WAL;
				 PRAGMA synchronous=NORMAL;
				 PRAGMA temp_store=MEMORY;
				 PRAGMA foreign_keys=ON;
				 CREATE TABLE IF NOT EXISTS cache_meta (
				   singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
				   epoch INTEGER NOT NULL,
				   used_bytes INTEGER NOT NULL
				 );
				 INSERT OR IGNORE INTO cache_meta(singleton, epoch, used_bytes) VALUES (1, 0, 0);
				 CREATE TABLE IF NOT EXISTS cache_objects (
				   key TEXT PRIMARY KEY,
				   size INTEGER NOT NULL CHECK (size >= 0),
				   access_epoch INTEGER NOT NULL,
				   state INTEGER NOT NULL CHECK (state IN (0, 1, 2))
				 ) WITHOUT ROWID;
                 CREATE INDEX IF NOT EXISTS cache_objects_evict
				   ON cache_objects(state, access_epoch, key);",
            )
            .map_err(sql_error)?;
        let has_legacy_pin = {
            let mut statement = connection
                .prepare("PRAGMA table_info(cache_objects)")
                .map_err(sql_error)?;
            let columns = statement
                .query_map([], |row| row.get::<_, String>(1))
                .map_err(sql_error)?
                .collect::<Result<Vec<_>, _>>()
                .map_err(sql_error)?;
            columns.iter().any(|column| column == "pin_epoch")
        };
        if has_legacy_pin {
            connection
                .execute_batch(
                    "BEGIN IMMEDIATE;
                     DROP INDEX IF EXISTS cache_objects_evict;
                     ALTER TABLE cache_objects RENAME TO cache_objects_with_pins;
                     CREATE TABLE cache_objects (
                       key TEXT PRIMARY KEY,
                       size INTEGER NOT NULL CHECK (size >= 0),
                       access_epoch INTEGER NOT NULL,
                       state INTEGER NOT NULL CHECK (state IN (0, 1, 2))
                     ) WITHOUT ROWID;
                     INSERT INTO cache_objects(key,size,access_epoch,state)
                       SELECT key,size,access_epoch,state FROM cache_objects_with_pins;
                     DROP TABLE cache_objects_with_pins;
                     CREATE INDEX cache_objects_evict
                       ON cache_objects(state, access_epoch, key);
                     COMMIT;",
                )
                .map_err(sql_error)?;
        }
        let object_columns = table_columns(&connection, "cache_objects")?;
        if !object_columns.iter().any(|column| column == "insert_seq") {
            connection
                .execute_batch(
                    "BEGIN IMMEDIATE;
                     ALTER TABLE cache_objects ADD COLUMN insert_seq INTEGER NOT NULL DEFAULT 0;
                     ALTER TABLE cache_objects ADD COLUMN slot INTEGER NOT NULL DEFAULT 0;
                     UPDATE cache_objects SET
                       insert_seq=(SELECT COUNT(*) FROM cache_objects AS earlier
                         WHERE (earlier.access_epoch, earlier.key) <= (cache_objects.access_epoch, cache_objects.key)),
                       slot=(SELECT COUNT(*)-1 FROM cache_objects AS earlier
                         WHERE (earlier.access_epoch, earlier.key) <= (cache_objects.access_epoch, cache_objects.key));
                     CREATE INDEX cache_objects_clock ON cache_objects(state, insert_seq, key);
                     ALTER TABLE cache_meta ADD COLUMN next_insert_seq INTEGER NOT NULL DEFAULT 1;
                     ALTER TABLE cache_meta ADD COLUMN clock_hand INTEGER NOT NULL DEFAULT 0;
                     UPDATE cache_meta SET next_insert_seq=MAX(1,(SELECT COALESCE(MAX(insert_seq),0)+1 FROM cache_objects));
                     COMMIT;",
                )
                .map_err(sql_error)?;
        }
        let (epoch, used_bytes, clock_hand) = connection
            .query_row(
                "SELECT epoch, used_bytes, clock_hand FROM cache_meta WHERE singleton=1",
                [],
                |row| {
                    Ok((
                        row.get::<_, i64>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                    ))
                },
            )
            .map_err(sql_error)?;
        let mut policy = PolicyState {
            clock_hand: from_sql_u64(clock_hand)?,
            ..PolicyState::default()
        };
        {
            let mut statement = connection
                .prepare(
                    "SELECT key, insert_seq, slot FROM cache_objects WHERE state=1 ORDER BY slot",
                )
                .map_err(sql_error)?;
            let rows = statement
                .query_map([], |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                    ))
                })
                .map_err(sql_error)?;
            for row in rows {
                let (key, seq, slot) = row.map_err(sql_error)?;
                let slot = usize::try_from(from_sql_u64(slot)?).map_err(|_| {
                    io::Error::new(io::ErrorKind::InvalidData, "slot exceeds usize")
                })?;
                policy.slots.resize_with(slot + 1, SlotState::default);
                let _ = from_sql_u64(seq)?;
                policy.slots[slot] = SlotState {
                    key: Some(key),
                    reference: false,
                    frequency: 0,
                };
            }
        }
        Ok(Self {
            connection,
            in_batch: false,
            epoch: from_sql_u64(epoch)?,
            used_bytes: from_sql_u64(used_bytes)?,
            batch_used_start: None,
            order,
            policy,
            batch_policy_start: None,
        })
    }

    pub fn begin_batch(&mut self) -> io::Result<()> {
        if self.in_batch {
            return Err(io::Error::other("cache catalog batch already active"));
        }
        self.connection
            .execute_batch("BEGIN IMMEDIATE")
            .map_err(sql_error)?;
        self.in_batch = true;
        self.batch_used_start = Some(self.used_bytes);
        self.batch_policy_start = Some(self.policy.clone());
        self.epoch = self.epoch.saturating_add(1);
        let result = self
            .connection
            .execute(
                "UPDATE cache_meta SET epoch=?1 WHERE singleton=1",
                params![to_sql_i64(self.epoch)?],
            )
            .map(|_| ())
            .map_err(sql_error);
        if let Err(error) = result {
            let _ = self.rollback();
            return Err(error);
        }
        Ok(())
    }

    pub fn commit(&mut self) -> io::Result<u128> {
        if !self.in_batch {
            return Ok(0);
        }
        let started = Instant::now();
        self.persist_used_bytes()?;
        self.connection.execute_batch("COMMIT").map_err(sql_error)?;
        self.in_batch = false;
        self.batch_used_start = None;
        self.batch_policy_start = None;
        Ok(started.elapsed().as_micros())
    }

    pub fn rollback(&mut self) -> io::Result<()> {
        if self.in_batch {
            self.connection
                .execute_batch("ROLLBACK")
                .map_err(sql_error)?;
            self.in_batch = false;
            self.used_bytes = self.batch_used_start.take().unwrap_or(self.used_bytes);
            self.policy = self.batch_policy_start.take().unwrap_or_default();
        }
        Ok(())
    }

    pub fn is_resident(&self, key: &str, size: u64) -> io::Result<bool> {
        let size = to_sql_i64(size)?;
        self.connection
            .query_row(
                "SELECT 1 FROM cache_objects WHERE key=?1 AND size=?2 AND state=1",
                params![key, size],
                |_| Ok(()),
            )
            .optional()
            .map(|value| value.is_some())
            .map_err(sql_error)
    }

    pub fn touch(&mut self, key: &str) -> io::Result<()> {
        if self.order.touch() == Touch::None {
            return Ok(());
        }
        let slot = self
            .connection
            .query_row(
                "SELECT slot FROM cache_objects WHERE key=?1 AND state=1",
                params![key],
                |row| row.get::<_, i64>(0),
            )
            .optional()
            .map_err(sql_error)?;
        let Some(slot) = slot else {
            return Ok(());
        };
        let slot = usize::try_from(from_sql_u64(slot)?)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "slot exceeds usize"))?;
        let Some(state) = self.policy.slots.get_mut(slot) else {
            return Ok(());
        };
        match self.order.touch() {
            Touch::None => {}
            Touch::SetRefBit => state.reference = true,
            Touch::BumpFrequency => state.frequency = state.frequency.saturating_add(1),
        }
        Ok(())
    }

    pub fn reserve(
        &mut self,
        key: &str,
        size: u64,
        capacity: u64,
    ) -> io::Result<(Reservation, Vec<Victim>)> {
        let existing = self
            .connection
            .query_row(
                "SELECT state, size, access_epoch, insert_seq, slot FROM cache_objects WHERE key=?1",
                params![key],
                |row| {
                    Ok((
                        row.get::<_, i64>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                        row.get::<_, i64>(3)?,
                        row.get::<_, i64>(4)?,
                    ))
                },
            )
            .optional()
            .map_err(sql_error)?;
        if existing.is_some_and(|(state, stored_size, _, _, _)| {
            state == STATE_RESIDENT && stored_size == i64::try_from(size).unwrap_or(-1)
        }) {
            return Ok((Reservation::Resident, Vec::new()));
        }
        if existing.is_some_and(|(state, stored_size, _, _, _)| {
            state == STATE_PENDING && stored_size == i64::try_from(size).unwrap_or(-1)
        }) {
            return Ok((Reservation::Pending, Vec::new()));
        }

        let policy_before = self.policy.clone();
        let mut was_evicted = existing.is_some_and(|(state, _, _, _, _)| state == STATE_EVICTED);
        let clear_existing = existing
            .is_some_and(|(state, _, _, _, _)| matches!(state, STATE_PENDING | STATE_RESIDENT));
        let mut planned_used = self.used_bytes;
        let mut victims = Vec::new();
        if let Some((state, stored_size, access, _, slot)) = existing
            && matches!(state, STATE_PENDING | STATE_RESIDENT)
        {
            let stored_size = from_sql_u64(stored_size)?;
            planned_used = planned_used.saturating_sub(stored_size);
            if state == STATE_RESIDENT {
                was_evicted = true;
                victims.push(Victim {
                    key: key.to_owned(),
                    size: stored_size,
                    access: from_sql_u64(access)?,
                });
                self.clear_slot(from_sql_u64(slot)?)?;
            }
        }
        if planned_used > capacity.saturating_sub(size) {
            let sql = match self.order {
                EvictionOrder::LeastRecentlySelectedGeneration => {
                    "SELECT key,size,access_epoch,insert_seq,slot FROM cache_objects WHERE state=1 AND key<>?1 ORDER BY access_epoch,key"
                }
                _ => {
                    "SELECT key,size,access_epoch,insert_seq,slot FROM cache_objects WHERE state=1 AND key<>?1 ORDER BY insert_seq,key"
                }
            };
            let mut statement = self.connection.prepare(sql).map_err(sql_error)?;
            let rows = statement
                .query_map(params![key], |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                        row.get::<_, i64>(3)?,
                        row.get::<_, i64>(4)?,
                    ))
                })
                .map_err(sql_error)?;
            let mut candidates = Vec::new();
            for row in rows {
                let (victim_key, victim_size, access, seq, slot) = row.map_err(sql_error)?;
                candidates.push((
                    victim_key,
                    from_sql_u64(victim_size)?,
                    from_sql_u64(access)?,
                    from_sql_u64(seq)?,
                    from_sql_u64(slot)?,
                ));
            }
            drop(statement);
            for (victim_key, victim_size, access, seq, slot) in self.policy_order(candidates) {
                planned_used = planned_used.saturating_sub(victim_size);
                victims.push(Victim {
                    key: victim_key,
                    size: victim_size,
                    access,
                });
                if self.order == EvictionOrder::ClockSecondChance {
                    self.policy.clock_hand = seq.saturating_add(1);
                }
                self.clear_slot(slot)?;
                if planned_used <= capacity.saturating_sub(size) {
                    break;
                }
            }
        }
        if planned_used > capacity.saturating_sub(size) {
            self.policy = policy_before;
            return Err(io::Error::new(
                io::ErrorKind::StorageFull,
                "cache limit reached",
            ));
        }

        if clear_existing {
            self.connection
                .execute(
                    "UPDATE cache_objects SET state=2 WHERE key=?1",
                    params![key],
                )
                .map_err(sql_error)?;
        }
        for victim in victims.iter().filter(|victim| victim.key != key) {
            self.connection
                .execute(
                    "UPDATE cache_objects SET state=2 WHERE key=?1",
                    params![&victim.key],
                )
                .map_err(sql_error)?;
        }
        self.used_bytes = planned_used;
        let (insert_seq, slot) = self.allocate_slot(key)?;
        self.connection
            .execute(
                "INSERT INTO cache_objects(key,size,access_epoch,state,insert_seq,slot)
				 VALUES(?1,?2,?3,0,?4,?5)
				 ON CONFLICT(key) DO UPDATE SET
				   size=excluded.size,
				   access_epoch=excluded.access_epoch,
				   insert_seq=excluded.insert_seq,
				   slot=excluded.slot,
				   state=0",
                params![
                    key,
                    to_sql_i64(size)?,
                    to_sql_i64(self.epoch)?,
                    to_sql_i64(insert_seq)?,
                    to_sql_i64(slot)?
                ],
            )
            .map_err(sql_error)?;
        self.used_bytes = self.used_bytes.saturating_add(size);
        self.persist_used_if_autocommit()?;
        Ok((
            if was_evicted {
                Reservation::Refetch
            } else {
                Reservation::Pending
            },
            victims,
        ))
    }

    fn policy_order(
        &mut self,
        mut rows: Vec<(String, u64, u64, u64, u64)>,
    ) -> Vec<(String, u64, u64, u64, u64)> {
        match self.order {
            EvictionOrder::LeastRecentlySelectedGeneration => rows,
            EvictionOrder::ClockSecondChance => {
                rows.sort_by_key(|row| (row.3 < self.policy.clock_hand, row.3, row.0.clone()));
                let mut cold = Vec::new();
                let mut hot = Vec::new();
                for row in rows {
                    self.policy
                        .slots
                        .resize_with(row.4 as usize + 1, SlotState::default);
                    let state = &mut self.policy.slots[row.4 as usize];
                    if state.reference {
                        state.reference = false;
                        hot.push(row);
                    } else {
                        cold.push(row);
                    }
                }
                cold.extend(hot);
                cold
            }
            EvictionOrder::ApproxLeastFrequentlyUsed => {
                rows.sort_by_key(|row| {
                    let frequency = self
                        .policy
                        .slots
                        .get(row.4 as usize)
                        .map_or(0, |state| state.frequency);
                    (frequency, row.3, row.0.clone())
                });
                rows
            }
        }
    }

    fn clear_slot(&mut self, slot: u64) -> io::Result<()> {
        let slot = usize::try_from(slot)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "slot exceeds usize"))?;
        if let Some(state) = self.policy.slots.get_mut(slot) {
            *state = SlotState::default();
        }
        Ok(())
    }

    fn allocate_slot(&mut self, key: &str) -> io::Result<(u64, u64)> {
        let next = self
            .connection
            .query_row(
                "SELECT next_insert_seq FROM cache_meta WHERE singleton=1",
                [],
                |row| row.get::<_, i64>(0),
            )
            .map_err(sql_error)
            .and_then(from_sql_u64)?;
        self.connection
            .execute(
                "UPDATE cache_meta SET next_insert_seq=?1 WHERE singleton=1",
                params![to_sql_i64(next.saturating_add(1))?],
            )
            .map_err(sql_error)?;
        let slot = self
            .policy
            .slots
            .iter()
            .position(|state| state.key.is_none())
            .unwrap_or(self.policy.slots.len());
        self.policy.slots.resize_with(slot + 1, SlotState::default);
        self.policy.slots[slot] = SlotState {
            key: Some(key.to_owned()),
            reference: false,
            frequency: 0,
        };
        Ok((next, slot as u64))
    }

    pub fn finish(&mut self, key: &str, size: u64) -> io::Result<()> {
        let changed = self
            .connection
            .execute(
                "UPDATE cache_objects SET size=?2, state=1, access_epoch=?3
				 WHERE key=?1 AND state=0",
                params![key, to_sql_i64(size)?, to_sql_i64(self.epoch)?],
            )
            .map_err(sql_error)?;
        if changed != 1 {
            return Err(io::Error::other("cache reservation disappeared"));
        }
        Ok(())
    }

    pub fn release(&mut self, key: &str) -> io::Result<()> {
        let released = self
            .connection
            .query_row(
                "SELECT size FROM cache_objects WHERE key=?1 AND state=0",
                params![key],
                |row| row.get::<_, i64>(0),
            )
            .optional()
            .map_err(sql_error)?;
        self.connection
            .execute(
                "UPDATE cache_objects SET state=2 WHERE key=?1 AND state=0",
                params![key],
            )
            .map_err(sql_error)?;
        if let Some(size) = released {
            if let Some(slot) = self
                .connection
                .query_row(
                    "SELECT slot FROM cache_objects WHERE key=?1",
                    params![key],
                    |row| row.get::<_, i64>(0),
                )
                .optional()
                .map_err(sql_error)?
            {
                self.clear_slot(from_sql_u64(slot)?)?;
            }
            self.used_bytes = self.used_bytes.saturating_sub(from_sql_u64(size)?);
            self.persist_used_if_autocommit()?;
        }
        Ok(())
    }

    pub fn mark_missing(&mut self, key: &str) -> io::Result<()> {
        let missing = self
            .connection
            .query_row(
                "SELECT size FROM cache_objects WHERE key=?1 AND state IN (0,1)",
                params![key],
                |row| row.get::<_, i64>(0),
            )
            .optional()
            .map_err(sql_error)?;
        self.connection
            .execute(
                "UPDATE cache_objects SET state=2 WHERE key=?1",
                params![key],
            )
            .map_err(sql_error)?;
        if let Some(size) = missing {
            if let Some(slot) = self
                .connection
                .query_row(
                    "SELECT slot FROM cache_objects WHERE key=?1",
                    params![key],
                    |row| row.get::<_, i64>(0),
                )
                .optional()
                .map_err(sql_error)?
            {
                self.clear_slot(from_sql_u64(slot)?)?;
            }
            self.used_bytes = self.used_bytes.saturating_sub(from_sql_u64(size)?);
            self.persist_used_if_autocommit()?;
        }
        Ok(())
    }

    pub fn import_resident(&mut self, key: &str, size: u64, access: u64) -> io::Result<()> {
        if self
            .connection
            .query_row(
                "SELECT 1 FROM cache_objects WHERE key=?1",
                params![key],
                |_| Ok(()),
            )
            .optional()
            .map_err(sql_error)?
            .is_some()
        {
            return Ok(());
        }
        let (insert_seq, slot) = self.allocate_slot(key)?;
        let inserted = self
            .connection
            .execute(
                "INSERT OR IGNORE INTO cache_objects(key,size,access_epoch,state,insert_seq,slot)
				 VALUES(?1,?2,?3,1,?4,?5)",
                params![
                    key,
                    to_sql_i64(size)?,
                    to_sql_i64(access)?,
                    to_sql_i64(insert_seq)?,
                    to_sql_i64(slot)?
                ],
            )
            .map_err(sql_error)?;
        if inserted == 1 {
            self.used_bytes = self.used_bytes.saturating_add(size);
        }
        self.epoch = self.epoch.max(access);
        self.connection
            .execute(
                "UPDATE cache_meta SET epoch=?1, used_bytes=?2 WHERE singleton=1",
                params![to_sql_i64(self.epoch)?, to_sql_i64(self.used_bytes)?],
            )
            .map_err(sql_error)?;
        Ok(())
    }

    pub fn pending_keys(&self) -> io::Result<Vec<String>> {
        let mut statement = self
            .connection
            .prepare("SELECT key FROM cache_objects WHERE state=0")
            .map_err(sql_error)?;
        let rows = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(sql_error)?;
        rows.collect::<Result<Vec<_>, _>>().map_err(sql_error)
    }

    /// Resident (state=1) keys in deterministic key order. Introspection/oracle
    /// accessor — an O(rows) scan, never called on the hot path. Mirrors
    /// `pending_keys`. See docs/eviction-policy-design-1.1.md §6.
    pub fn resident_keys(&self) -> io::Result<Vec<String>> {
        let mut statement = self
            .connection
            .prepare("SELECT key FROM cache_objects WHERE state=1 ORDER BY key")
            .map_err(sql_error)?;
        let rows = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(sql_error)?;
        rows.collect::<Result<Vec<_>, _>>().map_err(sql_error)
    }

    /// Full resident+scalar snapshot for the differential oracle: resident
    /// `(key, size, access_epoch)` rows in key order plus `used_bytes` and
    /// `epoch`. O(rows); test/introspection only.
    pub fn snapshot_state(&self) -> io::Result<CatalogSnapshot> {
        let mut statement = self
            .connection
            .prepare("SELECT key,size,access_epoch,insert_seq,slot FROM cache_objects WHERE state=1 ORDER BY key")
            .map_err(sql_error)?;
        let rows = statement
            .query_map([], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, i64>(1)?,
                    row.get::<_, i64>(2)?,
                    row.get::<_, i64>(3)?,
                    row.get::<_, i64>(4)?,
                ))
            })
            .map_err(sql_error)?;
        let mut resident = Vec::new();
        for row in rows {
            let (key, size, access_epoch, insert_seq, slot) = row.map_err(sql_error)?;
            resident.push(ResidentRow {
                key,
                size: from_sql_u64(size)?,
                access_epoch: from_sql_u64(access_epoch)?,
                insert_seq: from_sql_u64(insert_seq)?,
                slot: u32::try_from(from_sql_u64(slot)?)
                    .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "slot exceeds u32"))?,
            });
        }
        let policy_values =
            resident
                .iter()
                .map(|row| {
                    let value = self.policy.slots.get(row.slot as usize).map_or(0, |state| {
                        match self.order {
                            EvictionOrder::ClockSecondChance => u8::from(state.reference),
                            EvictionOrder::ApproxLeastFrequentlyUsed => state.frequency,
                            EvictionOrder::LeastRecentlySelectedGeneration => 0,
                        }
                    });
                    (row.key.clone(), value)
                })
                .collect();
        Ok(CatalogSnapshot {
            resident,
            used_bytes: self.used_bytes,
            epoch: self.epoch,
            clock_hand: self.policy.clock_hand,
            policy_values,
        })
    }

    pub fn clear_pending(&mut self) -> io::Result<()> {
        let pending_bytes = self
            .connection
            .query_row(
                "SELECT COALESCE(SUM(size),0) FROM cache_objects WHERE state=0",
                [],
                |row| row.get::<_, i64>(0),
            )
            .map_err(sql_error)
            .and_then(from_sql_u64)?;
        self.connection
            .execute("UPDATE cache_objects SET state=2 WHERE state=0", [])
            .map_err(sql_error)?;
        self.used_bytes = self.used_bytes.saturating_sub(pending_bytes);
        self.persist_used_if_autocommit()?;
        Ok(())
    }

    pub fn gauges(&self) -> io::Result<Gauges> {
        self.connection
            .query_row(
                "SELECT
				 COALESCE(SUM(CASE WHEN state=1 THEN 1 ELSE 0 END),0),
				 COALESCE(SUM(CASE WHEN state=1 THEN size ELSE 0 END),0),
				 COALESCE(SUM(CASE WHEN state=0 THEN 1 ELSE 0 END),0)
				 FROM cache_objects",
                [],
                |row| {
                    Ok((
                        row.get::<_, i64>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                    ))
                },
            )
            .map_err(sql_error)
            .and_then(|(objects, bytes, pending)| {
                Ok(Gauges {
                    resident_objects: from_sql_u64(objects)?,
                    resident_bytes: from_sql_u64(bytes)?,
                    pending_objects: from_sql_u64(pending)?,
                })
            })
    }

    fn persist_used_if_autocommit(&self) -> io::Result<()> {
        if self.in_batch {
            Ok(())
        } else {
            self.persist_used_bytes()
        }
    }

    fn persist_used_bytes(&self) -> io::Result<()> {
        self.connection
            .execute(
                "UPDATE cache_meta SET used_bytes=?1,clock_hand=?2 WHERE singleton=1",
                params![
                    to_sql_i64(self.used_bytes)?,
                    to_sql_i64(self.policy.clock_hand)?
                ],
            )
            .map_err(sql_error)?;
        Ok(())
    }
}

fn to_sql_i64(value: u64) -> io::Result<i64> {
    i64::try_from(value)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "value exceeds SQLite INTEGER"))
}

fn table_columns(connection: &Connection, table: &str) -> io::Result<Vec<String>> {
    let mut statement = connection
        .prepare(&format!("PRAGMA table_info({table})"))
        .map_err(sql_error)?;
    statement
        .query_map([], |row| row.get::<_, String>(1))
        .map_err(sql_error)?
        .collect::<Result<Vec<_>, _>>()
        .map_err(sql_error)
}

fn from_sql_u64(value: i64) -> io::Result<u64> {
    u64::try_from(value)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "negative SQLite cache value"))
}

fn sql_error(error: rusqlite::Error) -> io::Error {
    io::Error::other(format!("cache catalog: {error}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn failed_reservation_does_not_consume_partial_victims() {
        let root = std::env::temp_dir().join(format!(
            "oxfs-catalog-reserve-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&root).unwrap();
        let mut catalog =
            Catalog::open(&root.join("catalog.sqlite"), EvictionOrder::default()).unwrap();
        catalog.import_resident("victim", 4, 1).unwrap();
        catalog.begin_batch().unwrap();
        catalog.reserve("inflight", 6, 10).unwrap();

        let error = catalog.reserve("incoming", 7, 10).unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::StorageFull);
        let gauges = catalog.gauges().unwrap();
        assert_eq!(gauges.resident_objects, 1);
        assert_eq!(gauges.resident_bytes, 4);
        assert_eq!(gauges.pending_objects, 1);

        catalog.rollback().unwrap();
        drop(catalog);
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn opens_and_migrates_catalogs_with_legacy_pin_epochs() {
        let root = std::env::temp_dir().join(format!(
            "oxfs-catalog-pin-migration-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&root).unwrap();
        let path = root.join("catalog.sqlite");
        let connection = Connection::open(&path).unwrap();
        connection
            .execute_batch(
                "CREATE TABLE cache_meta (
                   singleton INTEGER PRIMARY KEY,
                   epoch INTEGER NOT NULL,
                   used_bytes INTEGER NOT NULL
                 );
                 INSERT INTO cache_meta VALUES(1, 7, 4);
                 CREATE TABLE cache_objects (
                   key TEXT PRIMARY KEY,
                   size INTEGER NOT NULL,
                   access_epoch INTEGER NOT NULL,
                   pin_epoch INTEGER NOT NULL,
                   state INTEGER NOT NULL
                 ) WITHOUT ROWID;
                 INSERT INTO cache_objects VALUES('resident', 4, 2, 7, 1);
                 CREATE INDEX cache_objects_evict
                   ON cache_objects(state, pin_epoch, access_epoch, key);",
            )
            .unwrap();
        drop(connection);

        let catalog = Catalog::open(&path, EvictionOrder::default()).unwrap();
        assert_eq!(catalog.gauges().unwrap().resident_bytes, 4);
        let columns: Vec<String> = catalog
            .connection
            .prepare("PRAGMA table_info(cache_objects)")
            .unwrap()
            .query_map([], |row| row.get(1))
            .unwrap()
            .collect::<Result<_, _>>()
            .unwrap();
        assert!(!columns.iter().any(|column| column == "pin_epoch"));
        drop(catalog);
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn use_based_policies_protect_a_touched_resident() {
        for order in [
            EvictionOrder::ClockSecondChance,
            EvictionOrder::ApproxLeastFrequentlyUsed,
        ] {
            let root = std::env::temp_dir().join(format!(
                "oxfs-catalog-policy-{order:?}-{}-{}",
                std::process::id(),
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_nanos()
            ));
            fs::create_dir_all(&root).unwrap();
            let mut catalog = Catalog::open(&root.join("catalog.sqlite"), order).unwrap();
            catalog.import_resident("a", 4, 1).unwrap();
            catalog.import_resident("b", 4, 1).unwrap();
            catalog.touch("a").unwrap();

            let (_, victims) = catalog.reserve("c", 4, 8).unwrap();
            assert_eq!(
                victims
                    .iter()
                    .map(|victim| victim.key.as_str())
                    .collect::<Vec<_>>(),
                vec!["b"]
            );
            assert!(catalog.is_resident("a", 4).unwrap());
            fs::remove_dir_all(root).ok();
        }
    }
}

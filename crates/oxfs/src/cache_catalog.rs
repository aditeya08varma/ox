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
}

impl Catalog {
    pub fn open(path: &Path) -> io::Result<Self> {
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
        let (epoch, used_bytes) = connection
            .query_row(
                "SELECT epoch, used_bytes FROM cache_meta WHERE singleton=1",
                [],
                |row| Ok((row.get::<_, i64>(0)?, row.get::<_, i64>(1)?)),
            )
            .map_err(sql_error)?;
        Ok(Self {
            connection,
            in_batch: false,
            epoch: from_sql_u64(epoch)?,
            used_bytes: from_sql_u64(used_bytes)?,
            batch_used_start: None,
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
        Ok(started.elapsed().as_micros())
    }

    pub fn rollback(&mut self) -> io::Result<()> {
        if self.in_batch {
            self.connection
                .execute_batch("ROLLBACK")
                .map_err(sql_error)?;
            self.in_batch = false;
            self.used_bytes = self.batch_used_start.take().unwrap_or(self.used_bytes);
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

    pub fn reserve(
        &mut self,
        key: &str,
        size: u64,
        capacity: u64,
    ) -> io::Result<(Reservation, Vec<Victim>)> {
        let existing = self
            .connection
            .query_row(
                "SELECT state, size, access_epoch FROM cache_objects WHERE key=?1",
                params![key],
                |row| {
                    Ok((
                        row.get::<_, i64>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                    ))
                },
            )
            .optional()
            .map_err(sql_error)?;
        if existing.is_some_and(|(state, stored_size, _)| {
            state == STATE_RESIDENT && stored_size == i64::try_from(size).unwrap_or(-1)
        }) {
            return Ok((Reservation::Resident, Vec::new()));
        }
        if existing.is_some_and(|(state, stored_size, _)| {
            state == STATE_PENDING && stored_size == i64::try_from(size).unwrap_or(-1)
        }) {
            return Ok((Reservation::Pending, Vec::new()));
        }

        let mut was_evicted = existing.is_some_and(|(state, _, _)| state == STATE_EVICTED);
        let clear_existing =
            existing.is_some_and(|(state, _, _)| matches!(state, STATE_PENDING | STATE_RESIDENT));
        let mut planned_used = self.used_bytes;
        let mut victims = Vec::new();
        if let Some((state, stored_size, access)) = existing
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
            }
        }
        if planned_used > capacity.saturating_sub(size) {
            let mut statement = self
                .connection
                .prepare_cached(
                    "SELECT key, size, access_epoch FROM cache_objects
					 WHERE state=1 AND key<>?1
					 ORDER BY access_epoch, key",
                )
                .map_err(sql_error)?;
            let rows = statement
                .query_map(params![key], |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, i64>(2)?,
                    ))
                })
                .map_err(sql_error)?;
            for row in rows {
                let (victim_key, victim_size, access) = row.map_err(sql_error)?;
                let victim_size = from_sql_u64(victim_size)?;
                planned_used = planned_used.saturating_sub(victim_size);
                victims.push(Victim {
                    key: victim_key,
                    size: victim_size,
                    access: from_sql_u64(access)?,
                });
                if planned_used <= capacity.saturating_sub(size) {
                    break;
                }
            }
        }
        if planned_used > capacity.saturating_sub(size) {
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
        self.connection
            .execute(
                "INSERT INTO cache_objects(key,size,access_epoch,state)
				 VALUES(?1,?2,?3,0)
				 ON CONFLICT(key) DO UPDATE SET
				   size=excluded.size,
				   access_epoch=excluded.access_epoch,
				   state=0",
                params![key, to_sql_i64(size)?, to_sql_i64(self.epoch)?],
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
            self.used_bytes = self.used_bytes.saturating_sub(from_sql_u64(size)?);
            self.persist_used_if_autocommit()?;
        }
        Ok(())
    }

    pub fn import_resident(&mut self, key: &str, size: u64, access: u64) -> io::Result<()> {
        let inserted = self
            .connection
            .execute(
                "INSERT OR IGNORE INTO cache_objects(key,size,access_epoch,state)
				 VALUES(?1,?2,?3,1)",
                params![key, to_sql_i64(size)?, to_sql_i64(access)?],
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
                "UPDATE cache_meta SET used_bytes=?1 WHERE singleton=1",
                params![to_sql_i64(self.used_bytes)?],
            )
            .map_err(sql_error)?;
        Ok(())
    }
}

fn to_sql_i64(value: u64) -> io::Result<i64> {
    i64::try_from(value)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "value exceeds SQLite INTEGER"))
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
        let mut catalog = Catalog::open(&root.join("catalog.sqlite")).unwrap();
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

        let catalog = Catalog::open(&path).unwrap();
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
}

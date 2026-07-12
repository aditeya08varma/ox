use std::fs::{self, File, OpenOptions};
use std::io::{self, Write};
use std::path::Path;
use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

#[derive(Clone, Copy, Debug)]
pub enum ObservationKind {
    Open,
    Read,
    Dismiss,
}

pub struct ObservationLog {
    file: Mutex<File>,
}

impl ObservationLog {
    pub fn open(state_dir: &Path) -> io::Result<Self> {
        fs::create_dir_all(state_dir)?;
        let file = OpenOptions::new()
            .create(true)
            .append(true)
            .open(state_dir.join("access.jsonl"))?;
        Ok(Self {
            file: Mutex::new(file),
        })
    }

    pub fn append(
        &self,
        kind: ObservationKind,
        inode: u64,
        path: &str,
        offset: u64,
        bytes: usize,
    ) -> io::Result<()> {
        let timestamp_ms = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_millis();
        let kind = match kind {
            ObservationKind::Open => "open",
            ObservationKind::Read => "read",
            ObservationKind::Dismiss => "dismiss",
        };
        let escaped = path.replace('\\', "\\\\").replace('"', "\\\"");
        let mut file = self
            .file
            .lock()
            .map_err(|_| io::Error::other("observation log poisoned"))?;
        writeln!(
            file,
            "{{\"ts_ms\":{timestamp_ms},\"kind\":\"{kind}\",\"inode\":{inode},\"path\":\"{escaped}\",\"offset\":{offset},\"bytes\":{bytes}}}"
        )
    }
}

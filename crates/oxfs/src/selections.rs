use crate::content::ContentRef;
use crate::manifest::{Manifest, ManifestEntry};
use std::collections::BTreeMap;
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::path::{Path, PathBuf};

/// Transactional persistence for complete per-Session working sets.
///
/// This is deliberately a small regenerable format rather than a second source
/// of truth. Unknown format versions or malformed rows fail closed; the caller
/// can discard it and request fresh selections from the server.
pub struct SelectionStore {
    path: PathBuf,
}

impl SelectionStore {
    pub fn open(state_dir: &Path) -> io::Result<Self> {
        fs::create_dir_all(state_dir)?;
        Ok(Self {
            path: state_dir.join("selections.v1"),
        })
    }

    pub fn load(&self) -> io::Result<BTreeMap<String, Manifest>> {
        let file = match File::open(&self.path) {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(BTreeMap::new()),
            Err(error) => return Err(error),
        };
        let mut sessions = BTreeMap::new();
        let mut current: Option<Manifest> = None;
        for line in BufReader::new(file).lines() {
            let line = line?;
            let fields: Vec<_> = line.split('\t').collect();
            match fields.first().copied() {
                Some("S") if fields.len() == 3 => {
                    finish(&mut sessions, current.take())?;
                    current = Some(Manifest {
                        generation: parse_u64(fields[1])?,
                        session_id: decode(fields[2])?,
                        entries: vec![],
                    });
                }
                Some("E") if fields.len() == 11 => {
                    let manifest = current
                        .as_mut()
                        .ok_or_else(|| invalid("entry before session"))?;
                    let content = ContentRef::new(
                        decode(fields[6])?,
                        decode(fields[7])?,
                        decode(fields[8])?,
                        parse_u64(fields[9])?,
                    )
                    .map_err(|_| invalid("bad content reference"))?;
                    manifest.entries.push(
                        ManifestEntry::new(
                            decode(fields[1])?,
                            decode(fields[2])?,
                            decode(fields[3])?,
                            parse_u32(fields[4])?,
                            parse_u64(fields[5])?,
                            content,
                            decode(fields[10])?,
                        )
                        .map_err(|_| invalid("bad selection path"))?,
                    );
                }
                _ => return Err(invalid("bad selection row")),
            }
        }
        finish(&mut sessions, current)?;
        for manifest in sessions.values() {
            manifest
                .validate()
                .map_err(|_| invalid("invalid persisted manifest"))?;
        }
        Ok(sessions)
    }

    pub fn replace(&self, sessions: &BTreeMap<String, Manifest>) -> io::Result<()> {
        let temp = self
            .path
            .with_extension(format!("tmp-{}", std::process::id()));
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temp)?;
        let result = (|| {
            for manifest in sessions.values() {
                writeln!(
                    file,
                    "S\t{}\t{}",
                    manifest.generation,
                    encode(&manifest.session_id)
                )?;
                for entry in &manifest.entries {
                    writeln!(
                        file,
                        "E\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}",
                        encode(entry.path.as_str()),
                        encode(&entry.source_id),
                        encode(&entry.source_kind),
                        entry.mode,
                        entry.mtime_secs,
                        encode(&entry.content.tenant),
                        encode(&entry.content.algorithm),
                        encode(&entry.content.digest),
                        entry.content.size,
                        encode(&entry.reason)
                    )?;
                }
            }
            file.flush()?;
            file.sync_all()?;
            drop(file);
            fs::rename(&temp, &self.path)?;
            File::open(self.path.parent().expect("selection path parent"))?.sync_all()
        })();
        if result.is_err() {
            let _ = fs::remove_file(temp);
        }
        result
    }
}

fn finish(sessions: &mut BTreeMap<String, Manifest>, manifest: Option<Manifest>) -> io::Result<()> {
    if let Some(manifest) = manifest
        && sessions
            .insert(manifest.session_id.clone(), manifest)
            .is_some()
    {
        return Err(invalid("duplicate session"));
    }
    Ok(())
}
fn parse_u64(value: &str) -> io::Result<u64> {
    value.parse().map_err(|_| invalid("bad integer"))
}
fn parse_u32(value: &str) -> io::Result<u32> {
    value.parse().map_err(|_| invalid("bad integer"))
}
fn invalid(message: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message)
}
fn encode(value: &str) -> String {
    let mut output = String::with_capacity(value.len() * 2);
    for byte in value.as_bytes() {
        use std::fmt::Write as _;
        write!(output, "{byte:02x}").expect("write to String");
    }
    output
}
fn decode(value: &str) -> io::Result<String> {
    if !value.len().is_multiple_of(2) {
        return Err(invalid("bad hex string"));
    }
    let mut bytes = Vec::with_capacity(value.len() / 2);
    for pair in value.as_bytes().chunks_exact(2) {
        let pair = std::str::from_utf8(pair).map_err(|_| invalid("bad hex string"))?;
        bytes.push(u8::from_str_radix(pair, 16).map_err(|_| invalid("bad hex string"))?);
    }
    String::from_utf8(bytes).map_err(|_| invalid("selection string is not UTF-8"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{SystemTime, UNIX_EPOCH};
    #[test]
    fn round_trip() {
        let dir = std::env::temp_dir().join(format!(
            "oxfs-selections-{}",
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let store = SelectionStore::open(&dir).unwrap();
        let content = ContentRef::new("t", "sha256", "aa", 7).unwrap();
        let manifest = Manifest {
            session_id: "s\t1".into(),
            generation: 9,
            entries: vec![
                ManifestEntry::new("a/b", "id", "Session", 0o444, 5, content, "why\nnow").unwrap(),
            ],
        };
        store
            .replace(&BTreeMap::from([(manifest.session_id.clone(), manifest)]))
            .unwrap();
        let loaded = store.load().unwrap();
        assert_eq!(loaded["s\t1"].entries[0].reason, "why\nnow");
        fs::remove_dir_all(dir).unwrap();
    }
}

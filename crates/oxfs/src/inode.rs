use std::collections::BTreeMap;
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::path::{Path, PathBuf};

pub const ROOT_INODE: u64 = 1;

/// Append-only stable path-to-inode allocator.
///
/// Removed paths retain their number so a later reappearance does not create
/// avoidable kernel cache churn. Compaction can be added when measurements say
/// the tiny mapping warrants it.
pub struct InodeTable {
    path: PathBuf,
    by_path: BTreeMap<String, u64>,
    next: u64,
}

impl InodeTable {
    pub fn open(state_dir: &Path) -> io::Result<Self> {
        fs::create_dir_all(state_dir)?;
        let path = state_dir.join("inodes.v1");
        let mut by_path = BTreeMap::new();
        let mut next = ROOT_INODE + 1;
        match File::open(&path) {
            Ok(file) => {
                for line in BufReader::new(file).lines() {
                    let line = line?;
                    let Some((number, encoded)) = line.split_once('\t') else {
                        return Err(io::Error::new(io::ErrorKind::InvalidData, "bad inode row"));
                    };
                    let number: u64 = number.parse().map_err(|_| {
                        io::Error::new(io::ErrorKind::InvalidData, "bad inode number")
                    })?;
                    let path = unescape(encoded)?;
                    if number <= ROOT_INODE || by_path.insert(path, number).is_some() {
                        return Err(io::Error::new(
                            io::ErrorKind::InvalidData,
                            "duplicate inode row",
                        ));
                    }
                    next = next.max(number.saturating_add(1));
                }
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
        Ok(Self {
            path,
            by_path,
            next,
        })
    }

    pub fn inode_for(&mut self, path: &str) -> io::Result<u64> {
        if path.is_empty() {
            return Ok(ROOT_INODE);
        }
        if let Some(number) = self.by_path.get(path) {
            return Ok(*number);
        }
        let number = self.next;
        self.next = self
            .next
            .checked_add(1)
            .ok_or_else(|| io::Error::other("inode number exhausted"))?;
        let mut file = OpenOptions::new()
            .create(true)
            .append(true)
            .open(&self.path)?;
        writeln!(file, "{number}\t{}", escape(path))?;
        file.sync_data()?;
        self.by_path.insert(path.into(), number);
        Ok(number)
    }
}

fn escape(value: &str) -> String {
    value
        .replace('%', "%25")
        .replace('\t', "%09")
        .replace('\n', "%0a")
}

fn unescape(value: &str) -> io::Result<String> {
    let bytes = value.as_bytes();
    let mut output = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index] != b'%' {
            output.push(bytes[index]);
            index += 1;
            continue;
        }
        if index + 2 >= bytes.len() {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "bad inode path escape",
            ));
        }
        let encoded = std::str::from_utf8(&bytes[index + 1..index + 3])
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "bad inode path escape"))?;
        let byte = u8::from_str_radix(encoded, 16)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "bad inode path escape"))?;
        output.push(byte);
        index += 3;
    }
    String::from_utf8(output)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "inode path is not UTF-8"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn inode_survives_reopen() {
        let dir = std::env::temp_dir().join(format!(
            "oxfs-inodes-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let first = InodeTable::open(&dir).unwrap().inode_for("a/b").unwrap();
        let second = InodeTable::open(&dir).unwrap().inode_for("a/b").unwrap();
        assert_eq!(first, second);
        fs::remove_dir_all(dir).unwrap();
    }
}

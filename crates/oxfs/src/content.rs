use std::fmt;
use std::io::{self, Read, Write};

/// Immutable, tenant-scoped identity for a complete object.
///
/// The digest is SHA-256 for the LFS adapter. Keeping the algorithm explicit
/// lets a future Lore adapter use BLAKE3 without confusing the two namespaces.
#[derive(Clone, Debug, Eq, Hash, Ord, PartialEq, PartialOrd)]
pub struct ContentRef {
    pub tenant: String,
    pub algorithm: String,
    pub digest: String,
    pub size: u64,
}

impl ContentRef {
    /// Build a content reference from complete bytes using oxFS's canonical
    /// SHA-256 implementation.
    pub fn for_sha256(tenant: impl Into<String>, bytes: &[u8]) -> Self {
        let mut hash = crate::sha256::Sha256::new();
        hash.update(bytes);
        Self {
            tenant: tenant.into(),
            algorithm: "sha256".into(),
            digest: hash.hex_digest(),
            size: bytes.len() as u64,
        }
    }

    /// Build a content reference without holding the complete object in memory.
    pub fn for_sha256_reader(tenant: impl Into<String>, mut input: impl Read) -> io::Result<Self> {
        let mut hash = crate::sha256::Sha256::new();
        let mut size = 0u64;
        let mut buffer = [0u8; 64 * 1024];
        loop {
            let read = input.read(&mut buffer)?;
            if read == 0 {
                break;
            }
            hash.update(&buffer[..read]);
            size = size.saturating_add(read as u64);
        }
        Ok(Self {
            tenant: tenant.into(),
            algorithm: "sha256".into(),
            digest: hash.hex_digest(),
            size,
        })
    }

    pub fn new(
        tenant: impl Into<String>,
        algorithm: impl Into<String>,
        digest: impl Into<String>,
        size: u64,
    ) -> Result<Self, FetchError> {
        let value = Self {
            tenant: tenant.into(),
            algorithm: algorithm.into(),
            digest: digest.into(),
            size,
        };
        if value.tenant.is_empty() || value.algorithm.is_empty() || value.digest.is_empty() {
            return Err(FetchError::InvalidReference);
        }
        if !value
            .algorithm
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
        {
            return Err(FetchError::InvalidReference);
        }
        if !value.digest.bytes().all(|byte| byte.is_ascii_hexdigit()) {
            return Err(FetchError::InvalidReference);
        }
        Ok(value)
    }

    pub fn storage_key(&self) -> String {
        format!("{}-{}", self.algorithm, self.digest.to_ascii_lowercase())
    }
}

/// Storage-neutral source of immutable complete objects.
pub trait ContentSource: Send + Sync + 'static {
    fn fetch(&self, reference: &ContentRef, output: &mut dyn Write) -> Result<(), FetchError>;
}

#[derive(Debug)]
pub enum FetchError {
    InvalidReference,
    NotFound,
    Cancelled,
    Io(io::Error),
    WrongSize { expected: u64, actual: u64 },
    WrongDigest { expected: String, actual: String },
}

impl fmt::Display for FetchError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidReference => write!(f, "invalid content reference"),
            Self::NotFound => write!(f, "content not found"),
            Self::Cancelled => write!(f, "fetch cancelled"),
            Self::Io(error) => write!(f, "content I/O: {error}"),
            Self::WrongSize { expected, actual } => {
                write!(f, "wrong content size: expected {expected}, got {actual}")
            }
            Self::WrongDigest { expected, actual } => {
                write!(f, "wrong content digest: expected {expected}, got {actual}")
            }
        }
    }
}

impl std::error::Error for FetchError {}

impl From<io::Error> for FetchError {
    fn from(value: io::Error) -> Self {
        Self::Io(value)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn streaming_sha256_matches_complete_bytes() {
        let bytes = vec![0x5a; 128 * 1024 + 17];
        let complete = ContentRef::for_sha256("test", &bytes);
        let streaming = ContentRef::for_sha256_reader("test", bytes.as_slice()).unwrap();
        assert_eq!(streaming, complete);
    }
}

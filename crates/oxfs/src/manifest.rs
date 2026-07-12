use crate::content::ContentRef;
use crate::path::{PathError, RelativePath};
use std::collections::BTreeSet;
use std::fmt;

#[derive(Clone, Debug)]
pub struct ManifestEntry {
    pub path: RelativePath,
    pub source_id: String,
    pub source_kind: String,
    pub mode: u32,
    pub mtime_secs: u64,
    pub content: ContentRef,
    pub reason: String,
}

impl ManifestEntry {
    #[allow(clippy::too_many_arguments)]
    pub fn new(
        path: impl Into<String>,
        source_id: impl Into<String>,
        source_kind: impl Into<String>,
        mode: u32,
        mtime_secs: u64,
        content: ContentRef,
        reason: impl Into<String>,
    ) -> Result<Self, PathError> {
        Ok(Self {
            path: RelativePath::parse(path)?,
            source_id: source_id.into(),
            source_kind: source_kind.into(),
            mode: mode & 0o555,
            mtime_secs,
            content,
            reason: reason.into(),
        })
    }
}

#[derive(Clone, Debug)]
pub struct Manifest {
    pub session_id: String,
    pub generation: u64,
    pub entries: Vec<ManifestEntry>,
}

impl Manifest {
    pub fn validate(&self) -> Result<(), ManifestError> {
        if self.session_id.is_empty() {
            return Err(ManifestError::MissingSession);
        }
        let mut paths = BTreeSet::new();
        let mut files = BTreeSet::new();
        for entry in &self.entries {
            if !paths.insert(entry.path.as_str()) {
                return Err(ManifestError::Duplicate(entry.path.as_str().into()));
            }
            let mut prefix = String::new();
            let parts: Vec<_> = entry.path.components().collect();
            for (index, part) in parts.iter().enumerate() {
                if !prefix.is_empty() {
                    prefix.push('/');
                }
                prefix.push_str(part);
                if index + 1 != parts.len() && files.contains(prefix.as_str()) {
                    return Err(ManifestError::Collision(prefix));
                }
            }
            files.insert(entry.path.as_str());
        }
        for file in &files {
            if paths
                .iter()
                .any(|path| path.starts_with(&format!("{file}/")))
            {
                return Err(ManifestError::Collision((*file).into()));
            }
        }
        Ok(())
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum ManifestError {
    MissingSession,
    Duplicate(String),
    Collision(String),
}

impl fmt::Display for ManifestError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::MissingSession => write!(f, "manifest session ID is empty"),
            Self::Duplicate(path) => write!(f, "duplicate manifest path {path}"),
            Self::Collision(path) => write!(f, "file/directory collision at {path}"),
        }
    }
}

impl std::error::Error for ManifestError {}

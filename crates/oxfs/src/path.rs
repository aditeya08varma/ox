use std::fmt;

#[derive(Clone, Debug, Eq, Hash, Ord, PartialEq, PartialOrd)]
pub struct RelativePath(String);

impl RelativePath {
    pub fn parse(value: impl Into<String>) -> Result<Self, PathError> {
        let value = value.into();
        if value.is_empty() || value.starts_with('/') || value.ends_with('/') {
            return Err(PathError::Invalid(value));
        }
        for component in value.split('/') {
            if component.is_empty() || component == "." || component == ".." {
                return Err(PathError::Invalid(value));
            }
            if component.as_bytes().contains(&0) {
                return Err(PathError::Invalid(value));
            }
        }
        if value == ".sageox" || value.starts_with(".sageox/") {
            return Err(PathError::Reserved(value));
        }
        Ok(Self(value))
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }

    pub fn components(&self) -> impl Iterator<Item = &str> {
        self.0.split('/')
    }

    pub fn file_name(&self) -> &str {
        self.0.rsplit('/').next().expect("validated non-empty path")
    }

    pub fn parent(&self) -> Option<&str> {
        self.0.rsplit_once('/').map(|(parent, _)| parent)
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum PathError {
    Invalid(String),
    Reserved(String),
}

impl fmt::Display for PathError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Invalid(path) => write!(f, "invalid relative path {path:?}"),
            Self::Reserved(path) => write!(f, "path collides with reserved namespace: {path:?}"),
        }
    }
}

impl std::error::Error for PathError {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_unsafe_and_reserved_paths() {
        for path in ["", "/a", "a/", "a//b", "a/../b", ".", ".sageox/INDEX.md"] {
            assert!(RelativePath::parse(path).is_err(), "accepted {path:?}");
        }
        assert_eq!(RelativePath::parse("a/b.txt").unwrap().as_str(), "a/b.txt");
    }
}

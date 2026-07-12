use std::fmt;

#[derive(Debug, Clone, Eq, PartialEq)]
pub enum XdrError {
    Truncated,
    LengthOverflow,
    InvalidUtf8,
    TrailingBytes,
}

impl fmt::Display for XdrError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "invalid XDR: {self:?}")
    }
}
impl std::error::Error for XdrError {}

#[derive(Default)]
pub struct Encoder {
    bytes: Vec<u8>,
}
impl Encoder {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn u32(&mut self, value: u32) {
        self.bytes.extend_from_slice(&value.to_be_bytes());
    }
    pub fn u64(&mut self, value: u64) {
        self.bytes.extend_from_slice(&value.to_be_bytes());
    }
    pub fn boolean(&mut self, value: bool) {
        self.u32(u32::from(value));
    }
    pub fn fixed(&mut self, bytes: &[u8]) {
        self.bytes.extend_from_slice(bytes);
        self.pad(bytes.len());
    }
    pub fn opaque(&mut self, bytes: &[u8]) {
        self.u32(bytes.len().try_into().expect("XDR value under 4GiB"));
        self.fixed(bytes);
    }
    pub fn string(&mut self, value: &str) {
        self.opaque(value.as_bytes());
    }
    pub fn into_bytes(self) -> Vec<u8> {
        self.bytes
    }
    fn pad(&mut self, length: usize) {
        let padding = (4 - length % 4) % 4;
        self.bytes.resize(self.bytes.len() + padding, 0);
    }
}

pub struct Decoder<'a> {
    bytes: &'a [u8],
    offset: usize,
}
impl<'a> Decoder<'a> {
    pub fn new(bytes: &'a [u8]) -> Self {
        Self { bytes, offset: 0 }
    }
    pub fn u32(&mut self) -> Result<u32, XdrError> {
        Ok(u32::from_be_bytes(self.take(4)?.try_into().unwrap()))
    }
    pub fn u64(&mut self) -> Result<u64, XdrError> {
        Ok(u64::from_be_bytes(self.take(8)?.try_into().unwrap()))
    }
    pub fn boolean(&mut self) -> Result<bool, XdrError> {
        Ok(self.u32()? != 0)
    }
    pub fn fixed(&mut self, length: usize) -> Result<&'a [u8], XdrError> {
        let result = self.take(length)?;
        self.take((4 - length % 4) % 4)?;
        Ok(result)
    }
    pub fn opaque(&mut self, max: usize) -> Result<&'a [u8], XdrError> {
        let length = self.u32()? as usize;
        if length > max {
            return Err(XdrError::LengthOverflow);
        }
        self.fixed(length)
    }
    pub fn string(&mut self, max: usize) -> Result<&'a str, XdrError> {
        std::str::from_utf8(self.opaque(max)?).map_err(|_| XdrError::InvalidUtf8)
    }
    pub fn remaining(&self) -> usize {
        self.bytes.len() - self.offset
    }
    pub fn finish(self) -> Result<(), XdrError> {
        if self.remaining() == 0 {
            Ok(())
        } else {
            Err(XdrError::TrailingBytes)
        }
    }
    fn take(&mut self, length: usize) -> Result<&'a [u8], XdrError> {
        let end = self
            .offset
            .checked_add(length)
            .ok_or(XdrError::LengthOverflow)?;
        let result = self
            .bytes
            .get(self.offset..end)
            .ok_or(XdrError::Truncated)?;
        self.offset = end;
        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn round_trip() {
        let mut e = Encoder::new();
        e.u32(7);
        e.u64(9);
        e.string("abc");
        e.opaque(&[1, 2, 3, 4, 5]);
        let bytes = e.into_bytes();
        let mut d = Decoder::new(&bytes);
        assert_eq!(d.u32().unwrap(), 7);
        assert_eq!(d.u64().unwrap(), 9);
        assert_eq!(d.string(10).unwrap(), "abc");
        assert_eq!(d.opaque(10).unwrap(), [1, 2, 3, 4, 5]);
        d.finish().unwrap();
    }
}

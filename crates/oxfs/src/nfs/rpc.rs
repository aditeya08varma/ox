use super::xdr::{Decoder, Encoder, XdrError};
use std::io::{self, Read, Write};
use std::net::TcpStream;

pub const CALL: u32 = 0;
pub const REPLY: u32 = 1;
pub const RPC_VERSION: u32 = 2;
pub const MSG_ACCEPTED: u32 = 0;
pub const SUCCESS: u32 = 0;
pub const PROG_UNAVAIL: u32 = 1;
pub const PROG_MISMATCH: u32 = 2;
pub const PROC_UNAVAIL: u32 = 3;
pub const GARBAGE_ARGS: u32 = 4;

#[derive(Debug)]
pub struct Call<'a> {
    pub xid: u32,
    pub program: u32,
    pub version: u32,
    pub procedure: u32,
    pub arguments: &'a [u8],
}
pub fn decode_call(bytes: &[u8]) -> Result<Call<'_>, XdrError> {
    let mut d = Decoder::new(bytes);
    let xid = d.u32()?;
    if d.u32() != Ok(CALL) || d.u32() != Ok(RPC_VERSION) {
        return Err(XdrError::TrailingBytes);
    }
    let program = d.u32()?;
    let version = d.u32()?;
    let procedure = d.u32()?;
    skip_auth(&mut d)?;
    skip_auth(&mut d)?;
    let offset = bytes.len() - d.remaining();
    Ok(Call {
        xid,
        program,
        version,
        procedure,
        arguments: &bytes[offset..],
    })
}
fn skip_auth(d: &mut Decoder<'_>) -> Result<(), XdrError> {
    let _flavor = d.u32()?;
    let _ = d.opaque(400)?;
    Ok(())
}
pub fn accepted(xid: u32, status: u32, body: &[u8]) -> Vec<u8> {
    let mut e = Encoder::new();
    e.u32(xid);
    e.u32(REPLY);
    e.u32(MSG_ACCEPTED);
    e.u32(0);
    e.opaque(&[]);
    e.u32(status);
    let mut out = e.into_bytes();
    out.extend_from_slice(body);
    out
}
pub fn mismatch(xid: u32, low: u32, high: u32) -> Vec<u8> {
    let mut body = Encoder::new();
    body.u32(low);
    body.u32(high);
    accepted(xid, PROG_MISMATCH, &body.into_bytes())
}

pub fn read_record(stream: &mut TcpStream, max: usize) -> io::Result<Option<Vec<u8>>> {
    let mut output = Vec::new();
    loop {
        let mut header = [0u8; 4];
        match stream.read_exact(&mut header) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::UnexpectedEof && output.is_empty() => {
                return Ok(None);
            }
            Err(e) => return Err(e),
        }
        let marker = u32::from_be_bytes(header);
        let last = marker & 0x8000_0000 != 0;
        let length = (marker & 0x7fff_ffff) as usize;
        if output.len().saturating_add(length) > max {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "RPC record too large",
            ));
        }
        let start = output.len();
        output.resize(start + length, 0);
        stream.read_exact(&mut output[start..])?;
        if last {
            return Ok(Some(output));
        }
    }
}
pub fn write_record(stream: &mut TcpStream, bytes: &[u8]) -> io::Result<()> {
    let length: u32 = bytes
        .len()
        .try_into()
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "RPC record too large"))?;
    stream.write_all(&(length | 0x8000_0000).to_be_bytes())?;
    stream.write_all(bytes)?;
    stream.flush()
}

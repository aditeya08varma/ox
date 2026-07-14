//! Minimal macOS-facing ONC RPC, mountd v3, and NFSv3 adapter.
//!
//! It intentionally implements the read-only subset required by oxFS. Every
//! mutating NFS procedure returns `NFS3ERR_ROFS`; unsupported procedures return
//! an accepted RPC `PROC_UNAVAIL`, never a dropped connection.

mod rpc;
mod server;
mod xdr;

pub use server::{NfsServer, ServerConfig, ServerHandle};
pub use xdr::{Decoder, Encoder, XdrError};

pub const NFS_PROGRAM: u32 = 100003;
pub const NFS_VERSION: u32 = 3;
pub const MOUNT_PROGRAM: u32 = 100005;
pub const MOUNT_VERSION: u32 = 3;

pub const NFS3_OK: u32 = 0;
pub const NFS3ERR_NOENT: u32 = 2;
pub const NFS3ERR_IO: u32 = 5;
pub const NFS3ERR_ACCES: u32 = 13;
pub const NFS3ERR_NOTDIR: u32 = 20;
pub const NFS3ERR_ISDIR: u32 = 21;
pub const NFS3ERR_INVAL: u32 = 22;
pub const NFS3ERR_ROFS: u32 = 30;
pub const NFS3ERR_NAMETOOLONG: u32 = 63;
pub const NFS3ERR_BADHANDLE: u32 = 10001;

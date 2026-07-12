use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitStatus, Stdio};
use std::thread;
use std::time::{Duration, Instant};

const READY_TIMEOUT: Duration = Duration::from_secs(10);

#[derive(Debug, PartialEq, Eq)]
pub struct MountState {
    pub pid: u32,
    pub port: u16,
    pub mountpoint: PathBuf,
}

impl MountState {
    pub fn encode(&self) -> String {
        format!(
            "pid={}\nport={}\nmountpoint={}\n",
            self.pid,
            self.port,
            self.mountpoint.display()
        )
    }

    pub fn decode(value: &str) -> io::Result<Self> {
        let field = |name: &str| {
            value
                .lines()
                .find_map(|line| line.strip_prefix(&format!("{name}=")))
                .ok_or_else(|| {
                    io::Error::new(io::ErrorKind::InvalidData, format!("missing {name}"))
                })
        };
        Ok(Self {
            pid: field("pid")?.parse().map_err(invalid_state)?,
            port: field("port")?.parse().map_err(invalid_state)?,
            mountpoint: PathBuf::from(field("mountpoint")?),
        })
    }
}

fn invalid_state(error: impl std::fmt::Display) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, error.to_string())
}

pub fn state_path(state_dir: &Path) -> PathBuf {
    state_dir.join("mount.state")
}

pub fn publish_ready(path: &Path, state: &MountState) -> io::Result<()> {
    let temporary = path.with_extension("state.tmp");
    fs::write(&temporary, state.encode())?;
    fs::rename(temporary, path)
}

fn wait_ready(path: &Path, child: &mut Child) -> io::Result<MountState> {
    let deadline = Instant::now() + READY_TIMEOUT;
    loop {
        match fs::read_to_string(path) {
            Ok(value) => return MountState::decode(&value),
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
        if let Some(status) = child.try_wait()? {
            return Err(io::Error::other(format!(
                "oxFS server exited before ready: {status}"
            )));
        }
        if Instant::now() >= deadline {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "timed out waiting for oxFS server",
            ));
        }
        thread::sleep(Duration::from_millis(25));
    }
}

pub fn mount_demo(state_dir: &Path, object_dir: &Path, mountpoint: &Path) -> io::Result<()> {
    ensure_unprivileged()?;
    fs::create_dir_all(state_dir)?;
    fs::create_dir_all(mountpoint)?;
    let state_file = state_path(state_dir);
    match fs::remove_file(&state_file) {
        Ok(()) => {}
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error),
    }
    let executable = std::env::current_exe()?;
    let mut child = Command::new(executable)
        .arg("serve-demo")
        .arg(state_dir)
        .arg(object_dir)
        .arg("127.0.0.1:0")
        .arg("--ready-file")
        .arg(&state_file)
        .arg("--mountpoint")
        .arg(mountpoint)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::inherit())
        .spawn()?;
    let ready = match wait_ready(&state_file, &mut child) {
        Ok(ready) => ready,
        Err(error) => {
            let _ = child.kill();
            return Err(error);
        }
    };
    let status = mount_command(&ready).status()?;
    if !status.success() {
        let _ = child.kill();
        let _ = fs::remove_file(&state_file);
        return Err(command_failed("mount_nfs", status));
    }
    eprintln!(
        "level=INFO action=mounted mountpoint={} address=127.0.0.1:{}",
        mountpoint.display(),
        ready.port
    );
    Ok(())
}

fn ensure_unprivileged() -> io::Result<()> {
    let output = Command::new("/usr/bin/id").arg("-u").output()?;
    if !output.status.success() {
        return Err(command_failed("id -u", output.status));
    }
    let uid = parse_uid(&output.stdout)?;
    if uid == 0 {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "oxFS server must run unprivileged; run `oxfsd mount-demo ...` without sudo (oxFS elevates only mount_nfs internally)",
        ));
    }
    Ok(())
}

fn parse_uid(output: &[u8]) -> io::Result<u32> {
    std::str::from_utf8(output)
        .map_err(invalid_state)?
        .trim()
        .parse()
        .map_err(invalid_state)
}

fn mount_command(state: &MountState) -> Command {
    let mut command = Command::new("/usr/bin/sudo");
    command
        .arg("/sbin/mount_nfs")
        .arg("-o")
        .arg(format!(
            "vers=3,tcp,ro,nolocks,soft,intr,sec=sys,retrycnt=0,noac,nonegnamecache,port={},mountport={}",
            state.port, state.port
        ))
        .arg("127.0.0.1:/oxfs")
        .arg(&state.mountpoint);
    command
}

pub fn unmount(state_dir: &Path) -> io::Result<()> {
    let state_file = state_path(state_dir);
    let state = MountState::decode(&fs::read_to_string(&state_file)?)?;
    let status = Command::new("/usr/bin/sudo")
        .arg("/sbin/umount")
        .arg(&state.mountpoint)
        .status()?;
    if !status.success() {
        return Err(command_failed("umount", status));
    }
    let status = Command::new("/bin/kill")
        .arg(state.pid.to_string())
        .status()?;
    if !status.success() {
        return Err(command_failed("kill", status));
    }
    fs::remove_file(state_file)?;
    eprintln!(
        "level=INFO action=unmounted mountpoint={}",
        state.mountpoint.display()
    );
    Ok(())
}

fn command_failed(command: &str, status: ExitStatus) -> io::Error {
    io::Error::other(format!("{command} failed with {status}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn state_round_trip() {
        let state = MountState {
            pid: 42,
            port: 20490,
            mountpoint: PathBuf::from("/tmp/ox fs"),
        };
        assert_eq!(MountState::decode(&state.encode()).unwrap(), state);
    }

    #[test]
    fn mount_uses_explicit_matching_ports() {
        let state = MountState {
            pid: 42,
            port: 20490,
            mountpoint: PathBuf::from("/tmp/oxfs"),
        };
        let command = mount_command(&state);
        let args: Vec<_> = command
            .get_args()
            .map(|arg| arg.to_string_lossy().into_owned())
            .collect();
        assert_eq!(
            args,
            [
                "/sbin/mount_nfs",
                "-o",
                "vers=3,tcp,ro,nolocks,soft,intr,sec=sys,retrycnt=0,noac,nonegnamecache,port=20490,mountport=20490",
                "127.0.0.1:/oxfs",
                "/tmp/oxfs"
            ]
        );
    }

    #[test]
    fn parses_effective_uid_output() {
        assert_eq!(parse_uid(b"501\n").unwrap(), 501);
        assert!(parse_uid(b"not-a-uid\n").is_err());
    }
}

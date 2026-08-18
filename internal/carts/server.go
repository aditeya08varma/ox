package carts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	// stateFileName holds the running server's PID and port as ONE record.
	//
	// These were two files, written in sequence. A crash between the writes
	// bypasses any in-process cleanup and leaves a live dolt child with a PID
	// recorded and no port: runningServerPort rejects that, so every later call
	// starts another child, waits out the full startup timeout, and gives up —
	// while the original child still holds dolt's exclusive lock. Publishing one
	// record via atomic rename makes the state either wholly absent or wholly
	// valid, so a crash costs a restart rather than a permanent wedge.
	stateFileName = "dolt-server.json"

	// Legacy single-value files, removed on publish. Older ox builds wrote these;
	// leaving them behind would strand a real server behind unreadable metadata.
	legacyPIDFileName  = "dolt-server.pid"
	legacyPortFileName = "dolt-server.port"
)

// serverState is the published record of a running dolt sql-server.
type serverState struct {
	PID  int `json:"pid"`
	Port int `json:"port"`
}

// readServerState loads the published record, if any.
func readServerState(cartsDir string) (serverState, error) {
	var st serverState
	data, err := os.ReadFile(filepath.Join(cartsDir, stateFileName))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", stateFileName, err)
	}
	if st.PID <= 0 || st.Port <= 0 {
		return st, fmt.Errorf("%s: incomplete record (pid=%d port=%d)", stateFileName, st.PID, st.Port)
	}
	return st, nil
}

// writeServerState publishes the record atomically: write a temp file in the
// same directory, then rename over the target. Readers see either the old record
// or the new one, never a partial write.
func writeServerState(cartsDir string, st serverState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal server state: %w", err)
	}
	tmp, err := os.CreateTemp(cartsDir, stateFileName+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	// Durability before the rename: without the sync a crash can leave the
	// renamed file present but empty, which is the split-state this avoids.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(cartsDir, stateFileName)); err != nil {
		return fmt.Errorf("publish %s: %w", stateFileName, err)
	}

	// Best-effort: a stale legacy pair would otherwise outlive this record.
	_ = os.Remove(filepath.Join(cartsDir, legacyPIDFileName))
	_ = os.Remove(filepath.Join(cartsDir, legacyPortFileName))
	return nil
}

// removeServerState clears the published record.
func removeServerState(cartsDir string) {
	_ = os.Remove(filepath.Join(cartsDir, stateFileName))
}

// EnsureServer ensures a dolt sql-server is running for the carts database.
// If a server is already running (PID file exists and process alive), it returns the port.
// Otherwise it starts a new server on an ephemeral port.
func EnsureServer(cartsDir string) (int, error) {
	// Check if already running
	if port, err := runningServerPort(cartsDir); err == nil {
		return port, nil
	}

	// Start new server
	return startServer(cartsDir)
}

// runningServerPort returns the port of a running server, or error if not running.
func runningServerPort(cartsDir string) (int, error) {
	st, err := readServerState(cartsDir)
	if err != nil {
		return 0, err
	}
	// Liveness is platform-specific (see proc_unix.go / proc_windows.go). It was
	// written here as proc.Signal(os.Signal(nil)), which always errors, so reuse
	// was dead code — every invocation started another sql-server and all but the
	// first died on dolt's exclusive write lock.
	if !processAlive(st.PID) {
		return 0, fmt.Errorf("server process %d not running", st.PID)
	}

	// A live PID is not proof the server is ours — the OS recycles PIDs, so a
	// stale record can name an unrelated process. Confirm something is actually
	// serving on the recorded port before handing it back.
	if err := waitForServer("127.0.0.1", st.Port, reuseProbeBudget); err != nil {
		return 0, fmt.Errorf("server process %d is not serving port %d: %w", st.PID, st.Port, err)
	}
	return st.Port, nil
}

// startServer starts a dolt sql-server on an ephemeral port.
func startServer(cartsDir string) (int, error) {
	doltDir := filepath.Join(cartsDir, "dolt")

	// Ensure dolt directory exists and is initialized
	if err := ensureDoltInit(doltDir); err != nil {
		return 0, fmt.Errorf("init dolt: %w", err)
	}

	// Allocate ephemeral port
	port, err := allocateEphemeralPort("127.0.0.1")
	if err != nil {
		return 0, err
	}

	// Start dolt sql-server
	logFile, err := os.Create(filepath.Join(cartsDir, "dolt-server.log"))
	if err != nil {
		return 0, fmt.Errorf("create log file: %w", err)
	}

	cmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-auto-commit",
	)
	cmd.Dir = doltDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("start dolt sql-server: %w", err)
	}
	logFile.Close()

	// Wait for readiness BEFORE recording state. Writing the files first let a
	// starter that was about to lose dolt's write-lock race overwrite the
	// metadata of the server that won it, permanently orphaning the healthy
	// server: every later invocation read the loser's dead PID and started yet
	// another doomed one.
	//
	// Losing that race is expected, not exceptional: dolt permits exactly one
	// writer, so when two processes start concurrently one of them cannot come
	// up. waitForOwnServerOrWinner therefore also watches for a peer recording a
	// healthy server, and hands back the winner's port instead of failing.
	winner, err := waitForOwnServerOrWinner(cartsDir, port, 30*time.Second)
	if err != nil {
		// Nothing published yet, so there is no state of ours to unwind — only the
		// child. Deleting state files here would delete someone else's.
		killChild(cmd)
		return 0, fmt.Errorf("server failed to start: %w", err)
	}
	if winner != 0 {
		// A peer won the lock and published a ready server; ours never came up.
		// Kill only our child — the state files on disk are the WINNER's, and
		// removing them would orphan the healthy server and send every later
		// invocation into another doomed startup.
		killChild(cmd)
		return winner, nil
	}

	// From here the record is ours. It publishes in one atomic rename, so a
	// failure leaves nothing half-written to clean up — only the child to unwind.
	if err := writeServerState(cartsDir, serverState{PID: cmd.Process.Pid, Port: port}); err != nil {
		killChild(cmd)
		return 0, fmt.Errorf("publish server state: %w", err)
	}

	return port, nil
}

// killChild terminates and reaps a dolt sql-server we spawned. It deliberately
// touches no state files: at every call site the published metadata either
// belongs to a peer or does not exist yet.
func killChild(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// waitForOwnServerOrWinner polls until either the server we started on ourPort
// accepts connections (returns 0, nil) or a concurrently-started peer publishes
// a ready server (returns that peer's port, nil). It returns an error only if
// neither happens before the timeout.
//
// Polling for the peer matters for latency as much as correctness: without it a
// process that lost dolt's write-lock race blocks for the full timeout before
// failing, even though a healthy server it could have used appeared seconds in.
func waitForOwnServerOrWinner(cartsDir string, ourPort int, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := probeServer("127.0.0.1", ourPort, remainingProbe(deadline)); err == nil {
			return 0, nil
		}
		// Ignore the error: a missing or stale peer record simply means no winner
		// yet, which is the common case on the first iteration.
		if peer, err := runningServerPort(cartsDir); err == nil && peer != ourPort {
			return peer, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, fmt.Errorf("timeout waiting for dolt server on port %d", ourPort)
}

// ensureDoltInit ensures the dolt directory is initialized.
func ensureDoltInit(doltDir string) error {
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		return err
	}
	// Check if already initialized
	if _, err := os.Stat(filepath.Join(doltDir, ".dolt")); err == nil {
		return nil
	}
	cmd := exec.Command("dolt", "init")
	cmd.Dir = doltDir
	cmd.Env = append(os.Environ(),
		"DOLT_SILENCE_USER_REQ_FOR_TESTING=Y",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dolt init: %s: %w", string(out), err)
	}
	return nil
}

// allocateEphemeralPort asks the OS for a free TCP port.
func allocateEphemeralPort(host string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, fmt.Errorf("allocating ephemeral port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// probeServer is the liveness probe used by the reuse and readiness paths.
// Indirected through a variable so tests can exercise the concurrency logic
// without standing up a real dolt sql-server to satisfy a MySQL handshake.
//
// It takes an explicit timeout rather than always using probeTimeout so callers
// can hand it whatever remains of THEIR budget: a probe started just before a
// deadline must not run past it.
var probeServer = pingServer

// probeTimeout bounds a single liveness probe. Every caller polls on a deadline,
// so an unbounded Ping against a peer that accepts the TCP connection but never
// completes the MySQL handshake would block past that deadline — and in
// runningServerPort, which budgets 2s, would hang every ox carts invocation.
// 1s is generous for a loopback handshake and keeps deadline overshoot bounded.
const probeTimeout = time.Second

// reuseProbeBudget caps how long runningServerPort spends confirming a recorded
// server is really serving. It sits on the fast path of every carts command, so
// it must stay comfortably above probeTimeout (one probe has to fit) and low
// enough that a dead record costs a short pause rather than a visible stall.
const reuseProbeBudget = 2 * time.Second

// pingServer reports whether a dolt sql-server accepts an authenticated
// connection on host:port right now. Connects as root — dolt provisions no other
// account, and the git author name is not a SQL user.
func pingServer(host string, port int, timeout time.Duration) error {
	dsn := fmt.Sprintf("root@tcp(%s:%d)/?timeout=%s&readTimeout=%s&writeTimeout=%s",
		host, port, timeout, timeout, timeout)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	// Bound the whole probe, not just the socket operations: the driver's
	// timeouts cover dial/read/write individually, while a context deadline caps
	// the call itself including any retry inside database/sql.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return db.PingContext(ctx)
}

// minProbeWindow is the least time in which a loopback TCP connect plus MySQL
// handshake can plausibly finish. A probe granted less than this would report a
// perfectly healthy server as dead purely from scheduling jitter, so the budget
// is treated as spent instead.
const minProbeWindow = 50 * time.Millisecond

// remainingProbe returns how long a probe started now may run without
// overshooting deadline, capped at probeTimeout. It never returns a positive
// value below minProbeWindow — a sliver of budget yields a false negative rather
// than an answer — so callers stop probing once the budget is effectively spent.
func remainingProbe(deadline time.Time) time.Duration {
	remaining := time.Until(deadline)
	if remaining > probeTimeout {
		return probeTimeout
	}
	if remaining < minProbeWindow {
		return 0
	}
	return remaining
}

// waitForServer polls until the server accepts connections, or the timeout is
// spent. The timeout is a HARD bound: each probe is given only what remains, so
// a probe starting near the end cannot extend the caller's budget by its own.
func waitForServer(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		remaining := remainingProbe(deadline)
		if remaining <= 0 {
			break
		}
		if err := probeServer(host, port, remaining); err == nil {
			return nil
		}
		// Don't sleep past the deadline — that would burn budget doing nothing.
		if time.Until(deadline) <= 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for dolt server on port %d", port)
}

// StopServer stops the dolt server recorded for the given carts directory.
//
// A recorded PID is NOT sufficient authority to signal: PIDs are recycled, so a
// record left behind by a crash can name an unrelated process that the OS handed
// the same number, and stopping carts would kill a stranger's process. The PID
// is only acted on once the recorded port answers as a dolt server — the same
// proof runningServerPort requires before reusing it.
//
// Stale records are still cleared, so a wrong record cannot wedge future starts.
func StopServer(cartsDir string) error {
	st, err := readServerState(cartsDir)
	if err != nil {
		return nil // no server running
	}

	// Bind the PID to a live server on the recorded port before signaling.
	if !processAlive(st.PID) || probeServer("127.0.0.1", st.Port, probeTimeout) != nil {
		removeServerState(cartsDir)
		return nil
	}

	proc, err := os.FindProcess(st.PID)
	if err != nil {
		removeServerState(cartsDir)
		return nil
	}
	if err := terminateProcess(proc); err != nil {
		// Leave the record in place: the server is still running and still
		// discoverable, which beats orphaning it behind deleted metadata.
		return fmt.Errorf("stop dolt server %d: %w", st.PID, err)
	}
	removeServerState(cartsDir)
	return nil
}

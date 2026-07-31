package carts

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	pidFileName  = "dolt-server.pid"
	portFileName = "dolt-server.port"
)

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
	pidData, err := os.ReadFile(filepath.Join(cartsDir, pidFileName))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, err
	}
	// Liveness is platform-specific (see proc_unix.go / proc_windows.go). It was
	// written here as proc.Signal(os.Signal(nil)), which always errors, so reuse
	// was dead code — every invocation started another sql-server and all but the
	// first died on dolt's exclusive write lock.
	if !processAlive(pid) {
		return 0, fmt.Errorf("server process %d not running", pid)
	}

	portData, err := os.ReadFile(filepath.Join(cartsDir, portFileName))
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(portData)))
	if err != nil {
		return 0, err
	}

	// A live PID is not proof the server is ours — the OS recycles PIDs, so a
	// stale file can name an unrelated process. Confirm something is actually
	// serving on the recorded port before handing it back.
	if err := waitForServer("127.0.0.1", port, 2*time.Second); err != nil {
		return 0, fmt.Errorf("server process %d is not serving port %d: %w", pid, port, err)
	}
	return port, nil
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

	// From here the state files are ours, so a failed write must unwind both the
	// child and whatever we already published: a ready server with incomplete
	// metadata is worse than no server, because nothing can find it to reuse.
	pidPath := filepath.Join(cartsDir, pidFileName)
	portPath := filepath.Join(cartsDir, portFileName)

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		killChild(cmd)
		_ = os.Remove(pidPath) // may be partially written
		return 0, fmt.Errorf("write %s: %w", pidFileName, err)
	}
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(port)), 0o644); err != nil {
		killChild(cmd)
		_ = os.Remove(portPath)
		_ = os.Remove(pidPath) // written by us moments ago; would name a dead process
		return 0, fmt.Errorf("write %s: %w", portFileName, err)
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
		if err := probeServer("127.0.0.1", ourPort); err == nil {
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
var probeServer = pingServer

// pingServer reports whether a dolt sql-server accepts an authenticated
// connection on host:port right now. Connects as root — dolt provisions no other
// account, and the git author name is not a SQL user.
func pingServer(host string, port int) error {
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(%s:%d)/", host, port))
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Ping()
}

// waitForServer polls until the server accepts connections.
func waitForServer(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := probeServer(host, port); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for dolt server on port %d", port)
}

// StopServer stops a running dolt server for the given carts directory.
func StopServer(cartsDir string) error {
	pidData, err := os.ReadFile(filepath.Join(cartsDir, pidFileName))
	if err != nil {
		return nil // no server running
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	_ = proc.Signal(os.Interrupt)
	// Clean up state files
	os.Remove(filepath.Join(cartsDir, pidFileName))
	os.Remove(filepath.Join(cartsDir, portFileName))
	return nil
}

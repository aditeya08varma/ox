package carts

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/proc"

	_ "github.com/go-sql-driver/mysql"
)

const (
	pidFileName  = "dolt-server.pid"
	portFileName = "dolt-server.port"

	// serverHost is the loopback address the carts dolt sql-server binds to.
	serverHost = "127.0.0.1"
	// endpointCheckTimeout bounds the health probe used to validate a recorded
	// server before reusing it (a healthy loopback server answers in ms).
	endpointCheckTimeout = 2 * time.Second
	// serverStartTimeout bounds how long we wait for a freshly started server
	// to accept connections before giving up.
	serverStartTimeout = 30 * time.Second
)

// pingEndpoint validates that a recorded carts server is reachable and speaking
// MySQL. It is a package var so tests can exercise the stale/healthy reuse
// branches without a real dolt server.
var pingEndpoint = pingServer

// EnsureServer ensures a dolt sql-server is running for the carts database.
// If a healthy server is already recorded it returns that port; otherwise it
// starts a new one. Startup is serialized across processes with an advisory
// lock so two concurrent callers cannot each spawn a server against the same
// single-writer dolt directory.
func EnsureServer(cartsDir string) (int, error) {
	// Fast path: a recorded server that still answers.
	if port, err := runningServerPort(cartsDir); err == nil {
		return port, nil
	}

	// Slow path: serialize the check-then-start so only one caller starts a
	// server. The lock is held across readiness, so a concurrent caller waits
	// (or times out cleanly) instead of starting a duplicate. On a cold start a
	// concurrent second caller may hit the lock timeout; retrying then takes the
	// fast path once the first caller has published a ready server.
	var port int
	lockErr := fileutil.WithFileLock(context.Background(), filepath.Join(cartsDir, pidFileName), func() error {
		// Re-check inside the lock: another process may have started the server
		// while we were blocked.
		if p, err := runningServerPort(cartsDir); err == nil {
			port = p
			return nil
		}
		p, err := startServer(cartsDir)
		if err != nil {
			return err
		}
		port = p
		return nil
	})
	if lockErr != nil {
		return 0, lockErr
	}
	return port, nil
}

// runningServerPort returns the port of a healthy recorded server, or an error
// if none is usable. A live PID alone is insufficient: after a crash the OS can
// reassign the recorded PID to an unrelated process while stale pid/port files
// linger, so we also probe the endpoint before handing the port back.
func runningServerPort(cartsDir string) (int, error) {
	pidData, err := os.ReadFile(filepath.Join(cartsDir, pidFileName))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, err
	}
	// Check if process is alive. (Signal(nil) always fails with "unsupported
	// signal type", which made every call believe the server was dead and try
	// to start a second one against the same locked database.)
	if !proc.IsAlive(pid) {
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

	// Validate the recorded endpoint actually answers as a MySQL/dolt server.
	// This rejects a stale port left by a crashed server whose PID was recycled
	// by an unrelated live process, instead of returning it and failing later
	// with "connection refused".
	if err := pingEndpoint(serverHost, port, endpointCheckTimeout); err != nil {
		return 0, fmt.Errorf("recorded server on port %d is not reachable: %w", port, err)
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
	port, err := allocateEphemeralPort(serverHost)
	if err != nil {
		return 0, err
	}

	// Start dolt sql-server
	logFile, err := os.Create(filepath.Join(cartsDir, "dolt-server.log"))
	if err != nil {
		return 0, fmt.Errorf("create log file: %w", err)
	}

	cmd := exec.Command("dolt", "sql-server",
		"--host", serverHost,
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

	// Wait for readiness BEFORE publishing state, so a concurrent reader never
	// observes a pid/port for a server that isn't accepting connections yet.
	if err := waitForServer(serverHost, port, serverStartTimeout); err != nil {
		_ = cmd.Process.Kill() // don't orphan a half-started server
		return 0, fmt.Errorf("server failed to start: %w", err)
	}

	// Publish reusable state only after the server is ready.
	if err := os.WriteFile(filepath.Join(cartsDir, pidFileName), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(cartsDir, portFileName), []byte(strconv.Itoa(port)), 0o644); err != nil {
		return 0, err
	}

	return port, nil
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

// pingServer opens a MySQL connection to host:port and pings it. Success proves
// the endpoint is up AND speaking MySQL (i.e. it really is our dolt sql-server,
// not an unrelated process that inherited the recorded PID/port).
func pingServer(host string, port int, timeout time.Duration) error {
	dsn := fmt.Sprintf("root@tcp(%s:%d)/?timeout=%s&readTimeout=%s&writeTimeout=%s",
		host, port, timeout, timeout, timeout)
	db, err := sql.Open("mysql", dsn)
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
		if err := pingServer(host, port, time.Second); err == nil {
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

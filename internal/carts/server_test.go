package carts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var errStubUnreachable = errors.New("stub: endpoint unreachable")

// TestRunningServerPort covers the recorded-server reuse decision: it returns
// the saved port only when the PID is live AND the endpoint answers, and errors
// otherwise.
//
// Failure prevented: before #780 the liveness check (proc.Signal(nil)) always
// errored, so a live server was never detected and every call started a second
// sql-server. The "unreachable endpoint (stale)" case guards the follow-up
// validation — a live PID alone is not enough, because a crashed server can
// leave stale pid/port files while the OS reassigns its PID to an unrelated
// process; reusing that port would fail later with "connection refused".
func TestRunningServerPort(t *testing.T) {
	tests := []struct {
		name     string
		pid      string // "self" live, "dead" reaped, "" no file, else literal
		port     string // port file content; "" means no port file
		ping     error  // simulated endpoint probe result
		wantErr  bool
		wantPort int
	}{
		{name: "no pid file", pid: "", wantErr: true},
		{name: "live pid, reachable endpoint", pid: "self", port: "50422", ping: nil, wantPort: 50422},
		{name: "live pid, unreachable endpoint (stale)", pid: "self", port: "1", ping: errStubUnreachable, wantErr: true},
		{name: "dead pid", pid: "dead", port: "50422", ping: nil, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			// Stub the endpoint probe per-case so the reuse branches are
			// exercised without a real dolt server.
			orig := pingEndpoint
			pingEndpoint = func(string, int, time.Duration) error { return tc.ping }
			t.Cleanup(func() { pingEndpoint = orig })

			switch tc.pid {
			case "":
				// no pid file
			case "self":
				writeStateFile(t, dir, pidFileName, strconv.Itoa(os.Getpid()))
			case "dead":
				writeStateFile(t, dir, pidFileName, strconv.Itoa(deadPID(t)))
			default:
				writeStateFile(t, dir, pidFileName, tc.pid)
			}
			if tc.port != "" {
				writeStateFile(t, dir, portFileName, tc.port)
			}

			port, err := runningServerPort(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got port %d", port)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if port != tc.wantPort {
				t.Fatalf("port = %d, want %d", port, tc.wantPort)
			}
		})
	}
}

// deadPID returns a PID that has certainly exited: it runs the test binary as a
// no-op child (`-test.run=^$` matches no test) and reaps it. Portable across
// platforms, unlike shelling out to `true`, and it fails rather than skips when
// the helper cannot run so the dead-PID assertion is never silently bypassed.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn no-op helper: %v", err)
	}
	return cmd.Process.Pid
}

func writeStateFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

package carts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// runningServerPort must return the recorded port when the pid file names a
// live process, and an error when it names a dead one. Before the fix the
// liveness check used proc.Signal(os.Signal(nil)), which always errored, so a
// live server was never detected and every call started a second sql-server.
func TestRunningServerPort(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No pid file → not running.
	if _, err := runningServerPort(dir); err == nil {
		t.Fatal("expected error with no pid file")
	}

	// Live process (ourselves) + port file → port is returned.
	writeFile(pidFileName, strconv.Itoa(os.Getpid()))
	writeFile(portFileName, "50422")
	port, err := runningServerPort(dir)
	if err != nil {
		t.Fatalf("live pid: unexpected error: %v", err)
	}
	if port != 50422 {
		t.Fatalf("live pid: port = %d, want 50422", port)
	}

	// Dead process → error. Spawn a short-lived child and wait for it so the
	// pid is guaranteed to be gone.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot spawn helper process: %v", err)
	}
	writeFile(pidFileName, strconv.Itoa(cmd.Process.Pid))
	if _, err := runningServerPort(dir); err == nil {
		t.Fatal("expected error for dead pid")
	}
}

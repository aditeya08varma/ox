package carts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// The liveness probe in runningServerPort must use syscall.Signal(0). It was
// written as proc.Signal(os.Signal(nil)), and os.Process.Signal type-asserts its
// argument to syscall.Signal — nil fails that assertion, so the call ALWAYS
// returned an error. Server reuse became dead code: every ox carts invocation
// started another dolt sql-server, and all but the first died on dolt's
// exclusive write lock. This asserts the two behaviors reuse depends on.
func TestSignalZeroDetectsLiveness(t *testing.T) {
	t.Run("live process is detected as alive", func(t *testing.T) {
		proc, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatalf("FindProcess(self): %v", err)
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			t.Errorf("signal 0 on the running test process should succeed, got %v", err)
		}
	})

	t.Run("nil signal always errors", func(t *testing.T) {
		// Guards against reintroducing the original bug: a nil signal reports
		// even a definitely-live process as dead.
		proc, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatalf("FindProcess(self): %v", err)
		}
		if err := proc.Signal(os.Signal(nil)); err == nil {
			t.Error("expected nil signal to error; if this now works, the guard below is moot")
		}
	})

	t.Run("reaped process is detected as dead", func(t *testing.T) {
		cmd := exec.Command("true")
		if err := cmd.Start(); err != nil {
			t.Skipf("cannot spawn helper process: %v", err)
		}
		pid := cmd.Process.Pid
		_ = cmd.Wait() // reap, so the PID no longer names a live process

		proc, err := os.FindProcess(pid)
		if err != nil {
			t.Fatalf("FindProcess(%d): %v", pid, err)
		}
		if err := proc.Signal(syscall.Signal(0)); err == nil {
			t.Errorf("signal 0 on reaped pid %d should fail", pid)
		}
	})
}

// A stale PID file must not be trusted. Before the fix a losing starter could
// overwrite the state files of the server that won the lock race, leaving a
// dead PID recorded and the healthy server orphaned.
func TestRunningServerPortRejectsStalePID(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn helper process: %v", err)
	}
	deadPID := cmd.Process.Pid
	_ = cmd.Wait()

	write(t, filepath.Join(dir, pidFileName), strconv.Itoa(deadPID))
	write(t, filepath.Join(dir, portFileName), "1")

	if _, err := runningServerPort(dir); err == nil {
		t.Fatal("expected an error for a dead PID, got nil (caller would reuse a nonexistent server)")
	}
}

func TestRunningServerPortErrorsWithoutStateFiles(t *testing.T) {
	if _, err := runningServerPort(t.TempDir()); err == nil {
		t.Fatal("expected an error when no PID file exists")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

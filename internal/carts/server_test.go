package carts

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubProbe replaces the MySQL liveness probe for the duration of a test so the
// concurrency logic can be exercised without standing up a real dolt sql-server.
// healthy lists the ports that should answer; everything else is refused.
func stubProbe(t *testing.T, healthy ...int) {
	t.Helper()
	original := probeServer
	t.Cleanup(func() { probeServer = original })
	probeServer = func(_ string, port int, _ time.Duration) error {
		for _, h := range healthy {
			if h == port {
				return nil
			}
		}
		return errors.New("connection refused")
	}
}

// deadPID returns the PID of a process that has exited and been reaped, so the
// number is valid but names nothing running.
//
// It re-executes the test binary with a filter that matches no test rather than
// shelling out to `true`: a missing external binary would otherwise turn the
// liveness assertions into a skip, letting them pass vacuously on exactly the
// hosts where they might regress.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait() // reap, so the PID no longer names a live process
	return pid
}

// freePort returns a port number with nothing listening on it.
func freePort(t *testing.T) int {
	t.Helper()
	port, err := allocateEphemeralPort("127.0.0.1")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	return port
}

// processAlive must report liveness truthfully. It was written as
// proc.Signal(os.Signal(nil)), and os.Process.Signal type-asserts its argument to
// syscall.Signal — nil fails that assertion, so the call ALWAYS returned an
// error. Server reuse became dead code: every ox carts invocation started another
// dolt sql-server, and all but the first died on dolt's exclusive write lock.
func TestProcessAlive(t *testing.T) {
	t.Run("running process is alive", func(t *testing.T) {
		if !processAlive(os.Getpid()) {
			t.Error("the running test process should report as alive")
		}
	})

	t.Run("reaped process is not alive", func(t *testing.T) {
		pid := deadPID(t)
		if processAlive(pid) {
			t.Errorf("reaped pid %d should report as dead", pid)
		}
	})
}

// runningServerPort gates server reuse. Every rejection path matters: handing
// back a port for a server that is not really there sends the caller into a
// confusing connection error instead of a clean restart.
func TestRunningServerPortRejections(t *testing.T) {
	tests := []struct {
		name string
		// setup populates the carts dir; nil leaves it empty.
		setup func(t *testing.T, dir string)
	}{
		{
			name:  "no state files",
			setup: nil,
		},
		{
			name: "pid file names a reaped process",
			setup: func(t *testing.T, dir string) {
				writeState(t, dir, deadPID(t), freePort(t))
			},
		},
		{
			name: "live pid but nothing serving the recorded port",
			setup: func(t *testing.T, dir string) {
				// PID recycling means a live PID is not proof the server is ours.
				// This test process is definitely alive and definitely not dolt.
				writeState(t, dir, os.Getpid(), freePort(t))
			},
		},
		{
			name: "state file is not valid json",
			setup: func(t *testing.T, dir string) {
				mustWrite(t, filepath.Join(dir, stateFileName), "{not json")
			},
		},
		{
			name: "state record is missing the port",
			setup: func(t *testing.T, dir string) {
				// The split-state a mid-write crash used to leave behind.
				mustWrite(t, filepath.Join(dir, stateFileName),
					`{"pid":`+strconv.Itoa(os.Getpid())+`}`)
			},
		},
		{
			name: "only the legacy pid/port pair is present",
			setup: func(t *testing.T, dir string) {
				// Written by an older ox build; unreadable to the current format, so
				// the caller must start fresh rather than trust it.
				mustWrite(t, filepath.Join(dir, legacyPIDFileName), strconv.Itoa(os.Getpid()))
				mustWrite(t, filepath.Join(dir, legacyPortFileName), "12345")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubProbe(t) // nothing is healthy
			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			if port, err := runningServerPort(dir); err == nil {
				t.Fatalf("expected an error, got port %d", port)
			}
		})
	}
}

func TestRunningServerPortAcceptsHealthyServer(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	stubProbe(t, port)
	writeState(t, dir, os.Getpid(), port)

	got, err := runningServerPort(dir)
	if err != nil {
		t.Fatalf("expected reuse of a healthy server, got error: %v", err)
	}
	if got != port {
		t.Errorf("expected port %d, got %d", port, got)
	}
}

// waitForOwnServerOrWinner is what lets a process that lost dolt's single-writer
// race reuse the winner instead of failing after a full timeout.
func TestWaitForOwnServerOrWinner(t *testing.T) {
	t.Run("reports our own server once it comes up", func(t *testing.T) {
		ourPort := freePort(t)
		stubProbe(t, ourPort)

		winner, err := waitForOwnServerOrWinner(t.TempDir(), ourPort, time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if winner != 0 {
			t.Errorf("expected 0 (our own server won), got peer port %d", winner)
		}
	})

	t.Run("returns the peer port when a peer publishes a ready server", func(t *testing.T) {
		dir := t.TempDir()
		ourPort := freePort(t)
		peerPort := freePort(t)
		// Only the peer answers; ours never comes up, as when we lose the lock race.
		stubProbe(t, peerPort)
		writeState(t, dir, os.Getpid(), peerPort)

		winner, err := waitForOwnServerOrWinner(dir, ourPort, time.Second)
		if err != nil {
			t.Fatalf("expected the peer's port, got error: %v", err)
		}
		if winner != peerPort {
			t.Errorf("expected peer port %d, got %d", peerPort, winner)
		}
	})

	t.Run("errors when neither our server nor a peer appears", func(t *testing.T) {
		stubProbe(t) // nothing is healthy
		if _, err := waitForOwnServerOrWinner(t.TempDir(), freePort(t), 300*time.Millisecond); err == nil {
			t.Fatal("expected a timeout error when nothing is serving")
		}
	})
}

// A process that loses dolt's write-lock race must tear down only its own child.
// The state files at that moment belong to the WINNER; deleting them orphans the
// healthy server and sends every later invocation into another doomed startup.
func TestKillChildLeavesPublishedStateIntact(t *testing.T) {
	dir := t.TempDir()
	winnerPID, winnerPort := 4242, 54321
	writeState(t, dir, winnerPID, winnerPort)

	// A real child to stand in for the dolt server we started and lost with.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	pid := cmd.Process.Pid

	killChild(cmd)

	if processAlive(pid) {
		t.Errorf("our child pid %d should have been reaped", pid)
	}
	st, err := readServerState(dir)
	if err != nil {
		t.Fatalf("winner metadata must survive: %v", err)
	}
	if st.PID != winnerPID || st.Port != winnerPort {
		t.Errorf("winner record = %+v, want pid=%d port=%d", st, winnerPID, winnerPort)
	}
}

// PID and port must publish as ONE record. They used to be two sequential
// writes, and a crash between them left a live dolt child with a PID recorded
// and no port — a state runningServerPort rejects, so every retry spawned
// another child while the original kept dolt's lock. A crash can't be simulated
// directly, so assert the properties that make it survivable: the record is
// never partially visible, and a legacy half-state is rejected rather than
// half-trusted.
func TestServerStatePublishesAtomically(t *testing.T) {
	dir := t.TempDir()

	t.Run("no record before publish", func(t *testing.T) {
		if _, err := readServerState(dir); err == nil {
			t.Fatal("expected an error before anything is published")
		}
	})

	t.Run("record is complete after publish", func(t *testing.T) {
		if err := writeServerState(dir, serverState{PID: 111, Port: 222}); err != nil {
			t.Fatalf("write: %v", err)
		}
		st, err := readServerState(dir)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if st.PID != 111 || st.Port != 222 {
			t.Errorf("got %+v, want pid=111 port=222", st)
		}
	})

	t.Run("no temp files are left behind", func(t *testing.T) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp") {
				t.Errorf("leftover temp file %q would accumulate on every start", e.Name())
			}
		}
	})

	t.Run("publishing clears the legacy pair", func(t *testing.T) {
		mustWrite(t, filepath.Join(dir, legacyPIDFileName), "999")
		mustWrite(t, filepath.Join(dir, legacyPortFileName), "888")
		if err := writeServerState(dir, serverState{PID: 333, Port: 444}); err != nil {
			t.Fatalf("write: %v", err)
		}
		for _, name := range []string{legacyPIDFileName, legacyPortFileName} {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("%s should have been removed; a stale pair outlives the record", name)
			}
		}
	})
}

// reuseProbeBudget must be a HARD bound. waitForServer previously checked the
// deadline only before starting a probe, so a probe beginning just under the
// limit could still run a full probeTimeout and overshoot — on the fast path of
// every carts command.
func TestWaitForServerHonoursBudgetAsHardDeadline(t *testing.T) {
	// Probe that always fails, but only after consuming everything it was given —
	// the worst case for a caller whose budget is nearly spent.
	original := probeServer
	t.Cleanup(func() { probeServer = original })
	probeServer = func(_ string, _ int, timeout time.Duration) error {
		time.Sleep(timeout)
		return errors.New("still not serving")
	}

	start := time.Now()
	if err := waitForServer("127.0.0.1", freePort(t), reuseProbeBudget); err == nil {
		t.Fatal("expected a timeout error")
	}
	elapsed := time.Since(start)

	// Slack covers scheduling only; the point is that overshoot is bounded by
	// jitter rather than by a whole extra probeTimeout.
	if ceiling := reuseProbeBudget + 500*time.Millisecond; elapsed > ceiling {
		t.Errorf("waitForServer took %v, exceeding its %v budget (ceiling %v)",
			elapsed, reuseProbeBudget, ceiling)
	}
}

// A probe must fail fast against a socket that accepts the connection but never
// completes the MySQL handshake. Without DSN/context timeouts the Ping blocks
// indefinitely, blowing past every caller's deadline — and since
// runningServerPort budgets 2s, it would hang every ox carts invocation.
func TestPingServerTimesOutOnSilentPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Accept and then say nothing, holding the connection open.
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				conn.Close()
			}()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	start := time.Now()
	if err := pingServer("127.0.0.1", port, probeTimeout); err == nil {
		t.Fatal("expected an error probing a peer that never completes the handshake")
	}
	elapsed := time.Since(start)

	// The ceiling is probeTimeout plus scheduling slack, NOT an arbitrary large
	// number: the probe runs on runningServerPort's reuseProbeBudget, so a
	// regression that merely slowed the probe to just under a loose ceiling would
	// still blow that budget and stall every carts command.
	ceiling := probeTimeout + 750*time.Millisecond
	if ceiling >= reuseProbeBudget {
		t.Fatalf("test ceiling %v must stay below the reuse budget %v", ceiling, reuseProbeBudget)
	}
	if elapsed > ceiling {
		t.Errorf("probe took %v; expected it to give up near %v (ceiling %v, reuse budget %v)",
			elapsed, probeTimeout, ceiling, reuseProbeBudget)
	}
}

func writeState(t *testing.T, dir string, pid, port int) {
	t.Helper()
	if err := writeServerState(dir, serverState{PID: pid, Port: port}); err != nil {
		t.Fatalf("write server state: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

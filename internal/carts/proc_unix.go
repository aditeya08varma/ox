//go:build !windows

package carts

import (
	"os"
	"syscall"
)

// processAlive reports whether pid names a live process.
//
// Signal 0 is the POSIX process-existence convention: it runs the kernel's
// permission and existence checks without delivering anything. It must be
// syscall.Signal(0) and not a nil os.Signal — os.Process.Signal type-asserts its
// argument to syscall.Signal, which nil fails, so a nil signal always reports
// even a live process as dead.
//
// A true result is necessary but not sufficient for reuse: PIDs are recycled, so
// callers must still confirm the recorded port is actually serving.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// terminateProcess asks a dolt sql-server to shut down. SIGINT lets dolt close
// its storage cleanly and release the write lock; a hard kill risks leaving lock
// state behind for the next start to trip over.
func terminateProcess(proc *os.Process) error {
	return proc.Signal(os.Interrupt)
}

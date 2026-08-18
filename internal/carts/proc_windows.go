//go:build windows

package carts

import (
	"os"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a process that has
// not exited (STILL_ACTIVE in the Win32 headers, 259). x/sys/windows exposes the
// same value as STATUS_PENDING but not under the STILL_ACTIVE name, so spell it
// out rather than borrowing a constant that means something else.
const stillActive = 259

// processAlive reports whether pid names a live process.
//
// Signal 0 is meaningless here: Go's Windows os.Process.Signal supports only
// os.Kill and returns syscall.EWINDOWS for anything else, so the POSIX probe
// would report every live process as dead and defeat server reuse entirely.
// Instead open the process and ask for its exit code — STILL_ACTIVE means running.
//
// A true result is necessary but not sufficient for reuse: PIDs are recycled, so
// callers must still confirm the recorded port is actually serving.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// terminateProcess asks a dolt sql-server to shut down.
//
// os.Process.Signal(os.Interrupt) is NOT implemented on Windows — it returns an
// error without touching the target. Using it here made StopServer a silent
// no-op that deleted the state record while the server kept running and holding
// dolt's write lock, which is worse than failing outright. Windows has no
// deliverable equivalent (GenerateConsoleCtrlEvent needs a shared console and
// hits the whole group), so terminate the process directly.
func terminateProcess(proc *os.Process) error {
	return proc.Kill()
}

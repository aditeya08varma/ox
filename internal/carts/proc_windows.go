//go:build windows

package carts

import (
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

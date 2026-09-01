//go:build windows

package adapters

import "os/exec"

// configureOneShotCommand leaves Windows cancellation to CommandContext.
// WaitDelay remains the cross-platform backstop for inherited output pipes.
func configureOneShotCommand(_ *exec.Cmd) {}

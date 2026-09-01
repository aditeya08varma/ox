//go:build windows

package adapters

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const windowsJobHelperEnv = "OX_TEST_WINDOWS_JOB_HELPER"

func TestRunOneShotCommand_WindowsJobKillsDescendants(t *testing.T) {
	if mode := os.Getenv(windowsJobHelperEnv); mode != "" {
		runWindowsJobHelper(t, mode)
		return
	}

	dir := t.TempDir()
	survivedPath := filepath.Join(dir, "descendant-survived")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunOneShotCommand_WindowsJobKillsDescendants$")
	cmd.Env = append(os.Environ(), windowsJobHelperEnv+"=parent", "OX_TEST_SURVIVED_PATH="+survivedPath) // safe: re-executes this test binary only
	cmd.WaitDelay = 100 * time.Millisecond

	err := runOneShotCommand(cmd)
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("run error = %v, context error = %v; want deadline cancellation", err, ctx.Err())
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(survivedPath); !os.IsNotExist(err) {
		t.Fatalf("adapter descendant survived Job Object cancellation: %v", err)
	}
}

func runWindowsJobHelper(t *testing.T, mode string) {
	t.Helper()
	switch mode {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestRunOneShotCommand_WindowsJobKillsDescendants$")
		child.Env = append(os.Environ(), windowsJobHelperEnv+"=child") // safe: re-executes this test binary only
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
	case "child":
		time.Sleep(time.Second)
		if err := os.WriteFile(os.Getenv("OX_TEST_SURVIVED_PATH"), []byte("alive"), 0o600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	default:
		os.Exit(4)
	}
}

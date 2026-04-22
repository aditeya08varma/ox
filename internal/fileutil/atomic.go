package fileutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteBytes writes raw bytes to filePath atomically using temp+rename,
// fsync'd before rename and the parent directory fsync'd after rename so the
// new inode entry survives a hard crash. Intended for user-facing files where
// a crash-window partial write would destroy content — instruction files
// (AGENTS.md / CONVENTIONS.md), env files, etc.
func AtomicWriteBytes(filePath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filePath)

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write bytes: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	// fsync the parent directory so the rename's dirent update is durable
	// even across a hard crash. Best-effort: directory fsync is not supported
	// on every platform/filesystem (Windows, some network mounts), so log-and-
	// continue rather than failing the write.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	success = true
	return nil
}

// AtomicWriteJSON writes data as JSON to filePath atomically using temp+rename.
// This prevents partial/corrupt files from concurrent reads during write.
// The file is fsync'd before rename to ensure durability.
func AtomicWriteJSON(filePath string, data any, perm os.FileMode) error {
	dir := filepath.Dir(filePath)

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		tmp.Close()
		return fmt.Errorf("encode JSON: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	success = true
	return nil
}

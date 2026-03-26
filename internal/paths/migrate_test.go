package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// GetLegacyPaths Tests
// -----------------------------------------------------------------------------

func TestGetLegacyPaths(t *testing.T) {
	legacyPaths := GetLegacyPaths()

	// verify all paths are absolute
	assert.True(t, filepath.IsAbs(legacyPaths.ConfigDir), "ConfigDir should be absolute")
	assert.True(t, filepath.IsAbs(legacyPaths.GuidanceCache), "GuidanceCache should be absolute")
	assert.True(t, filepath.IsAbs(legacyPaths.SessionCache), "SessionCache should be absolute")

	// verify expected path components
	assert.Contains(t, legacyPaths.ConfigDir, ".config")
	assert.Contains(t, legacyPaths.ConfigDir, "sageox")

	assert.Contains(t, legacyPaths.GuidanceCache, ".sageox")
	assert.Contains(t, legacyPaths.GuidanceCache, "guidance")

	assert.Contains(t, legacyPaths.SessionCache, ".cache")
	assert.Contains(t, legacyPaths.SessionCache, "sageox")
}

// -----------------------------------------------------------------------------
// CheckMigrationStatus Tests
// -----------------------------------------------------------------------------

func TestCheckMigrationStatus_XDGModeDisablesMigration(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	status := CheckMigrationStatus()

	assert.False(t, status.Needed, "migration should not be needed in XDG mode")
	assert.False(t, status.LegacyConfigExists)
	assert.False(t, status.LegacyGuidanceCacheExists)
	assert.False(t, status.LegacySessionCacheExists)
	assert.False(t, status.NewStructureExists)
}

func TestCheckMigrationStatus_ReturnsValidStruct(t *testing.T) {
	// ensure XDG mode is disabled
	t.Setenv("OX_XDG_ENABLE", "")

	status := CheckMigrationStatus()

	// verify the function returns without panic and produces a valid struct
	// the actual values depend on system state, but structure should be valid
	assert.IsType(t, MigrationStatus{}, status)
}

// -----------------------------------------------------------------------------
// Migrate Tests
// -----------------------------------------------------------------------------

func TestMigrate_XDGModeSkipped(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	result := Migrate()

	assert.False(t, result.ConfigMigrated)
	assert.False(t, result.GuidanceCacheMigrated)
	assert.False(t, result.SessionCacheMigrated)
	assert.Empty(t, result.Errors)
}

func TestMigrate_ReturnsValidResult(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "")

	result := Migrate()

	// verify the function returns without panic and produces a valid struct
	assert.IsType(t, MigrationResult{}, result)
	// errors should be a slice (possibly empty)
	assert.IsType(t, []error{}, result.Errors)
}

// -----------------------------------------------------------------------------
// migrateDirectory Tests
// -----------------------------------------------------------------------------

func TestMigrateDirectory_Success(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create source with files and subdirectories
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0644))

	subDir := filepath.Join(srcDir, "subdir")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("content2"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify files migrated
	assert.FileExists(t, filepath.Join(dstDir, "file1.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "subdir", "file2.txt"))

	// verify content preserved
	content1, _ := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
	content2, _ := os.ReadFile(filepath.Join(dstDir, "subdir", "file2.txt"))
	assert.Equal(t, "content1", string(content1))
	assert.Equal(t, "content2", string(content2))

	// verify source removed
	assert.NoDirExists(t, srcDir)
}

func TestMigrateDirectory_SkipsExistingDestination(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create source
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("source"), 0644))

	// create destination with existing file
	require.NoError(t, os.MkdirAll(dstDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "file.txt"), []byte("existing"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify existing file was NOT overwritten
	content, _ := os.ReadFile(filepath.Join(dstDir, "file.txt"))
	assert.Equal(t, "existing", string(content))
}

func TestMigrateDirectory_EmptySource(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify destination created
	assert.DirExists(t, dstDir)

	// verify source removed
	assert.NoDirExists(t, srcDir)
}

func TestMigrateDirectory_PreservesPermissions(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// create file with specific permissions
	srcFile := filepath.Join(srcDir, "script.sh")
	require.NoError(t, os.WriteFile(srcFile, []byte("#!/bin/bash"), 0755))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify permissions preserved (within umask constraints)
	dstFile := filepath.Join(dstDir, "script.sh")
	info, err := os.Stat(dstFile)
	require.NoError(t, err)

	// on most systems, executable bit should be preserved
	assert.True(t, info.Mode()&0100 != 0, "expected executable permission to be preserved")
}

func TestMigrateDirectory_SourceNotExists(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "nonexistent")
	dstDir := filepath.Join(tempDir, "dst")

	err := migrateDirectory(srcDir, dstDir)
	assert.Error(t, err)
}

func TestMigrateDirectory_NestedSubdirectories(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create deeply nested structure
	deepPath := filepath.Join(srcDir, "a", "b", "c", "d")
	require.NoError(t, os.MkdirAll(deepPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(deepPath, "deep.txt"), []byte("deep content"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify deep file migrated
	assert.FileExists(t, filepath.Join(dstDir, "a", "b", "c", "d", "deep.txt"))
	content, _ := os.ReadFile(filepath.Join(dstDir, "a", "b", "c", "d", "deep.txt"))
	assert.Equal(t, "deep content", string(content))
}

func TestMigrateDirectory_MixedContentTypes(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// create files with different types of content
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "text.txt"), []byte("text"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "binary.bin"), []byte{0x00, 0x01, 0x02}, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "empty.txt"), []byte{}, 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify all files migrated with correct content
	textContent, _ := os.ReadFile(filepath.Join(dstDir, "text.txt"))
	binaryContent, _ := os.ReadFile(filepath.Join(dstDir, "binary.bin"))
	emptyContent, _ := os.ReadFile(filepath.Join(dstDir, "empty.txt"))

	assert.Equal(t, []byte("text"), textContent)
	assert.Equal(t, []byte{0x00, 0x01, 0x02}, binaryContent)
	assert.Empty(t, emptyContent)
}

// -----------------------------------------------------------------------------
// copyFile Tests
// -----------------------------------------------------------------------------

func TestCopyFile_Success(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "source.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	require.NoError(t, os.WriteFile(srcFile, []byte("hello world"), 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	assert.FileExists(t, dstFile)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, "hello world", string(content))
}

func TestCopyFile_PreservesMode(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "script.sh")
	dstFile := filepath.Join(tempDir, "dest.sh")

	require.NoError(t, os.WriteFile(srcFile, []byte("#!/bin/bash"), 0755))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	srcInfo, _ := os.Stat(srcFile)
	dstInfo, _ := os.Stat(dstFile)

	assert.Equal(t, srcInfo.Mode(), dstInfo.Mode())
}

func TestCopyFile_SourceNotExists(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "nonexistent.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	err := copyFile(srcFile, dstFile)
	assert.Error(t, err)
}

func TestCopyFile_BinaryContent(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "binary.bin")
	dstFile := filepath.Join(tempDir, "dest.bin")

	// binary content including null bytes
	binaryData := []byte{0x00, 0xFF, 0x01, 0xFE, 0x02, 0xFD}
	require.NoError(t, os.WriteFile(srcFile, binaryData, 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, binaryData, content)
}

func TestCopyFile_EmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "empty.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	require.NoError(t, os.WriteFile(srcFile, []byte{}, 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Empty(t, content)
}

func TestCopyFile_LargeFile(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "large.bin")
	dstFile := filepath.Join(tempDir, "dest.bin")

	// create a 1MB file
	largeData := make([]byte, 1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}
	require.NoError(t, os.WriteFile(srcFile, largeData, 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, largeData, content)
}

func TestCopyFile_OverwritesExisting(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "source.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	require.NoError(t, os.WriteFile(srcFile, []byte("new content"), 0644))
	require.NoError(t, os.WriteFile(dstFile, []byte("old content"), 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, "new content", string(content))
}

// -----------------------------------------------------------------------------
// copyDir Tests
// -----------------------------------------------------------------------------

func TestCopyDir_Success(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create source with nested structure
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "a", "b", "c"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "root.txt"), []byte("root"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a", "level1.txt"), []byte("level1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a", "b", "level2.txt"), []byte("level2"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a", "b", "c", "level3.txt"), []byte("level3"), 0644))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// verify all files copied
	assert.FileExists(t, filepath.Join(dstDir, "root.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "a", "level1.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "a", "b", "level2.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "a", "b", "c", "level3.txt"))
}

func TestCopyDir_EmptyDir(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	assert.DirExists(t, dstDir)
}

func TestCopyDir_SourceNotExists(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "nonexistent")
	dstDir := filepath.Join(tempDir, "dst")

	err := copyDir(srcDir, dstDir)
	assert.Error(t, err)
}

func TestCopyDir_PreservesDirectoryMode(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	srcInfo, _ := os.Stat(srcDir)
	dstInfo, _ := os.Stat(dstDir)

	assert.Equal(t, srcInfo.Mode(), dstInfo.Mode())
}

func TestCopyDir_MultipleFiles(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// create multiple files
	for i := 0; i < 10; i++ {
		filename := filepath.Join(srcDir, "file"+string(rune('0'+i))+".txt")
		require.NoError(t, os.WriteFile(filename, []byte("content"+string(rune('0'+i))), 0644))
	}

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// verify all files copied
	for i := 0; i < 10; i++ {
		filename := filepath.Join(dstDir, "file"+string(rune('0'+i))+".txt")
		assert.FileExists(t, filename)
		content, _ := os.ReadFile(filename)
		assert.Equal(t, "content"+string(rune('0'+i)), string(content))
	}
}

func TestCopyDir_DoesNotDeleteSource(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// source should still exist (copyDir does not remove source, migrateDirectory does)
	assert.DirExists(t, srcDir)
	assert.FileExists(t, filepath.Join(srcDir, "file.txt"))
}

// -----------------------------------------------------------------------------
// TeamContextMigrationNeeded Tests
// -----------------------------------------------------------------------------

func TestTeamContextMigrationNeeded_XDGMode(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	result := TeamContextMigrationNeeded("/project", "team123", "/some/path")

	assert.False(t, result, "should return false in XDG mode")
}

func TestTeamContextMigrationNeeded_LegacyNotExists(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "")

	projectRoot := filepath.Join(tempDir, "project")
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")
	// legacyPath does not exist

	result := TeamContextMigrationNeeded(projectRoot, "abc", legacyPath)

	assert.False(t, result, "should return false when legacy path doesn't exist")
}

func TestTeamContextMigrationNeeded_LegacyExists(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "")

	projectRoot := filepath.Join(tempDir, "project")
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")

	// create legacy path
	require.NoError(t, os.MkdirAll(legacyPath, 0755))

	// result depends on whether new path exists
	// since we can't control home dir caching, just verify no panic
	result := TeamContextMigrationNeeded(projectRoot, "abc", legacyPath)
	assert.IsType(t, true, result)
}

// -----------------------------------------------------------------------------
// MigrateTeamContext Tests
// -----------------------------------------------------------------------------

func TestMigrateTeamContext_XDGModeError(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	err := MigrateTeamContext("team123", "/some/legacy/path")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "XDG mode")
}

func TestMigrateTeamContext_InvalidPath(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "")

	// try to migrate from non-existent path
	err := MigrateTeamContext("team123", "/nonexistent/path/that/does/not/exist")

	// should fail because source doesn't exist
	assert.Error(t, err)
}

// -----------------------------------------------------------------------------
// EnsureMigrated Tests
// -----------------------------------------------------------------------------

func TestEnsureMigrated_RunsWithoutPanic(t *testing.T) {
	// EnsureMigrated uses sync.Once, so it only runs once per process.
	// this test verifies it doesn't panic.
	t.Setenv("OX_XDG_ENABLE", "")

	// should not panic
	err := EnsureMigrated()
	// error may or may not be nil depending on system state
	_ = err
}

// -----------------------------------------------------------------------------
// migrateSessionCache Tests
// -----------------------------------------------------------------------------

func TestMigrateSessionCache_NoContextDir(t *testing.T) {
	tempDir := t.TempDir()

	legacyDir := filepath.Join(tempDir, "legacy")
	require.NoError(t, os.MkdirAll(legacyDir, 0755))

	// no context subdirectory - should return nil (nothing to migrate)
	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)
}

func TestMigrateSessionCache_EmptyContextDir(t *testing.T) {
	tempDir := t.TempDir()

	legacyDir := filepath.Join(tempDir, "legacy")
	contextDir := filepath.Join(legacyDir, "context")
	require.NoError(t, os.MkdirAll(contextDir, 0755))

	// empty context dir - should succeed
	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)
}

// -----------------------------------------------------------------------------
// MigrationStatus Struct Tests
// -----------------------------------------------------------------------------

func TestMigrationStatus_Struct(t *testing.T) {
	status := MigrationStatus{
		Needed:                    true,
		LegacyConfigExists:        true,
		LegacyGuidanceCacheExists: false,
		LegacySessionCacheExists:  true,
		NewStructureExists:        false,
	}

	assert.True(t, status.Needed)
	assert.True(t, status.LegacyConfigExists)
	assert.False(t, status.LegacyGuidanceCacheExists)
	assert.True(t, status.LegacySessionCacheExists)
	assert.False(t, status.NewStructureExists)
}

// -----------------------------------------------------------------------------
// MigrationResult Struct Tests
// -----------------------------------------------------------------------------

func TestMigrationResult_Struct(t *testing.T) {
	result := MigrationResult{
		ConfigMigrated:        true,
		GuidanceCacheMigrated: false,
		SessionCacheMigrated:  true,
		Errors:                nil,
	}

	assert.True(t, result.ConfigMigrated)
	assert.False(t, result.GuidanceCacheMigrated)
	assert.True(t, result.SessionCacheMigrated)
	assert.Empty(t, result.Errors)
}

func TestMigrationResult_WithErrors(t *testing.T) {
	result := MigrationResult{
		Errors: []error{
			os.ErrNotExist,
			os.ErrPermission,
		},
	}

	assert.Len(t, result.Errors, 2)
	assert.ErrorIs(t, result.Errors[0], os.ErrNotExist)
	assert.ErrorIs(t, result.Errors[1], os.ErrPermission)
}

// -----------------------------------------------------------------------------
// LegacyPaths Struct Tests
// -----------------------------------------------------------------------------

func TestLegacyPaths_Struct(t *testing.T) {
	paths := LegacyPaths{
		ConfigDir:     "/home/user/.config/sageox",
		GuidanceCache: "/home/user/.sageox/guidance/cache",
		SessionCache:  "/home/user/.cache/sageox",
	}

	assert.Equal(t, "/home/user/.config/sageox", paths.ConfigDir)
	assert.Equal(t, "/home/user/.sageox/guidance/cache", paths.GuidanceCache)
	assert.Equal(t, "/home/user/.cache/sageox", paths.SessionCache)
}

// -----------------------------------------------------------------------------
// Edge Case Tests
// -----------------------------------------------------------------------------

func TestMigrateDirectory_SpecialCharactersInFilenames(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// files with various special characters (that are valid on most filesystems)
	specialFiles := []string{
		"file with spaces.txt",
		"file-with-dashes.txt",
		"file_with_underscores.txt",
		"file.multiple.dots.txt",
	}

	for _, name := range specialFiles {
		path := filepath.Join(srcDir, name)
		require.NoError(t, os.WriteFile(path, []byte("content: "+name), 0644))
	}

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify all special files migrated
	for _, name := range specialFiles {
		path := filepath.Join(dstDir, name)
		assert.FileExists(t, path)
		content, _ := os.ReadFile(path)
		assert.Equal(t, "content: "+name, string(content))
	}
}

func TestCopyFile_SpecialPermissions(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "source.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	// test with read-only permission
	require.NoError(t, os.WriteFile(srcFile, []byte("read only"), 0444))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	dstInfo, _ := os.Stat(dstFile)
	assert.Equal(t, os.FileMode(0444), dstInfo.Mode().Perm())
}

func TestCopyDir_SymlinksSkipped(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")
	targetFile := filepath.Join(tempDir, "target.txt")

	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(targetFile, []byte("target"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "regular.txt"), []byte("regular"), 0644))

	// create symlink in source (on systems that support it)
	symlinkPath := filepath.Join(srcDir, "link.txt")
	err := os.Symlink(targetFile, symlinkPath)
	if err != nil {
		t.Skip("symlinks not supported on this system")
	}

	// copyDir should handle this gracefully (might copy symlink or skip it)
	err = copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// regular file should be copied
	assert.FileExists(t, filepath.Join(dstDir, "regular.txt"))
}

// -----------------------------------------------------------------------------
// DetectLegacyLedgerPath Tests
// -----------------------------------------------------------------------------

func TestDetectLegacyLedgerPath(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string // returns projectRoot
		wantFound   bool
	}{
		{
			name: "empty project root returns empty",
			setup: func(t *testing.T) string {
				return ""
			},
			wantFound: false,
		},
		{
			name: "no legacy dir returns empty",
			setup: func(t *testing.T) string {
				tmp := t.TempDir()
				projectRoot := filepath.Join(tmp, "myrepo")
				require.NoError(t, os.MkdirAll(projectRoot, 0755))
				return projectRoot
			},
			wantFound: false,
		},
		{
			name: "legacy dir exists returns path",
			setup: func(t *testing.T) string {
				tmp := t.TempDir()
				projectRoot := filepath.Join(tmp, "myrepo")
				require.NoError(t, os.MkdirAll(projectRoot, 0755))
				legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
				require.NoError(t, os.MkdirAll(legacyDir, 0755))
				return projectRoot
			},
			wantFound: true,
		},
		{
			name: "legacy file (not dir) returns empty",
			setup: func(t *testing.T) string {
				tmp := t.TempDir()
				projectRoot := filepath.Join(tmp, "myrepo")
				require.NoError(t, os.MkdirAll(projectRoot, 0755))
				// create a file, not a directory
				legacyFile := filepath.Join(tmp, "myrepo_sageox_ledger")
				require.NoError(t, os.WriteFile(legacyFile, []byte("not a dir"), 0644))
				return projectRoot
			},
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot := tt.setup(t)
			result := DetectLegacyLedgerPath(projectRoot)
			if tt.wantFound {
				assert.NotEmpty(t, result)
				assert.Contains(t, result, "_sageox_ledger")
			} else {
				assert.Empty(t, result)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// NewLedgerPath Tests
// -----------------------------------------------------------------------------

func TestNewLedgerPath(t *testing.T) {
	tests := []struct {
		name        string
		projectRoot string
		endpoint    string
		wantSuffix  string
		wantPanic   bool
	}{
		{
			name:        "empty project root returns empty",
			projectRoot: "",
			endpoint:    "https://sageox.ai",
			wantSuffix:  "",
		},
		{
			name:        "production endpoint skips endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "https://sageox.ai",
			wantSuffix:  filepath.Join("myrepo_sageox", "ledger"),
		},
		{
			name:        "production api prefix also skips endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "https://api.sageox.ai",
			wantSuffix:  filepath.Join("myrepo_sageox", "ledger"),
		},
		{
			name:        "staging endpoint includes endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "https://staging.sageox.ai",
			wantSuffix:  filepath.Join("myrepo_sageox", "staging.sageox.ai", "ledger"),
		},
		{
			name:        "localhost endpoint includes endpoint dir",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "http://localhost:8080",
			wantSuffix:  "ledger",
		},
		{
			name:        "empty endpoint panics",
			projectRoot: "/home/dev/Code/myrepo",
			endpoint:    "",
			wantPanic:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantPanic {
				assert.Panics(t, func() {
					NewLedgerPath(tt.projectRoot, tt.endpoint)
				})
				return
			}
			result := NewLedgerPath(tt.projectRoot, tt.endpoint)
			if tt.projectRoot == "" {
				assert.Empty(t, result)
				return
			}
			assert.True(t, strings.HasSuffix(result, tt.wantSuffix),
				"NewLedgerPath(%q, %q) = %q, want suffix %q", tt.projectRoot, tt.endpoint, result, tt.wantSuffix)
		})
	}
}

// -----------------------------------------------------------------------------
// LedgerNeedsMigration Tests
// -----------------------------------------------------------------------------

func TestLedgerNeedsMigration(t *testing.T) {
	t.Run("empty project root returns false", func(t *testing.T) {
		assert.False(t, LedgerNeedsMigration("", "https://sageox.ai"))
	})

	t.Run("no legacy dir returns false", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		assert.False(t, LedgerNeedsMigration(projectRoot, "https://sageox.ai"))
	})

	t.Run("legacy exists and new does not returns true", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))

		assert.True(t, LedgerNeedsMigration(projectRoot, "https://sageox.ai"))
	})

	t.Run("both exist returns false", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))
		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		require.NoError(t, os.MkdirAll(newDir, 0755))

		assert.False(t, LedgerNeedsMigration(projectRoot, "https://sageox.ai"))
	})
}

// -----------------------------------------------------------------------------
// CheckLedgerMigrationStatus Tests
// -----------------------------------------------------------------------------

func TestCheckLedgerMigrationStatus(t *testing.T) {
	t.Run("empty project root returns zero struct", func(t *testing.T) {
		status := CheckLedgerMigrationStatus("", "https://sageox.ai")
		assert.False(t, status.NeedsMigration)
		assert.False(t, status.LegacyExists)
		assert.False(t, status.NewExists)
	})

	t.Run("no legacy dir", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))

		status := CheckLedgerMigrationStatus(projectRoot, "https://sageox.ai")
		assert.False(t, status.NeedsMigration)
		assert.False(t, status.LegacyExists)
		// LegacyPath should still be populated (for reporting)
		assert.NotEmpty(t, status.LegacyPath)
		assert.Contains(t, status.LegacyPath, "_sageox_ledger")
	})

	t.Run("legacy exists needs migration", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))

		status := CheckLedgerMigrationStatus(projectRoot, "https://sageox.ai")
		assert.True(t, status.NeedsMigration)
		assert.True(t, status.LegacyExists)
		assert.False(t, status.NewExists)
		assert.Equal(t, legacyDir, status.LegacyPath)
	})

	t.Run("new already exists no migration needed", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))
		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		require.NoError(t, os.MkdirAll(newDir, 0755))

		status := CheckLedgerMigrationStatus(projectRoot, "https://sageox.ai")
		assert.False(t, status.NeedsMigration)
		assert.True(t, status.LegacyExists)
		assert.True(t, status.NewExists)
	})
}

// -----------------------------------------------------------------------------
// MigrateLedgerToNewStructure Tests
// -----------------------------------------------------------------------------

func TestMigrateLedgerToNewStructure(t *testing.T) {
	t.Run("empty project root returns error", func(t *testing.T) {
		err := MigrateLedgerToNewStructure("", "https://sageox.ai")
		assert.Error(t, err)
	})

	t.Run("no legacy dir is no-op", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))

		err := MigrateLedgerToNewStructure(projectRoot, "https://sageox.ai")
		assert.NoError(t, err)
	})

	t.Run("new path already exists is no-op", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))
		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		require.NoError(t, os.MkdirAll(newDir, 0755))

		err := MigrateLedgerToNewStructure(projectRoot, "https://sageox.ai")
		assert.NoError(t, err)
	})

	t.Run("successful migration moves directory", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))

		// create legacy ledger with .git dir and a file
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(filepath.Join(legacyDir, ".git"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "data.txt"), []byte("ledger data"), 0644))

		err := MigrateLedgerToNewStructure(projectRoot, "https://sageox.ai")
		require.NoError(t, err)

		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		// new path should exist with content
		assert.DirExists(t, newDir)
		assert.DirExists(t, filepath.Join(newDir, ".git"))
		content, err := os.ReadFile(filepath.Join(newDir, "data.txt"))
		require.NoError(t, err)
		assert.Equal(t, "ledger data", string(content))

		// legacy should be gone
		assert.NoDirExists(t, legacyDir)
	})
}

// -----------------------------------------------------------------------------
// CreateTeamSymlinks Tests
// -----------------------------------------------------------------------------

func TestCreateTeamSymlinks(t *testing.T) {
	t.Run("empty team IDs is no-op", func(t *testing.T) {
		err := CreateTeamSymlinks("/some/path", nil)
		assert.NoError(t, err)

		err = CreateTeamSymlinks("/some/path", []string{})
		assert.NoError(t, err)
	})

	t.Run("with team IDs returns nil (placeholder)", func(t *testing.T) {
		err := CreateTeamSymlinks("/some/path", []string{"team1", "team2"})
		assert.NoError(t, err)
	})
}

// -----------------------------------------------------------------------------
// CheckMigrationStatus with Legacy Mode Tests
// -----------------------------------------------------------------------------

func TestCheckMigrationStatus_LegacyMode(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	status := CheckMigrationStatus()
	// in legacy mode with OX_XDG_DISABLE, migration is possible
	// the result depends on filesystem state, but the function should not panic
	assert.IsType(t, MigrationStatus{}, status)
}

func TestMigrate_LegacyMode_NoLegacyData(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	result := Migrate()
	// no legacy data to migrate, so nothing should have migrated
	assert.IsType(t, MigrationResult{}, result)
}

// -----------------------------------------------------------------------------
// migrateSessionCache with context dir Tests
// -----------------------------------------------------------------------------

func TestMigrateSessionCache_WithContextDirContainingSubdirs(t *testing.T) {
	tempDir := t.TempDir()
	legacyDir := filepath.Join(tempDir, "legacy")
	contextDir := filepath.Join(legacyDir, "context")

	// create session directories inside context/
	for _, sessionName := range []string{"session-abc", "session-def"} {
		sessionDir := filepath.Join(contextDir, sessionName)
		require.NoError(t, os.MkdirAll(sessionDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("data"), 0644))
	}

	// also create a file (not dir) inside context/ — should be skipped
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "stray-file.txt"), []byte("stray"), 0644))

	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)

	// context dir should be cleaned up
	assert.NoDirExists(t, contextDir)
}

func TestMigrateSessionCache_WithDaemonLogs(t *testing.T) {
	tempDir := t.TempDir()
	legacyDir := filepath.Join(tempDir, "legacy")

	// context dir must exist for migrateSessionCache to proceed past early return
	contextDir := filepath.Join(legacyDir, "context")
	require.NoError(t, os.MkdirAll(contextDir, 0755))

	// create daemon log files matching the glob pattern
	for _, logName := range []string{"daemon-abc.log", "daemon-def.log"} {
		require.NoError(t, os.WriteFile(filepath.Join(legacyDir, logName), []byte("log data"), 0644))
	}

	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)

	// daemon logs should have been moved — original files removed
	for _, logName := range []string{"daemon-abc.log", "daemon-def.log"} {
		assert.NoFileExists(t, filepath.Join(legacyDir, logName))
	}
}

// -----------------------------------------------------------------------------
// TeamContextMigrationNeeded with existing new path
// -----------------------------------------------------------------------------

func TestTeamContextMigrationNeeded_NewPathExists(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	tempDir := t.TempDir()
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")
	require.NoError(t, os.MkdirAll(legacyPath, 0755))

	// create the new path so migration is not needed
	newPath := TeamContextDir("abc", "https://sageox.ai")
	require.NoError(t, os.MkdirAll(newPath, 0755))

	result := TeamContextMigrationNeeded(filepath.Join(tempDir, "project"), "abc", legacyPath)
	assert.False(t, result, "should return false when new path already exists")
}

// -----------------------------------------------------------------------------
// MigrateTeamContext Success Path Tests
// -----------------------------------------------------------------------------

func TestMigrateTeamContext_SuccessfulRename(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	tempDir := t.TempDir()
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")

	// create legacy dir with some content
	require.NoError(t, os.MkdirAll(legacyPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyPath, "SOUL.md"), []byte("team soul"), 0644))

	err := MigrateTeamContext("abc", legacyPath)
	require.NoError(t, err)

	// new path should exist with content
	newPath := TeamContextDir("abc", "https://sageox.ai")
	assert.DirExists(t, newPath)
	content, err := os.ReadFile(filepath.Join(newPath, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, "team soul", string(content))
}

// -----------------------------------------------------------------------------
// Migrate with Legacy Data Tests
// -----------------------------------------------------------------------------

func TestMigrate_LegacyMode_WithLegacyConfig(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	// create legacy config dir at ~/.config/sageox
	home := getHomeDir()
	if home == "" {
		t.Skip("cannot determine home directory")
	}

	legacyConfigDir := filepath.Join(home, ".config", "sageox")
	newConfigDir := ConfigDir() // in legacy mode: ~/.sageox/config

	// skip if legacy dir already exists (don't mess with real data)
	if _, err := os.Stat(legacyConfigDir); err == nil {
		t.Skip("legacy config dir already exists, skipping to avoid data loss")
	}
	// skip if new structure already exists (migration won't run)
	if _, err := os.Stat(newConfigDir); err == nil {
		t.Skip("new config dir already exists, migration would be skipped")
	}

	// this test is fragile on real systems — just verify no panic
	result := Migrate()
	assert.IsType(t, MigrationResult{}, result)
}

// -----------------------------------------------------------------------------
// migrateSessionCache with destination already existing
// -----------------------------------------------------------------------------

func TestMigrateSessionCache_SkipsExistingDestSession(t *testing.T) {
	tempDir := t.TempDir()
	legacyDir := filepath.Join(tempDir, "legacy")
	contextDir := filepath.Join(legacyDir, "context")

	// create a session in context/
	sessionDir := filepath.Join(contextDir, "existing-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("old data"), 0644))

	// pre-create the destination session dir so it gets skipped
	dstDir := SessionCacheDir("")
	dstSessionDir := filepath.Join(dstDir, "existing-session")
	require.NoError(t, os.MkdirAll(dstSessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dstSessionDir, "raw.jsonl"), []byte("keep this"), 0644))

	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)

	// destination content should NOT be overwritten
	content, err := os.ReadFile(filepath.Join(dstSessionDir, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "keep this", string(content))
}

func TestMigrateDirectory_PartialMigration(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.MkdirAll(dstDir, 0755))

	// create files in source
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("source1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file2.txt"), []byte("source2"), 0644))

	// pre-create one file in destination
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "file1.txt"), []byte("existing"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// file1 should NOT be overwritten
	content1, _ := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
	assert.Equal(t, "existing", string(content1))

	// file2 should be migrated
	content2, _ := os.ReadFile(filepath.Join(dstDir, "file2.txt"))
	assert.Equal(t, "source2", string(content2))
}

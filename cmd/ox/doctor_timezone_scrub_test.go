package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- JSON scrubber ---------------------------------------------------------
//
// Failure prevented: dead `timezone` keys in .sageox/config.json survive the
// team-timezone revert because struct decoding silently drops unknown fields.
// Without raw-map inspection, `ox doctor` would never notice them.

func TestScrubJSONTimezone_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		in           string
		wantScrubbed bool
		wantKeys     map[string]string // non-timezone keys that must survive
		wantAbsent   []string          // keys that must be gone after scrub
	}{
		{
			name: "no timezone key → no change",
			in: `{
  "org": "sageox",
  "project": "ox"
}`,
			wantScrubbed: false,
			wantKeys:     map[string]string{"org": "sageox", "project": "ox"},
			wantAbsent:   []string{"timezone"},
		},
		{
			name:         "timezone only → scrub to empty object",
			in:           `{"timezone": "America/New_York"}`,
			wantScrubbed: true,
			wantKeys:     map[string]string{},
			wantAbsent:   []string{"timezone"},
		},
		{
			name: "timezone + unrelated keys → scrub preserves unrelated",
			in: `{
  "org": "sageox",
  "timezone": "Europe/London",
  "project": "ox"
}`,
			wantScrubbed: true,
			wantKeys:     map[string]string{"org": "sageox", "project": "ox"},
			wantAbsent:   []string{"timezone"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.in), 0600))

			got, err := scrubJSONTimezone(path)
			require.NoError(t, err)
			assert.Equal(t, tt.wantScrubbed, got, "scrub return value")

			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var parsed map[string]string
			require.NoError(t, json.Unmarshal(raw, &parsed))

			for k, v := range tt.wantKeys {
				assert.Equal(t, v, parsed[k], "key %q should survive", k)
			}
			for _, k := range tt.wantAbsent {
				_, present := parsed[k]
				assert.False(t, present, "key %q should have been removed", k)
			}
		})
	}
}

func TestScrubJSONTimezone_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "org": "sageox",
  "timezone": "UTC"
}`), 0600))

	// first run — scrubs the key.
	got, err := scrubJSONTimezone(path)
	require.NoError(t, err)
	require.True(t, got)

	firstPass, err := os.ReadFile(path)
	require.NoError(t, err)

	// second run — must report no change and leave bytes untouched.
	got, err = scrubJSONTimezone(path)
	require.NoError(t, err)
	assert.False(t, got, "second scrub must be a no-op")

	secondPass, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, firstPass, secondPass, "idempotent run must not rewrite file")
}

func TestScrubJSONTimezone_MissingFile(t *testing.T) {
	t.Parallel()
	got, err := scrubJSONTimezone(filepath.Join(t.TempDir(), "nope.json"))
	require.NoError(t, err)
	assert.False(t, got)
}

func TestScrubJSONTimezone_InvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0600))

	got, err := scrubJSONTimezone(path)
	require.NoError(t, err)
	assert.False(t, got, "invalid JSON must be left untouched")

	raw, _ := os.ReadFile(path)
	assert.Equal(t, "not json", string(raw))
}

// --- TOML scrubber ---------------------------------------------------------
//
// Failure prevented: line-based removal loses surrounding comments, or strips
// table-scoped `timezone` keys that belong to unrelated tables.

func TestScrubTOMLTimezone_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		in           string
		want         string
		wantScrubbed bool
	}{
		{
			name:         "no timezone line → no change",
			in:           "session_recording = \"auto\"\n",
			want:         "session_recording = \"auto\"\n",
			wantScrubbed: false,
		},
		{
			name:         "timezone only → scrubbed to empty file",
			in:           "timezone = \"UTC\"\n",
			want:         "",
			wantScrubbed: true,
		},
		{
			name: "timezone with surrounding comments preserved",
			in: `# team config
# settings below

# legacy setting from v1
timezone = "America/New_York"
# end of legacy

session_recording = "auto"
`,
			want: `# team config
# settings below

# legacy setting from v1
# end of legacy

session_recording = "auto"
`,
			wantScrubbed: true,
		},
		{
			name: "timezone inside a table is NOT scrubbed",
			in: `[other]
timezone = "Europe/London"
`,
			want: `[other]
timezone = "Europe/London"
`,
			wantScrubbed: false,
		},
		{
			name: "timezone before a table stripped, table contents preserved",
			in: `timezone = "UTC"
session_recording = "auto"

[other]
timezone = "Europe/London"
`,
			want: `session_recording = "auto"

[other]
timezone = "Europe/London"
`,
			wantScrubbed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(tt.in), 0644))

			got, err := scrubTOMLTimezone(path)
			require.NoError(t, err)
			assert.Equal(t, tt.wantScrubbed, got, "scrub return value")

			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(raw))
		})
	}
}

func TestScrubTOMLTimezone_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`# leading comment
timezone = "UTC"
session_recording = "auto"
`), 0644))

	got, err := scrubTOMLTimezone(path)
	require.NoError(t, err)
	require.True(t, got)

	firstPass, err := os.ReadFile(path)
	require.NoError(t, err)

	got, err = scrubTOMLTimezone(path)
	require.NoError(t, err)
	assert.False(t, got)

	secondPass, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, firstPass, secondPass, "idempotent run must not rewrite file")
}

func TestScrubTOMLTimezone_MissingFile(t *testing.T) {
	t.Parallel()
	got, err := scrubTOMLTimezone(filepath.Join(t.TempDir(), "nope.toml"))
	require.NoError(t, err)
	assert.False(t, got)
}

// --- Doctor check wrapper --------------------------------------------------

func TestCheckTimezoneScrub_NotInGitRepo(t *testing.T) {
	// cannot be t.Parallel — changes cwd.
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(prev) })
	require.NoError(t, os.Chdir(dir))

	result := checkTimezoneScrub(false)
	assert.True(t, result.skipped, "should skip outside git repo")
}

func TestIsTOMLTimezoneLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{`timezone = "UTC"`, true},
		{`timezone="UTC"`, true},
		{`timezone  =  "UTC"`, true},
		{`timezone_setting = "UTC"`, false},
		{`# timezone = "UTC"`, false},
		{`session_recording = "auto"`, false},
		{``, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isTOMLTimezoneLine(tt.in))
		})
	}
}

// TestCheckTimezoneScrub_ScrubsProjectAndTeamConfigs exercises the full check
// wrapper: a real git repo with a .sageox/config.json containing a dead
// timezone key plus a team context whose config.toml also carries one. Both
// must be scrubbed, and the result must surface as a single info-priority
// warning (not a failure, so exit code stays 0).
//
// Failure prevented: the scrubber helpers work in isolation but the check
// wiring (findGitRoot + LoadLocalConfig loop + result aggregation) silently
// regresses, e.g. dropping the team iteration or misclassifying the priority.
func TestCheckTimezoneScrub_ScrubsProjectAndTeamConfigs(t *testing.T) {
	// cannot be t.Parallel — Chdir is process-global.
	gitRoot := t.TempDir()

	initCmd := exec.Command("git", "init")
	initCmd.Dir = gitRoot
	require.NoError(t, initCmd.Run())

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	projectConfig := `{
  "org": "sageox",
  "project": "ox",
  "timezone": "America/New_York"
}`
	projectPath := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(projectPath, []byte(projectConfig), 0600))

	teamDir := t.TempDir()
	teamConfigPath := filepath.Join(teamDir, "config.toml")
	teamConfig := `# team header comment
timezone = "Europe/London"
session_recording = "auto"
`
	require.NoError(t, os.WriteFile(teamConfigPath, []byte(teamConfig), 0644))

	localCfg := &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{TeamID: "sageox", TeamName: "SageOx", Path: teamDir},
		},
	}
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	t.Chdir(gitRoot)
	result := checkTimezoneScrub(true)

	assert.True(t, result.passed, "scrub is info-level, result should be passed=true")
	assert.True(t, result.warning, "scrub should surface as a warning")
	assert.Equal(t, "info", result.priority, "scrub priority must be info")
	assert.Contains(t, result.message, "2 file(s)", "message should report both files")
	assert.Contains(t, result.detail, ".sageox/config.json", "detail lists project config")
	assert.Contains(t, result.detail, "team sageox config.toml", "detail lists team config")

	// project config.json timezone key must be gone, unrelated keys preserved.
	rawProject, err := os.ReadFile(projectPath)
	require.NoError(t, err)
	var parsedProject map[string]string
	require.NoError(t, json.Unmarshal(rawProject, &parsedProject))
	_, present := parsedProject["timezone"]
	assert.False(t, present, "project config timezone must be removed")
	assert.Equal(t, "sageox", parsedProject["org"], "unrelated project keys preserved")
	assert.Equal(t, "ox", parsedProject["project"], "unrelated project keys preserved")

	// team config.toml timezone line must be gone, comment + other keys preserved.
	rawTeam, err := os.ReadFile(teamConfigPath)
	require.NoError(t, err)
	teamStr := string(rawTeam)
	assert.NotContains(t, teamStr, "timezone", "team config timezone line must be removed")
	assert.Contains(t, teamStr, "# team header comment", "leading comment preserved")
	assert.Contains(t, teamStr, `session_recording = "auto"`, "unrelated team keys preserved")
}

// TestScrubJSONTimezone_PreservesFileMode verifies the auto-fix does not
// silently tighten or loosen permissions on an existing config file.
//
// Failure prevented: a hardcoded 0600 or 0644 in the writer path silently
// changes permissions on scrubbed files, surprising users whose configs were
// group-readable by design.
func TestScrubJSONTimezone_PreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not meaningful on Windows")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"timezone": "UTC"}`), 0640))

	got, err := scrubJSONTimezone(path)
	require.NoError(t, err)
	require.True(t, got, "scrub should report a change")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm(), "file mode must be preserved")
}

// TestScrubTOMLTimezone_PreservesFileMode verifies the TOML writer also
// preserves the source file mode. Same rationale as the JSON variant.
func TestScrubTOMLTimezone_PreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not meaningful on Windows")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("timezone = \"UTC\"\nsession_recording = \"auto\"\n"), 0640))

	got, err := scrubTOMLTimezone(path)
	require.NoError(t, err)
	require.True(t, got, "scrub should report a change")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm(), "file mode must be preserved")
}

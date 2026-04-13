package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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

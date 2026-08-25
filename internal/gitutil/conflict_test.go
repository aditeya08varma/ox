package gitutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHasConflictMarkers covers the shapes HasConflictMarkers must
// distinguish. Failure prevented: a caller stages a file with this check as
// its only guard against baking conflict markers into a commit — a false
// negative here means real corruption ships silently.
func TestHasConflictMarkers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "clean file",
			content: `{"summary_status": "ok"}`,
			want:    false,
		},
		{
			name: "canonical three-way conflict",
			content: `{
<<<<<<< Updated upstream
  "summary_attempts": 3,
=======
  "summary_attempts": 2,
>>>>>>> Stashed changes
}`,
			want: true,
		},
		{
			name:    "marker must start the line, not just appear mid-line",
			content: `{"note": "the diff had <<<<<<< in it but this line doesn't start with it"}`,
			want:    false,
		},
		{
			name:    "empty file",
			content: "",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "meta.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0644))

			got, err := HasConflictMarkers(path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHasConflictMarkers_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := HasConflictMarkers(filepath.Join(t.TempDir(), "does-not-exist.json"))
	assert.Error(t, err)
}

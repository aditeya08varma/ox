package version

import (
	"testing"
)

func TestFull(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		buildDate string
		want      string
	}{
		{
			name:      "unknown build date returns version only",
			version:   "0.5.0",
			buildDate: "unknown",
			want:      "0.5.0",
		},
		{
			name:      "empty build date returns version only",
			version:   "0.5.0",
			buildDate: "",
			want:      "0.5.0",
		},
		{
			name:      "real build date appended",
			version:   "0.5.0",
			buildDate: "2026-03-25T12:00:00Z",
			want:      "0.5.0+2026-03-25T12:00:00Z",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origVersion := Version
			origBuildDate := BuildDate
			t.Cleanup(func() {
				Version = origVersion
				BuildDate = origBuildDate
			})

			Version = tc.version
			BuildDate = tc.buildDate

			got := Full()
			if got != tc.want {
				t.Errorf("Full() = %q, want %q", got, tc.want)
			}
		})
	}
}

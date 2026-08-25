package gitutil

import (
	"fmt"
	"os"
	"strings"
)

// ConflictMarkerStart is git's textual conflict marker. We look for it as a
// line prefix (not merely a substring) so legitimate content that happens to
// contain the literal characters mid-line — a quoted diff in a session
// transcript, for instance — doesn't false-positive.
const ConflictMarkerStart = "<<<<<<<"

// HasConflictMarkers reports whether the file at path contains an unresolved
// git conflict marker.
//
// This exists because `git add` does not refuse a conflicted path — it takes
// the file's current working-tree content, markers included, and stages it
// as "resolved." A caller that adds a conflicted path alongside genuinely
// clean changes and then commits bakes the markers permanently into history.
// Callers that stage files for an unattended/automatic commit MUST check
// this first and exclude (or refuse on) any match; git will not do it for
// them.
func HasConflictMarkers(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return HasConflictMarkersBytes(data), nil
}

// HasConflictMarkersBytes is the scan behind HasConflictMarkers, taking raw
// content directly. Use this when the content under inspection isn't (or
// might not be) the working-tree file — e.g. a staged git blob read via
// `git show :<path>`, which can differ from what's currently on disk.
func HasConflictMarkersBytes(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, ConflictMarkerStart) {
			return true
		}
	}
	return false
}

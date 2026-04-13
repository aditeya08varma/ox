package read

import "path/filepath"

// dailyDir, weeklyDir, monthlyDir return the absolute path of the given
// layer directory under a team-context root. Kept trivial on purpose:
// the reader's path math lives in one place so a future move of the
// memory/ tree is a single edit.
func dailyDir(teamRoot string) string {
	return filepath.Join(teamRoot, "memory", "daily")
}

func weeklyDir(teamRoot string) string {
	return filepath.Join(teamRoot, "memory", "weekly")
}

func monthlyDir(teamRoot string) string {
	return filepath.Join(teamRoot, "memory", "monthly")
}

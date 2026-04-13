package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// checkTimezoneScrub auto-removes dead `timezone` keys from .sageox/config.json
// and team config.toml files. These keys were dropped from the ProjectConfig
// and TeamConfig structs during the team-timezone revert. Struct decoding
// silently ignores them, so we inspect the raw JSON/TOML to catch stray values
// left behind by older ox versions.
//
// The `fix` parameter is ignored: scrubbing a dead key is always safe, so
// this check runs unconditionally under FixLevelAuto.
//
// Failure prevented: silent drift where orphaned timezone keys linger in user
// configs and mislead operators into thinking the feature still exists.
func checkTimezoneScrub(_ bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Timezone cleanup", "not in git repo", "")
	}

	var scrubbed []string

	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	projectDidScrub, err := scrubJSONTimezone(configPath)
	if err != nil {
		return FailedCheck("Timezone cleanup", "config.json scrub failed", err.Error())
	}
	if projectDidScrub {
		scrubbed = append(scrubbed, ".sageox/config.json")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err == nil {
		for _, tc := range localCfg.TeamContexts {
			if tc.Path == "" {
				continue
			}
			tomlPath := filepath.Join(tc.Path, "config.toml")
			teamDidScrub, err := scrubTOMLTimezone(tomlPath)
			if err != nil {
				return FailedCheck("Timezone cleanup",
					fmt.Sprintf("team %s config.toml scrub failed", tc.TeamID),
					err.Error())
			}
			if teamDidScrub {
				scrubbed = append(scrubbed, fmt.Sprintf("team %s config.toml", tc.TeamID))
			}
		}
	}

	if len(scrubbed) == 0 {
		return PassedCheck("Timezone cleanup", "none")
	}
	return InfoCheck("Timezone cleanup",
		fmt.Sprintf("removed dead timezone key from %d file(s)", len(scrubbed)),
		strings.Join(scrubbed, ", "))
}

// scrubJSONTimezone removes a top-level "timezone" key from a JSON file if
// present. Returns (true, nil) if a key was removed, (false, nil) if the file
// is missing, not valid JSON, or already clean. Uses map[string]json.RawMessage
// so unrelated values round-trip unchanged. The existing file mode is
// preserved so the auto-fix never silently tightens or loosens permissions.
func scrubJSONTimezone(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, nil
	}

	if _, ok := raw["timezone"]; !ok {
		return false, nil
	}
	delete(raw, "timezone")

	newData, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, err
	}
	newData = append(newData, '\n')
	if err := os.WriteFile(path, newData, info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

// scrubTOMLTimezone removes any top-level `timezone = ...` line from a TOML
// file, preserving all surrounding lines including comments. Lines inside a
// `[table]` section are ignored — only the root-level assignment is scrubbed.
// Returns (true, nil) if a line was removed, (false, nil) otherwise. The
// existing file mode is preserved so the auto-fix never silently tightens or
// loosens permissions.
func scrubTOMLTimezone(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	changed := false
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inTable = true
			out = append(out, line)
			continue
		}
		if !inTable && isTOMLTimezoneLine(trimmed) {
			changed = true
			continue
		}
		out = append(out, line)
	}

	if !changed {
		return false, nil
	}

	newContent := strings.Join(out, "\n")
	if err := os.WriteFile(path, []byte(newContent), info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

// isTOMLTimezoneLine reports whether a trimmed line is a top-level
// `timezone = ...` assignment.
func isTOMLTimezoneLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "timezone") {
		return false
	}
	rest := strings.TrimPrefix(trimmed, "timezone")
	rest = strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(rest, "=")
}

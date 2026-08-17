// Package omppaths resolves the session locations used by OMP.
//
// OMP retains Pi's PI_* path overrides for compatibility while using .omp as
// its default config root. The adapter and daemon share this package so a
// supported location cannot be discovered by one and rejected by the other.
package omppaths

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// SessionRoot describes an OMP session directory. Direct is true when the
// directory itself contains JSONL files rather than project subdirectories.
type SessionRoot struct {
	Path   string
	Direct bool
}

// SessionRoots mirrors OMP's directory resolver using the current process
// environment. Environment overrides are trusted configuration inherited by
// the daemon; adapter IPC cannot supply or widen them.
func SessionRoots(home string) []SessionRoot {
	if home == "" {
		return nil
	}

	var roots []SessionRoot
	if direct := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR")); direct != "" {
		roots = append(roots, SessionRoot{Path: expandHome(direct, home), Direct: true})
	}

	configName := os.Getenv("PI_CONFIG_DIR")
	if configName == "" {
		configName = ".omp"
	}
	configRoot := filepath.Join(home, configName)
	profile := activeProfile()

	agentDir := filepath.Join(configRoot, "agent")
	if profile != "" {
		agentDir = filepath.Join(configRoot, "profiles", profile, "agent")
	} else if override := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); override != "" {
		agentDir = expandHome(override, home)
	}
	roots = append(roots, SessionRoot{Path: filepath.Join(agentDir, "sessions")})

	// Search the default and all existing named-profile stores so import-by-id
	// works outside the profile process that wrote the transcript.
	roots = append(roots, SessionRoot{Path: filepath.Join(configRoot, "agent", "sessions")})
	appendProfileRoots(&roots, filepath.Join(configRoot, "profiles"), true)

	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
			xdgRoot := filepath.Join(expandHome(xdg, home), "omp")
			if info, err := os.Stat(xdgRoot); err == nil && info.IsDir() {
				roots = append(roots, SessionRoot{Path: filepath.Join(xdgRoot, "sessions")})
				appendProfileRoots(&roots, filepath.Join(xdgRoot, "profiles"), false)
			}
		}
	}

	// OMP migrates the legacy upstream Pi directory on a best-effort basis.
	roots = append(roots, SessionRoot{Path: filepath.Join(home, ".pi", "agent", "sessions")})
	return dedupe(roots)
}

// ProjectDirectoryNames returns OMP's current home-relative, temporary, or
// absolute directory encoding, followed by accepted legacy and hashed forms.
func ProjectDirectoryNames(cwd, home string) []string {
	canonicalCWD := canonicalPath(cwd)
	canonicalHome := canonicalPath(home)
	canonicalTemp := canonicalPath(os.TempDir())

	var current string
	var scope string
	switch {
	case pathWithin(canonicalHome, canonicalCWD):
		rel, _ := filepath.Rel(canonicalHome, canonicalCWD)
		current = encodeRelative("-", rel)
		scope = "home"
	case pathWithin(canonicalTemp, canonicalCWD):
		rel, _ := filepath.Rel(canonicalTemp, canonicalCWD)
		current = encodeRelative("-tmp", rel)
		scope = "tmp"
	default:
		current = legacyAbsoluteDirName(canonicalCWD)
		scope = "abs"
	}

	names := []string{current, legacyAbsoluteDirName(canonicalCWD), hashedDirName(canonicalCWD, scope)}
	seen := make(map[string]bool, len(names))
	out := names[:0]
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func activeProfile() string {
	profile, ok := os.LookupEnv("OMP_PROFILE")
	if !ok {
		profile = os.Getenv("PI_PROFILE")
	}
	profile = strings.TrimSpace(profile)
	if profile == "" || profile == "." || profile == ".." || strings.ContainsAny(profile, `/\`) {
		return ""
	}
	return profile
}

func appendProfileRoots(roots *[]SessionRoot, profilesDir string, hasAgentDir bool) {
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(profilesDir, entry.Name())
		if hasAgentDir {
			path = filepath.Join(path, "agent")
		}
		*roots = append(*roots, SessionRoot{Path: filepath.Join(path, "sessions")})
	}
}

func dedupe(roots []SessionRoot) []SessionRoot {
	seen := make(map[string]bool, len(roots))
	out := make([]SessionRoot, 0, len(roots))
	for _, root := range roots {
		root.Path = filepath.Clean(root.Path)
		if root.Path == "." || seen[root.Path] {
			continue
		}
		seen[root.Path] = true
		out = append(out, root)
	}
	return out
}

func expandHome(path, home string) string {
	switch {
	case path == "~":
		return home
	case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`):
		return filepath.Join(home, path[2:])
	case filepath.IsAbs(path):
		return filepath.Clean(path)
	default:
		return filepath.Join(home, path)
	}
}

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func encodeRelative(prefix, relative string) string {
	encoded := strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(relative)
	if encoded == "." || encoded == "" {
		return prefix
	}
	if strings.HasSuffix(prefix, "-") {
		return prefix + encoded
	}
	return prefix + "-" + encoded
}

func legacyAbsoluteDirName(cwd string) string {
	trimmed := strings.TrimLeft(cwd, `/\`)
	return "--" + strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(trimmed) + "--"
}

func hashedDirName(cwd, scope string) string {
	base := filepath.Base(cwd)
	var readable strings.Builder
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r) {
			readable.WriteRune(r)
		} else if readable.Len() == 0 || !strings.HasSuffix(readable.String(), "-") {
			readable.WriteByte('-')
		}
	}
	name := strings.Trim(readable.String(), "-")
	if len(name) > 80 {
		name = name[len(name)-80:]
	}
	if name == "" {
		name = "project"
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(cwd)))
	return scope + "-" + name + "-" + hex.EncodeToString(sum[:])
}

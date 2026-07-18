package decision

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sageox/ox/internal/config"
)

// resolvePaths returns the decision dirs/globs for this repo: the configured
// decision.paths, else the DefaultDecisionDirs that exist on disk. Empty means
// "no corpus here".
func resolvePaths(gitRoot string, cfg *config.DecisionConfig) []string {
	if gitRoot == "" {
		return nil
	}
	if !cfg.IsEmpty() {
		return cfg.Paths
	}
	return existingDefaultDirs(gitRoot)
}

func existingDefaultDirs(gitRoot string) []string {
	var dirs []string
	for _, d := range config.DefaultDecisionDirs {
		if fi, err := os.Stat(filepath.Join(gitRoot, d)); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// CorpusDetected reports whether any decision corpus exists for this project —
// the cheap gate `ox agent prime` uses before spending tokens on DR guidance.
func CorpusDetected(gitRoot string) bool {
	if gitRoot == "" {
		return false
	}
	cfg, _ := config.LoadProjectConfig(gitRoot)
	if cfg != nil && !cfg.Decision.IsEmpty() {
		return true
	}
	return len(existingDefaultDirs(gitRoot)) > 0
}

// LoadCorpus discovers and parses every DR in the repo's decision paths,
// fresh per call — no persisted index. Corpora are hundreds of small files at
// most, so a walk+parse is a few milliseconds; the searchable full-text index
// already exists in codedb (`ox code search` reaches these same files).
// Fail-open throughout: unreadable files are skipped with a debug log.
func LoadCorpus(gitRoot string, cfg *config.DecisionConfig) []Record {
	paths := resolvePaths(gitRoot, cfg)
	if len(paths) == 0 {
		return nil
	}

	var records []Record
	for _, file := range listFiles(gitRoot, paths) {
		data, err := os.ReadFile(file)
		if err != nil {
			slog.Debug("decision: unreadable file skipped", "path", file, "error", err)
			continue
		}
		rec := ParseContent(file, string(data))
		if !rec.IsRecord() {
			continue // plain markdown, not DR-shaped
		}
		if fi, err := os.Stat(file); err == nil {
			rec.Mtime = fi.ModTime().Unix()
			rec.Size = fi.Size()
		}
		if rel, err := filepath.Rel(gitRoot, file); err == nil {
			rec.RelPath = rel
		}
		rec.Corpus = "repo"
		records = append(records, rec)
	}

	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Number != records[j].Number {
			return records[i].Number < records[j].Number
		}
		return records[i].RelPath < records[j].RelPath
	})
	return records
}

// listFiles expands dirs (recursive *.md) and doublestar globs into a deduped
// file list. README.md is excluded everywhere — corpus index tables, not DRs.
func listFiles(gitRoot string, patterns []string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(p string) {
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			return
		}
		if strings.EqualFold(filepath.Base(p), "README.md") {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	for _, pat := range patterns {
		full := filepath.Join(gitRoot, pat)
		if fi, err := os.Stat(full); err == nil && fi.IsDir() {
			_ = filepath.WalkDir(full, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil // fail-open per entry
				}
				add(p)
				return nil
			})
			continue
		}
		matches, err := doublestar.Glob(os.DirFS(gitRoot), pat)
		if err != nil {
			slog.Debug("decision: bad glob", "pattern", pat, "error", err)
			continue
		}
		for _, m := range matches {
			add(filepath.Join(gitRoot, m))
		}
	}
	return out
}

// PrimaryDir returns the corpus dir new DRs should land in: the first
// configured/default path that is a plain directory. Empty when the corpus is
// glob-only or absent.
func PrimaryDir(gitRoot string, cfg *config.DecisionConfig) string {
	for _, p := range resolvePaths(gitRoot, cfg) {
		if fi, err := os.Stat(filepath.Join(gitRoot, p)); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

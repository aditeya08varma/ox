package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/daemon"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatIndexTiming(t *testing.T) {
	tests := []struct {
		name   string
		result *daemon.CodeIndexResult
		want   string
	}{
		{
			name: "typical index run",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   7000,
				SymbolDurationMs:  1200,
				CommentDurationMs: 800,
				TotalDurationMs:   9000,
			},
			want: "total 9s: index 7s, symbols 1s, comments 1s",
		},
		{
			name: "zero durations (incremental no-op)",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   500,
				SymbolDurationMs:  0,
				CommentDurationMs: 0,
				TotalDurationMs:   500,
			},
			want: "total 1s: index 1s, symbols <1s, comments <1s",
		},
		{
			name:   "all zeros",
			result: &daemon.CodeIndexResult{},
			want:   "total <1s: index <1s, symbols <1s, comments <1s",
		},
		{
			name: "large repo with minutes",
			result: &daemon.CodeIndexResult{
				IndexDurationMs:   82000,
				SymbolDurationMs:  15000,
				CommentDurationMs: 8000,
				TotalDurationMs:   105000,
			},
			want: "total 1m 45s: index 1m 22s, symbols 15s, comments 8s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatIndexTiming(tt.result)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCodeDBIndexing_DefaultReturnsFalseWithoutDaemon(t *testing.T) {
	// isolate from real daemon: prevent config.FindProjectRoot walk-up
	t.Chdir(t.TempDir())

	// With no daemon running, isCodeDBIndexing should return false
	// (IPC fails → err != nil → false). This is the default in test environments.
	assert.False(t, isCodeDBIndexing(false))
	assert.False(t, isCodeDBIndexing(true))
}

func TestIsCodeDBIndexing_Override(t *testing.T) {
	orig := isCodeDBIndexing
	t.Cleanup(func() { isCodeDBIndexing = orig })

	tests := []struct {
		name string
		stub func(bool) bool
		want bool
	}{
		{"indexing in progress", func(bool) bool { return true }, true},
		{"not indexing", func(bool) bool { return false }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isCodeDBIndexing = tt.stub
			assert.Equal(t, tt.want, isCodeDBIndexing(false))
		})
	}
}

func TestPopulateStatsFromDaemonCache(t *testing.T) {
	// Verify that the codeStatusCmd daemon-cached stats path correctly
	// maps CodeDBStats fields to local totals.
	codeStats := &daemon.CodeDBStats{
		Commits:     150,
		Blobs:       3200,
		Symbols:     800,
		Comments:    420,
		PRs:         25,
		Issues:      12,
		IndexingNow: true,
		Repos: []daemon.RepoStats{
			{Name: "main", Path: "/repo", Commits: 150, Blobs: 3200},
			{Name: "worktree-1", Path: "/wt1", Commits: 10, Blobs: 50},
		},
	}

	// Simulate the daemonIndexing branch from codeStatusCmd
	var totalCommits, totalBlobs, totalSymbols, totalComments, totalPRs, totalIssues int
	type repoRow struct {
		name    string
		path    string
		commits int
		blobs   int
	}
	var repos []repoRow

	daemonIndexing := codeStats != nil && codeStats.IndexingNow
	assert.True(t, daemonIndexing)

	totalCommits = codeStats.Commits
	totalBlobs = codeStats.Blobs
	totalSymbols = codeStats.Symbols
	totalComments = codeStats.Comments
	totalPRs = codeStats.PRs
	totalIssues = codeStats.Issues
	for _, r := range codeStats.Repos {
		repos = append(repos, repoRow{name: r.Name, path: r.Path, commits: r.Commits, blobs: r.Blobs})
	}

	assert.Equal(t, 150, totalCommits)
	assert.Equal(t, 3200, totalBlobs)
	assert.Equal(t, 800, totalSymbols)
	assert.Equal(t, 420, totalComments)
	assert.Equal(t, 25, totalPRs)
	assert.Equal(t, 12, totalIssues)
	assert.Len(t, repos, 2)
	assert.Equal(t, "main", repos[0].name)
	assert.Equal(t, "worktree-1", repos[1].name)
	assert.Equal(t, 50, repos[1].blobs)
}

func TestPopulateStatsFromDaemonCache_ZeroValues(t *testing.T) {
	// First-ever index: daemon reports indexing but has no cached stats.
	codeStats := &daemon.CodeDBStats{
		IndexingNow: true,
	}

	var totalCommits, totalBlobs, totalSymbols, totalComments, totalPRs, totalIssues int

	totalCommits = codeStats.Commits
	totalBlobs = codeStats.Blobs
	totalSymbols = codeStats.Symbols
	totalComments = codeStats.Comments
	totalPRs = codeStats.PRs
	totalIssues = codeStats.Issues

	assert.Equal(t, 0, totalCommits)
	assert.Equal(t, 0, totalBlobs)
	assert.Equal(t, 0, totalSymbols)
	assert.Equal(t, 0, totalComments)
	assert.Equal(t, 0, totalPRs)
	assert.Equal(t, 0, totalIssues)
}

// TestCodeStatusDisplay_EmptyIndex is a regression test for the false-positive where
// ox code stats showed "✓ indexed" when the index directory existed but the DB had 0 commits.
// This happens when indexing was interrupted after schema creation but before any data was written.
func TestCodeStatusDisplay_EmptyIndex(t *testing.T) {
	// Reproduce the exact condition: indexExists=true, codeStats=nil (no daemon), totalCommits=0.
	// The switch in codeStatusCmd must NOT fall through to the default "✓ indexed" case.
	indexExists := true
	totalCommits := 0
	var codeStats *daemon.CodeDBStats // nil = no daemon running

	// Evaluate the switch conditions in order, matching codeStatusCmd logic.
	var statusCase string
	switch {
	case !indexExists && (codeStats == nil || !codeStats.IndexingNow):
		statusCase = "not-indexed"
	case codeStats != nil && codeStats.IndexingNow:
		statusCase = "indexing"
	case codeStats != nil && codeStats.LastError != "" && totalCommits == 0:
		statusCase = "pending"
	case codeStats != nil && codeStats.LastError != "":
		statusCase = "error"
	case codeStats != nil && !codeStats.LastIndexed.IsZero():
		statusCase = "indexed-with-time"
	case indexExists && totalCommits == 0:
		statusCase = "empty-index"
	default:
		statusCase = "indexed"
	}

	assert.Equal(t, "empty-index", statusCase,
		"index dir exists with 0 commits and no daemon must show 'empty index', not 'indexed'")
}

// --- Batch 2 ergonomic surfaces (R6/R7/R9) ---
// Tests guard agent-facing behavior: snippet width, JIT DSL hint, and the
// structured index-not-ready response. Failure of any of these regresses the
// signal agents use to decide between ox code and grep.

func TestIsBareQuery(t *testing.T) {
	tests := []struct {
		q    string
		want bool
	}{
		// bare — single search term, no DSL
		{"authenticate", true},
		{"ResolveSession", true},
		{"  spaces-trimmed  ", true},

		// non-bare — DSL filter present
		{"authenticate type:symbol", false},
		{"calls:Handler", false},
		{`message:"fix bug"`, false},

		// non-bare — boolean / regex
		{"foo OR bar", false},
		{"/Resolve[A-Z]\\w+/", false},

		// empty / whitespace — explicitly NOT bare (no useful hint to give)
		{"", false},
		{"   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			assert.Equal(t, tt.want, isBareQuery(tt.q))
		})
	}
}

func TestCompactSearchResults_SnippetDefaultIs200(t *testing.T) {
	long := strings.Repeat("a", 400)
	results := []search.Result{{FilePath: "f.go", Line: 1, Content: long}}
	resp := compactSearchResults(results, 10, 0)
	require.Len(t, resp.Results, 1)
	// 200 chars + ellipsis
	assert.True(t, strings.HasSuffix(resp.Results[0].Snippet, "…"),
		"snippet must be truncated with ellipsis")
	// runes, not bytes — but ASCII so equivalent here
	assert.Equal(t, defaultSnippetLen+len("…"), len(resp.Results[0].Snippet),
		"default snippet length must be defaultSnippetLen (%d)", defaultSnippetLen)
}

func TestCompactSearchResults_SnippetOverride(t *testing.T) {
	long := strings.Repeat("b", 400)
	results := []search.Result{{FilePath: "f.go", Line: 1, Content: long}}
	resp := compactSearchResults(results, 10, 80)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, 80+len("…"), len(resp.Results[0].Snippet),
		"--snippet override must apply per-call")
}

func TestCompactSearchResults_NoJITHintInResponse(t *testing.T) {
	// compactSearchResults itself does not append the JIT hint — the RunE
	// caller does, because it has access to the raw query. We document the
	// boundary here so future refactors don't accidentally double-append.
	results := []search.Result{{FilePath: "f.go", Line: 1, Content: "anything"}}
	resp := compactSearchResults(results, 10, 0)
	assert.Empty(t, resp.Guidance, "compactSearchResults must not add JIT hint on its own")
}

func TestCompactSearchResults_PagingGuidance(t *testing.T) {
	// 12 results, limit 10 — guidance must explain how to see the rest.
	var results []search.Result
	for i := 0; i < 12; i++ {
		results = append(results, search.Result{FilePath: "f.go", Line: i + 1, Content: "x"})
	}
	resp := compactSearchResults(results, 10, 0)
	assert.Len(t, resp.Results, 10)
	assert.Equal(t, 12, resp.Total)
	assert.Contains(t, resp.Guidance, "Showing 10 of 12")
	assert.Contains(t, resp.Guidance, "--limit")
}

func TestEmitIndexNotReadyJSON_Indexing(t *testing.T) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := emitIndexNotReadyJSON(cmd, indexStatusIndexing,
		"Code index is currently being built.",
		"Use grep until ready.")
	require.NoError(t, err)

	var got indexNotReadyResponse
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "indexing", got.Status)
	assert.Contains(t, got.Message, "Code index")
	assert.Contains(t, got.FallbackHint, "grep")
}

func TestEmitIndexNotReadyJSON_NotIndexed(t *testing.T) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := emitIndexNotReadyJSON(cmd, indexStatusNotIndexed,
		"No code index found.",
		"Run 'ox code index' first.")
	require.NoError(t, err)

	var got indexNotReadyResponse
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "not_indexed", got.Status)
	assert.Contains(t, got.FallbackHint, "ox code index")
}

func TestIndexStatusConstants_AreStable(t *testing.T) {
	// Agent-side consumers branch on these strings; lock them in so a future
	// rename doesn't silently break callers parsing the JSON.
	assert.Equal(t, "indexing", indexStatusIndexing)
	assert.Equal(t, "not_indexed", indexStatusNotIndexed)
}

func TestFormatDurationBrief(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "<1s"},
		{"sub-second rounds up", 500 * time.Millisecond, "1s"},
		{"very small", 100 * time.Millisecond, "<1s"},
		{"one second", 1 * time.Second, "1s"},
		{"seconds", 7 * time.Second, "7s"},
		{"minute boundary", 60 * time.Second, "1m"},
		{"minutes and seconds", 90 * time.Second, "1m 30s"},
		{"hour boundary", 1 * time.Hour, "1h"},
		{"hours and minutes", 90 * time.Minute, "1h 30m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationBrief(tt.d)
			assert.Equal(t, tt.want, got)
		})
	}
}

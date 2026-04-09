package index

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/ledger"
)

func TestIndexGitHubData_PRWithCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	// set up CodeDB store
	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// set up ledger with a PR that has commits
	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	pr := &ledger.PRFile{
		Number:      42,
		Title:       "Add feature X",
		Body:        "description",
		Author:      "alice",
		State:       "merged",
		Labels:      []string{"enhancement"},
		CreatedAt:   now,
		MergedAt:    &now,
		UpdatedAt:   now,
		MergeCommit: "merge123",
		URL:         "https://github.com/org/repo/pull/42",
		Comments: []ledger.PRComment{
			{Author: "bob", Body: "LGTM", CreatedAt: now},
		},
		Commits: []ledger.PRCommit{
			{SHA: "aaa111", Author: "alice", Date: now.Add(-2 * time.Hour), Msg: "first commit"},
			{SHA: "bbb222", Author: "alice", Date: now.Add(-1 * time.Hour), Msg: "second commit"},
			{SHA: "ccc333", Author: "bob", Date: now, Msg: "review fix"},
		},
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	// index
	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.PRsIndexed != 1 {
		t.Errorf("expected 1 PR indexed, got %d", stats.PRsIndexed)
	}

	// verify PR was indexed
	var prID int64
	var title string
	err = s.QueryRow("SELECT id, title FROM pull_requests WHERE number = 42").Scan(&prID, &title)
	if err != nil {
		t.Fatalf("query PR: %v", err)
	}
	if title != "Add feature X" {
		t.Errorf("expected title 'Add feature X', got %q", title)
	}

	// verify commits were indexed
	rows, err := s.Query("SELECT sha FROM pr_commits WHERE pr_id = ? ORDER BY rowid", prID)
	if err != nil {
		t.Fatalf("query commits: %v", err)
	}
	defer rows.Close()

	var shas []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			t.Fatalf("scan commit: %v", err)
		}
		shas = append(shas, sha)
	}

	if len(shas) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(shas))
	}
	if shas[0] != "aaa111" {
		t.Errorf("expected first commit SHA 'aaa111', got %q", shas[0])
	}
	if shas[2] != "ccc333" {
		t.Errorf("expected third commit SHA 'ccc333', got %q", shas[2])
	}

	// verify comments were also indexed
	var commentCount int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_comments WHERE pr_id = ?", prID).Scan(&commentCount); err != nil {
		t.Fatalf("query comment count: %v", err)
	}
	if commentCount != 1 {
		t.Errorf("expected 1 comment, got %d", commentCount)
	}
}

func TestIndexGitHubData_PRWithoutCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	pr := &ledger.PRFile{
		Number:    43,
		Title:     "Open PR",
		Author:    "bob",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.PRsIndexed != 1 {
		t.Errorf("expected 1 PR indexed, got %d", stats.PRsIndexed)
	}

	// verify no commits indexed
	var prID int64
	if err := s.QueryRow("SELECT id FROM pull_requests WHERE number = 43").Scan(&prID); err != nil {
		t.Fatalf("query PR id: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_commits WHERE pr_id = ?", prID).Scan(&count); err != nil {
		t.Fatalf("query commit count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 commits for open PR, got %d", count)
	}
}

func TestIndexGitHubData_UpsertReplacesCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	// first index: PR with 1 commit
	pr := &ledger.PRFile{
		Number: 44, Title: "PR", Author: "alice", State: "merged",
		CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeCommit: "m1",
		Commits: []ledger.PRCommit{
			{SHA: "old1", Author: "alice", Date: now, Msg: "old"},
		},
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData first pass: %v", err)
	}

	// update file (touch it to change mtime)
	pr.Commits = []ledger.PRCommit{
		{SHA: "new1", Author: "alice", Date: now, Msg: "new1"},
		{SHA: "new2", Author: "bob", Date: now, Msg: "new2"},
	}
	// small delay to ensure mtime changes
	time.Sleep(10 * time.Millisecond)
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write updated PR: %v", err)
	}

	// re-index
	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData second pass: %v", err)
	}

	// verify commits were replaced
	var prID int64
	if err := s.QueryRow("SELECT id FROM pull_requests WHERE number = 44").Scan(&prID); err != nil {
		t.Fatalf("query PR id: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_commits WHERE pr_id = ?", prID).Scan(&count); err != nil {
		t.Fatalf("query commit count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 commits after upsert, got %d", count)
	}

	var sha string
	if err := s.QueryRow("SELECT sha FROM pr_commits WHERE pr_id = ? ORDER BY rowid LIMIT 1", prID).Scan(&sha); err != nil {
		t.Fatalf("query first commit SHA: %v", err)
	}
	if sha != "new1" {
		t.Errorf("expected SHA 'new1' after upsert, got %q", sha)
	}
}

func TestIndexGitHubData_BackwardCompatOldJSON(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// write old-format JSON without commits field
	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(ledgerPath, "data", "github",
		now.Format("2006"), now.Format("01"), now.Format("02"), "pr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir old-format dir: %v", err)
	}

	oldJSON := `{
		"number": 99,
		"title": "Old PR format",
		"body": "",
		"author": "charlie",
		"state": "merged",
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"merge_commit": "old999",
		"url": "https://github.com/org/repo/pull/99"
	}`
	if err := os.WriteFile(filepath.Join(dir, "99.json"), []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write old-format JSON: %v", err)
	}

	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.PRsIndexed != 1 {
		t.Errorf("expected 1 PR indexed, got %d", stats.PRsIndexed)
	}

	// verify PR was indexed correctly with no commits
	var prID int64
	if err := s.QueryRow("SELECT id FROM pull_requests WHERE number = 99").Scan(&prID); err != nil {
		t.Fatalf("query PR id: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_commits WHERE pr_id = ?", prID).Scan(&count); err != nil {
		t.Fatalf("query commit count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 commits from old JSON, got %d", count)
	}
}

// TestIndexGitHubData_LexLastIsNotChronologicallyLast is the regression test
// for issue #474. The ledger holds N append-only snapshots of the same PR
// (e.g. {number}-{hash}.json), one per observed state over time. Lex order of
// content-hash filenames has no relationship to chronological order, so a
// chronologically-earlier snapshot can lex-sort *after* the chronologically-
// latest one. Before the fix, the indexer iterated files in lex order and let
// the last upsert win — silently picking the wrong snapshot as "current state".
//
// Failure prevented: PR #461 in the wild was extracted as state=open even though
// a state=merged snapshot existed in the ledger, because the merged snapshot's
// content hash lex-sorted earlier than a stale "open" snapshot taken 3 minutes
// before the merge.
func TestIndexGitHubData_LexLastIsNotChronologicallyLast(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// Build the ledger directory directly so we can control filenames precisely.
	// Both snapshots represent PR #100. The "merged" snapshot has the latest
	// updated_at but its filename hash lex-sorts BEFORE the stale "open" snapshot.
	ledgerPath := t.TempDir()
	dir := filepath.Join(ledgerPath, "data", "github", "2026", "04", "08", "pr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mergedJSON := `{
		"number": 100,
		"title": "Conditional commit attribution",
		"body": "",
		"author": "alice",
		"state": "merged",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-09T00:23:45Z",
		"merged_at": "2026-04-09T00:21:15Z",
		"merge_commit": "deadbeef",
		"url": "https://github.com/org/repo/pull/100"
	}`
	staleOpenJSON := `{
		"number": 100,
		"title": "Conditional commit attribution",
		"body": "",
		"author": "alice",
		"state": "open",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-09T00:18:13Z",
		"url": "https://github.com/org/repo/pull/100"
	}`

	// "aaa11111" lex-sorts BEFORE "zzzfffff" — the merged file is lex-FIRST,
	// the stale-open file is lex-LAST. Pre-fix, the lex-last file would win.
	if err := os.WriteFile(filepath.Join(dir, "100-aaa11111.json"), []byte(mergedJSON), 0o644); err != nil {
		t.Fatalf("write merged snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "100-zzzfffff.json"), []byte(staleOpenJSON), 0o644); err != nil {
		t.Fatalf("write stale open snapshot: %v", err)
	}

	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}

	var state string
	var mergedAt sql.NullInt64
	if err := s.QueryRow("SELECT state, merged_at FROM pull_requests WHERE number = 100").Scan(&state, &mergedAt); err != nil {
		t.Fatalf("query PR: %v", err)
	}
	if state != "merged" {
		t.Errorf("expected state=merged (latest snapshot wins), got %q — bug #474 reproduced", state)
	}
	if !mergedAt.Valid {
		t.Errorf("expected merged_at to be set from the latest snapshot")
	}
}

// TestIndexGitHubData_GroupedSkipReindexAfterNewSibling verifies that adding
// a NEW snapshot to a previously-indexed group correctly invalidates the skip
// cache: the group is reprocessed and the new file (if it's the latest) wins.
//
// Failure prevented: a stale group whose all-known-mtimes-match optimization
// would skip reindexing even after a new "merged" snapshot lands.
func TestIndexGitHubData_GroupedSkipReindexAfterNewSibling(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ledgerPath := t.TempDir()
	dir := filepath.Join(ledgerPath, "data", "github", "2026", "04", "08", "pr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// First snapshot: state=open
	openJSON := `{
		"number": 200,
		"title": "PR",
		"author": "alice",
		"state": "open",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-08T05:00:00Z",
		"url": "https://github.com/org/repo/pull/200"
	}`
	if err := os.WriteFile(filepath.Join(dir, "200-aaa00001.json"), []byte(openJSON), 0o644); err != nil {
		t.Fatalf("write open snapshot: %v", err)
	}

	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData first pass: %v", err)
	}

	var state string
	if err := s.QueryRow("SELECT state FROM pull_requests WHERE number = 200").Scan(&state); err != nil {
		t.Fatalf("query PR: %v", err)
	}
	if state != "open" {
		t.Fatalf("expected state=open after first index, got %q", state)
	}

	// Add a NEW snapshot (state=merged) — group should be re-picked
	mergedJSON := `{
		"number": 200,
		"title": "PR",
		"author": "alice",
		"state": "merged",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-09T00:00:00Z",
		"merged_at": "2026-04-09T00:00:00Z",
		"merge_commit": "abc123",
		"url": "https://github.com/org/repo/pull/200"
	}`
	if err := os.WriteFile(filepath.Join(dir, "200-bbb00002.json"), []byte(mergedJSON), 0o644); err != nil {
		t.Fatalf("write merged snapshot: %v", err)
	}

	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData second pass: %v", err)
	}

	if err := s.QueryRow("SELECT state FROM pull_requests WHERE number = 200").Scan(&state); err != nil {
		t.Fatalf("query PR after re-index: %v", err)
	}
	if state != "merged" {
		t.Errorf("expected state=merged after new snapshot landed, got %q", state)
	}
}

// TestIndexGitHubData_IssuesPickLatestSnapshot is the issue twin of the PR
// regression test. Same bug class, same fix.
func TestIndexGitHubData_IssuesPickLatestSnapshot(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ledgerPath := t.TempDir()
	dir := filepath.Join(ledgerPath, "data", "github", "2026", "04", "08", "issue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	closedJSON := `{
		"number": 300,
		"title": "Bug report",
		"body": "",
		"author": "alice",
		"state": "closed",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-09T02:00:00Z",
		"closed_at": "2026-04-09T02:00:00Z",
		"url": "https://github.com/org/repo/issues/300"
	}`
	staleOpenJSON := `{
		"number": 300,
		"title": "Bug report",
		"body": "",
		"author": "alice",
		"state": "open",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-09T01:00:00Z",
		"url": "https://github.com/org/repo/issues/300"
	}`

	// "111aaaaa" lex-sorts BEFORE "999fffff" — closed file is lex-FIRST.
	if err := os.WriteFile(filepath.Join(dir, "300-111aaaaa.json"), []byte(closedJSON), 0o644); err != nil {
		t.Fatalf("write closed snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "300-999fffff.json"), []byte(staleOpenJSON), 0o644); err != nil {
		t.Fatalf("write stale open snapshot: %v", err)
	}

	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}

	var state string
	if err := s.QueryRow("SELECT state FROM issues WHERE number = 300").Scan(&state); err != nil {
		t.Fatalf("query issue: %v", err)
	}
	if state != "closed" {
		t.Errorf("expected state=closed (latest snapshot wins), got %q", state)
	}
}

// TestParseSnapshotNumber covers both content-hashed and legacy filename
// formats, and rejects malformed inputs.
func TestParseSnapshotNumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		filename string
		want     int
	}{
		{"hashed", "461-cbe93273.json", 461},
		{"hashed_long_hash", "100-aaaaaaaa12345678.json", 100},
		{"legacy_plain", "42.json", 42},
		{"legacy_zero_padded", "007.json", 7},
		{"missing_extension", "461", -1},
		{"wrong_extension", "461.txt", -1},
		{"non_numeric_prefix", "abc-12345678.json", -1},
		{"empty", "", -1},
		{"just_extension", ".json", -1},
		{"dash_no_number", "-12345678.json", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseSnapshotNumber(tc.filename)
			if got != tc.want {
				t.Errorf("parseSnapshotNumber(%q) = %d, want %d", tc.filename, got, tc.want)
			}
		})
	}
}

// TestIndexGitHubData_LegacyAndHashedInSameGroup verifies that a legacy
// {number}.json file and a content-hashed {number}-{hash}.json file in the
// same directory are recognized as the same PR and grouped together — so the
// winner-by-updated_at picks the chronologically-latest snapshot regardless
// of filename format. This is the migration scenario where a ledger evolves
// from legacy to hashed format mid-life.
//
// Failure prevented: a stale legacy file silently overwriting a fresh hashed
// snapshot (or vice versa) because the indexer treated them as separate PRs.
func TestIndexGitHubData_LegacyAndHashedInSameGroup(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ledgerPath := t.TempDir()
	dir := filepath.Join(ledgerPath, "data", "github", "2026", "04", "08", "pr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Legacy format file: older state, written first
	legacyJSON := `{
		"number": 400,
		"title": "Legacy era PR",
		"body": "",
		"author": "alice",
		"state": "open",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-08T05:00:00Z",
		"url": "https://github.com/org/repo/pull/400"
	}`
	if err := os.WriteFile(filepath.Join(dir, "400.json"), []byte(legacyJSON), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// Content-hashed file: NEWER state — should win
	hashedJSON := `{
		"number": 400,
		"title": "Legacy era PR",
		"body": "",
		"author": "alice",
		"state": "merged",
		"created_at": "2026-04-08T01:00:00Z",
		"updated_at": "2026-04-09T03:00:00Z",
		"merged_at": "2026-04-09T02:55:00Z",
		"merge_commit": "feedface",
		"url": "https://github.com/org/repo/pull/400"
	}`
	if err := os.WriteFile(filepath.Join(dir, "400-aabbccdd.json"), []byte(hashedJSON), 0o644); err != nil {
		t.Fatalf("write hashed: %v", err)
	}

	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}

	// PRsIndexed counts groups that were processed, not files. If legacy and
	// hashed end up in separate groups (parseSnapshotNumber bug), this would
	// be 2 — the smoking gun for the failure mode this test guards against.
	// The DB row count alone can't catch it: upsert-by-number collapses two
	// separate group writes into one row regardless.
	if stats.PRsIndexed != 1 {
		t.Errorf("expected PRsIndexed=1 (legacy + hashed in single group), got %d", stats.PRsIndexed)
	}

	// Both files must be in the same group → hashed (newer) wins → state=merged
	var state string
	var mergedAt sql.NullInt64
	if err := s.QueryRow("SELECT state, merged_at FROM pull_requests WHERE number = 400").Scan(&state, &mergedAt); err != nil {
		t.Fatalf("query PR: %v", err)
	}
	if state != "merged" {
		t.Errorf("expected state=merged (hashed file is newer), got %q — legacy and hashed must group together", state)
	}
	if !mergedAt.Valid {
		t.Errorf("expected merged_at set from hashed snapshot")
	}

	// Only ONE row should exist (not two — legacy and hashed must collapse)
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM pull_requests WHERE number = 400").Scan(&count); err != nil {
		t.Fatalf("count PR rows: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row for PR 400 (legacy and hashed in same group), got %d", count)
	}

	// Reverse case: legacy is NEWER than hashed — legacy must win
	dir2 := filepath.Join(ledgerPath, "data", "github", "2026", "04", "09", "pr")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatalf("mkdir reverse: %v", err)
	}
	hashedOldJSON := `{
		"number": 401,
		"title": "Reverse",
		"author": "alice",
		"state": "open",
		"created_at": "2026-04-09T01:00:00Z",
		"updated_at": "2026-04-09T02:00:00Z",
		"url": "https://github.com/org/repo/pull/401"
	}`
	legacyNewJSON := `{
		"number": 401,
		"title": "Reverse",
		"author": "alice",
		"state": "closed",
		"created_at": "2026-04-09T01:00:00Z",
		"updated_at": "2026-04-09T05:00:00Z",
		"closed_at": "2026-04-09T05:00:00Z",
		"url": "https://github.com/org/repo/pull/401"
	}`
	if err := os.WriteFile(filepath.Join(dir2, "401-11111111.json"), []byte(hashedOldJSON), 0o644); err != nil {
		t.Fatalf("write hashed reverse: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "401.json"), []byte(legacyNewJSON), 0o644); err != nil {
		t.Fatalf("write legacy reverse: %v", err)
	}

	stats2, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData reverse: %v", err)
	}
	// Reverse case: PRsIndexed must be exactly 1 — proving the legacy 401.json
	// and hashed 401-11111111.json were grouped together. The PR 400 group is
	// already mtime-skipped from the prior call, so 1 means "only PR 401's
	// single group was processed", catching a regression where the legacy
	// format silently became its own group.
	if stats2.PRsIndexed != 1 {
		t.Errorf("reverse case: expected PRsIndexed=1 (legacy + hashed for PR 401 in single group), got %d", stats2.PRsIndexed)
	}
	if err := s.QueryRow("SELECT state FROM pull_requests WHERE number = 401").Scan(&state); err != nil {
		t.Fatalf("query reverse PR: %v", err)
	}
	if state != "closed" {
		t.Errorf("expected state=closed (legacy is newer), got %q", state)
	}
	// Same parity check as forward case: exactly one row.
	if err := s.QueryRow("SELECT COUNT(*) FROM pull_requests WHERE number = 401").Scan(&count); err != nil {
		t.Fatalf("count PR 401 rows: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row for PR 401, got %d", count)
	}
}

// TestGroupSnapshotsByNumber_AllUnparseableIsDeterministic verifies that when
// every candidate in a group fails JSON parsing, the winner is selected
// deterministically — not via map iteration order or filesystem walk order.
// Two files with identical (zero) updated_at and zero mtime should still
// produce a reproducible winner across runs.
//
// Failure prevented: a future refactor (e.g., parallel walk) silently changing
// which corrupt snapshot becomes the "current state" row, producing different
// DB content on different machines or different runs.
func TestGroupSnapshotsByNumber_AllUnparseableIsDeterministic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	garbage := `{not valid json`

	pathA := filepath.Join(dir, "1-aaaaaaaa.json")
	pathB := filepath.Join(dir, "1-bbbbbbbb.json")
	pathC := filepath.Join(dir, "1-cccccccc.json")
	for _, p := range []string{pathA, pathB, pathC} {
		if err := os.WriteFile(p, []byte(garbage), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// run grouping multiple times — the winner must be the same every run
	// even though map iteration over byKey is intentionally randomized by Go.
	first := groupSnapshotsByNumber([]string{pathA, pathB, pathC})
	if len(first) != 1 {
		t.Fatalf("expected 1 group, got %d", len(first))
	}
	expectedWinner := first[0].winner

	for i := 0; i < 50; i++ {
		got := groupSnapshotsByNumber([]string{pathC, pathA, pathB}) // shuffled input
		if len(got) != 1 {
			t.Fatalf("iteration %d: expected 1 group, got %d", i, len(got))
		}
		if got[0].winner != expectedWinner {
			t.Errorf("iteration %d: winner drifted from %q to %q — selection is not deterministic", i, expectedWinner, got[0].winner)
		}
	}

	// the deterministic fallback should be lex-greatest path
	// (this guards against the rule silently flipping to lex-least)
	if expectedWinner != pathC {
		t.Errorf("expected lex-greatest path %q to win the all-unparseable tie, got %q", pathC, expectedWinner)
	}
}

// TestCandidateBeats covers the comparison precedence directly so any
// reordering of the rules is immediately visible.
func TestCandidateBeats(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		cur, best snapshotCandidate
		want      bool
	}{
		{
			name: "parsed_beats_unparsed",
			cur:  snapshotCandidate{path: "/a", parsed: true, updatedAt: t1},
			best: snapshotCandidate{path: "/z", parsed: false},
			want: true,
		},
		{
			name: "unparsed_loses_to_parsed",
			cur:  snapshotCandidate{path: "/z", parsed: false},
			best: snapshotCandidate{path: "/a", parsed: true, updatedAt: t1},
			want: false,
		},
		{
			name: "later_updated_at_wins",
			cur:  snapshotCandidate{path: "/a", parsed: true, updatedAt: t2},
			best: snapshotCandidate{path: "/z", parsed: true, updatedAt: t1},
			want: true,
		},
		{
			name: "earlier_updated_at_loses",
			cur:  snapshotCandidate{path: "/a", parsed: true, updatedAt: t1},
			best: snapshotCandidate{path: "/z", parsed: true, updatedAt: t2},
			want: false,
		},
		{
			name: "tie_break_on_mtime",
			cur:  snapshotCandidate{path: "/a", parsed: true, updatedAt: t1, mtime: t2},
			best: snapshotCandidate{path: "/z", parsed: true, updatedAt: t1, mtime: t1},
			want: true,
		},
		{
			name: "tie_break_on_path_lex_greater",
			cur:  snapshotCandidate{path: "/z", parsed: true, updatedAt: t1, mtime: t1},
			best: snapshotCandidate{path: "/a", parsed: true, updatedAt: t1, mtime: t1},
			want: true,
		},
		{
			name: "tie_break_on_path_lex_lesser_loses",
			cur:  snapshotCandidate{path: "/a", parsed: true, updatedAt: t1, mtime: t1},
			best: snapshotCandidate{path: "/z", parsed: true, updatedAt: t1, mtime: t1},
			want: false,
		},
		{
			name: "all_zero_unparsed_path_lex_greater_wins",
			cur:  snapshotCandidate{path: "/z"},
			best: snapshotCandidate{path: "/a"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := candidateBeats(tc.cur, tc.best); got != tc.want {
				t.Errorf("candidateBeats(%+v, %+v) = %v, want %v", tc.cur, tc.best, got, tc.want)
			}
		})
	}
}

// TestGroupSnapshotsByNumber_TieBreakOnMtime verifies that when two snapshots
// have identical updated_at, the one with the later filesystem mtime wins.
// This catches the case where two captures landed in the same second.
func TestGroupSnapshotsByNumber_TieBreakOnMtime(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: filesystem mtime")
	}

	dir := t.TempDir()
	sameTime := `{"number": 1, "updated_at": "2026-04-08T12:00:00Z"}`

	earlier := filepath.Join(dir, "1-aaaaaaaa.json")
	later := filepath.Join(dir, "1-bbbbbbbb.json")

	if err := os.WriteFile(earlier, []byte(sameTime), 0o644); err != nil {
		t.Fatal(err)
	}
	// ensure mtimes differ even if the test runs fast
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(later, []byte(sameTime), 0o644); err != nil {
		t.Fatal(err)
	}

	groups := groupSnapshotsByNumber([]string{earlier, later})
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].winner != later {
		t.Errorf("expected mtime tie-break to pick %q, got %q", later, groups[0].winner)
	}
	if len(groups[0].allPaths) != 2 {
		t.Errorf("expected group to track both paths, got %d", len(groups[0].allPaths))
	}
}

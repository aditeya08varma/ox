package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/facts"
	"github.com/sageox/ox/pkg/sessionsummary"
)

// createSessionDir creates a session directory with a summary.json in the ledger.
func createSessionDir(t *testing.T, sessionsDir, dirName string, summary *sessionsummary.SummarizeResponse) {
	t.Helper()
	dir := filepath.Join(sessionsDir, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func testSummary(title string, score float64) *sessionsummary.SummarizeResponse {
	return &sessionsummary.SummarizeResponse{
		Title:        title,
		Summary:      "A test session about " + title,
		KeyActions:   []string{"did something"},
		Outcome:      "success",
		QualityScore: score,
		AgentSummary: &sessionsummary.AgentSummary{
			Decisions: []sessionsummary.Decision{
				{What: "Use PostgreSQL", Why: "Better for analytics", Owner: "alice"},
			},
			ActionItems: []sessionsummary.ActionItem{
				{Task: "Write migration", Assignee: "bob", Priority: "high"},
			},
			OpenQuestions: []sessionsummary.OpenQuestion{
				{Question: "Should we add an index?", Context: "Query perf concern"},
			},
		},
		AhaMoments: []sessionsummary.AhaMoment{
			{Seq: 1, Type: "insight", Highlight: "Token bucket is better than fixed window", Why: "Handles bursts"},
			{Seq: 2, Type: "decision", Highlight: "Use Redis for rate limiting", Why: "Already in stack"},
			{Seq: 3, Type: "question", Highlight: "What about edge cases?", Why: "Needs investigation"},
		},
	}
}

func TestParseSessionName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantDate string
		wantWho  string
	}{
		{"standard", "2026-01-06T14-32-ryan-Ox7f3a", "2026-01-06", "ryan"},
		{"hyphenated username", "2026-03-10T09-15-alice-smith-OxABCD", "2026-03-10", "alice-smith"},
		{"email-like username", "2026-01-28T18-56-ajit-sageox-ai-OxKMZN", "2026-01-28", "ajit-sageox-ai"},
		{"too short", "2026-01", "", ""},
		{"no match", "not-a-session", "", ""},
		{"missing session ID", "2026-01-06T14-32-ryan", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			date, who := parseSessionName(tt.input)
			if date != tt.wantDate {
				t.Errorf("date = %q, want %q", date, tt.wantDate)
			}
			if who != tt.wantWho {
				t.Errorf("who = %q, want %q", who, tt.wantWho)
			}
		})
	}
}

func TestScanPendingSessions(t *testing.T) {
	setup := func(t *testing.T, withFactFiles bool) (ledgerPath, tcPath string) {
		t.Helper()
		ledgerPath = t.TempDir()
		tcPath = t.TempDir()
		sessionsDir := filepath.Join(ledgerPath, "sessions")

		createSessionDir(t, sessionsDir, "2026-03-10T14-23-ryan-Ox7f3a", testSummary("Session A", 0.8))
		createSessionDir(t, sessionsDir, "2026-03-11T09-00-alice-OxABCD", testSummary("Session B", 0.5))

		if withFactFiles {
			for _, date := range []string{"2026-03-10", "2026-03-11"} {
				os.MkdirAll(filepath.Join(tcPath, "memory", ".session-facts", date), 0o755)
			}
			os.WriteFile(
				filepath.Join(tcPath, "memory", ".session-facts", "2026-03-10", "2026-03-10T14-23-ryan-Ox7f3a.jsonl"),
				[]byte(`{"_meta":{"schema_version":"2"}}`), 0o644)
			os.WriteFile(
				filepath.Join(tcPath, "memory", ".session-facts", "2026-03-11", "2026-03-11T09-00-alice-OxABCD.jsonl"),
				[]byte(`{"_meta":{"schema_version":"2"}}`), 0o644)
		}
		return ledgerPath, tcPath
	}

	t.Run("no processed — finds all", func(t *testing.T) {
		ledgerPath, tcPath := setup(t, false)
		pending, err := scanPendingSessions(ledgerPath, tcPath, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 2 {
			t.Errorf("got %d pending, want 2", len(pending))
		}
	})

	t.Run("one processed with fact file — finds remaining", func(t *testing.T) {
		ledgerPath, tcPath := setup(t, true)
		processed := map[string]string{
			"2026-03-10T14-23-ryan-Ox7f3a": sessionContentHash(ledgerPath, "2026-03-10T14-23-ryan-Ox7f3a"),
		}
		pending, err := scanPendingSessions(ledgerPath, tcPath, processed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// alice is not in processed map but her fact file exists → skipped
		if len(pending) != 0 {
			t.Errorf("got %d pending, want 0 (both have fact files)", len(pending))
		}
	})

	t.Run("all processed with fact files — finds none", func(t *testing.T) {
		ledgerPath, tcPath := setup(t, true)
		processed := map[string]string{
			"2026-03-10T14-23-ryan-Ox7f3a": sessionContentHash(ledgerPath, "2026-03-10T14-23-ryan-Ox7f3a"),
			"2026-03-11T09-00-alice-OxABCD": sessionContentHash(ledgerPath, "2026-03-11T09-00-alice-OxABCD"),
		}
		pending, err := scanPendingSessions(ledgerPath, tcPath, processed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("got %d pending, want 0", len(pending))
		}
	})

	t.Run("stale hash + no fact file — re-processes", func(t *testing.T) {
		ledgerPath, tcPath := setup(t, false)
		processed := map[string]string{
			"2026-03-10T14-23-ryan-Ox7f3a": "stale-hash",
		}
		pending, err := scanPendingSessions(ledgerPath, tcPath, processed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 2 {
			t.Errorf("got %d pending, want 2", len(pending))
		}
	})

	t.Run("stale hash + fact file exists — re-processes changed session", func(t *testing.T) {
		ledgerPath, tcPath := setup(t, true)
		processed := map[string]string{
			"2026-03-10T14-23-ryan-Ox7f3a": "stale-hash",
			"2026-03-11T09-00-alice-OxABCD": sessionContentHash(ledgerPath, "2026-03-11T09-00-alice-OxABCD"),
		}
		pending, err := scanPendingSessions(ledgerPath, tcPath, processed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// ryan: stale hash → re-process despite fact file existing
		// alice: matching hash + fact file → skip
		if len(pending) != 1 {
			t.Fatalf("got %d pending, want 1", len(pending))
		}
		if pending[0].DirName != "2026-03-10T14-23-ryan-Ox7f3a" {
			t.Errorf("expected ryan's session to be re-processed, got %s", pending[0].DirName)
		}
	})

	t.Run("malformed summary.json — skipped", func(t *testing.T) {
		ledgerPath := t.TempDir()
		tcPath := t.TempDir()
		dir := filepath.Join(ledgerPath, "sessions", "2026-03-10T14-23-ryan-Ox7f3a")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{invalid json`), 0o644); err != nil {
			t.Fatal(err)
		}

		pending, err := scanPendingSessions(ledgerPath, tcPath, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("got %d pending, want 0 (malformed JSON)", len(pending))
		}
	})

	t.Run("low quality score — skipped", func(t *testing.T) {
		ledgerPath := t.TempDir()
		tcPath := t.TempDir()
		sessionsDir := filepath.Join(ledgerPath, "sessions")
		createSessionDir(t, sessionsDir, "2026-03-10T14-23-ryan-Ox7f3a", testSummary("Low quality", 0.1))

		pending, err := scanPendingSessions(ledgerPath, tcPath, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("got %d pending, want 0 (below quality threshold)", len(pending))
		}
	})

	t.Run("zero quality score — included (backward compat)", func(t *testing.T) {
		ledgerPath := t.TempDir()
		tcPath := t.TempDir()
		sessionsDir := filepath.Join(ledgerPath, "sessions")
		createSessionDir(t, sessionsDir, "2026-03-10T14-23-ryan-Ox7f3a", testSummary("No score", 0))

		pending, err := scanPendingSessions(ledgerPath, tcPath, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 1 {
			t.Errorf("got %d pending, want 1 (zero score = include)", len(pending))
		}
	})

	t.Run("no summary.json — skipped", func(t *testing.T) {
		ledgerPath := t.TempDir()
		tcPath := t.TempDir()
		// create session dir without summary.json
		os.MkdirAll(filepath.Join(ledgerPath, "sessions", "2026-03-10T14-23-ryan-Ox7f3a"), 0o755)

		pending, err := scanPendingSessions(ledgerPath, tcPath, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("got %d pending, want 0 (no summary.json)", len(pending))
		}
	})

	t.Run("nonexistent sessions dir — returns nil", func(t *testing.T) {
		ledgerPath := t.TempDir()
		tcPath := t.TempDir()
		pending, err := scanPendingSessions(ledgerPath, tcPath, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pending != nil {
			t.Errorf("got %v, want nil", pending)
		}
	})
}

func TestSessionSummaryToFacts(t *testing.T) {
	t.Run("full summary with all fields", func(t *testing.T) {
		input := sessionInput{
			DirName: "2026-01-06T14-32-ryan-Ox7f3a",
			Date:    "2026-01-06",
			Who:     "ryan",
			Summary: testSummary("Database Migration", 0.8),
		}

		result := sessionSummaryToFacts(input)

		// Expected: 1 context + 1 decision + 1 action_item + 1 open_question
		//         + 1 learning (insight) + 1 decision (aha decision)
		//         = 6 facts (question-type aha moment skipped)
		if len(result) != 6 {
			t.Fatalf("got %d facts, want 6", len(result))
		}

		// verify all facts have common fields
		for i, f := range result {
			if f.SourceType != facts.SourceSession {
				t.Errorf("fact[%d] source_type = %q, want %q", i, f.SourceType, facts.SourceSession)
			}
			if f.SourceRef != "sessions/2026-01-06T14-32-ryan-Ox7f3a" {
				t.Errorf("fact[%d] source_ref = %q", i, f.SourceRef)
			}
			if f.Timestamp != "2026-01-06T00:00:00Z" {
				t.Errorf("fact[%d] timestamp = %q", i, f.Timestamp)
			}
		}

		// verify context fact with key actions appended
		if result[0].Category != facts.CategoryContext {
			t.Errorf("fact[0] category = %q, want context", result[0].Category)
		}
		if result[0].Headline != "Database Migration" {
			t.Errorf("fact[0] headline = %q", result[0].Headline)
		}
		if !strings.Contains(result[0].Summary, "Key actions: did something") {
			t.Errorf("fact[0] summary should contain key actions, got %q", result[0].Summary)
		}

		// verify decision from AgentSummary
		if result[1].Category != facts.CategoryDecision {
			t.Errorf("fact[1] category = %q, want decision", result[1].Category)
		}
		if result[1].Headline != "Use PostgreSQL" {
			t.Errorf("fact[1] headline = %q", result[1].Headline)
		}
		if result[1].Who != "alice" {
			t.Errorf("fact[1] who = %q, want alice (owner)", result[1].Who)
		}

		// verify action item with priority
		if result[2].Category != facts.CategoryActionItem {
			t.Errorf("fact[2] category = %q, want action_item", result[2].Category)
		}
		if result[2].Who != "bob" {
			t.Errorf("fact[2] who = %q, want bob (assignee)", result[2].Who)
		}
		if result[2].Summary != "Priority: high" {
			t.Errorf("fact[2] summary = %q, want %q", result[2].Summary, "Priority: high")
		}

		// verify open question
		if result[3].Category != facts.CategoryOpenQuestion {
			t.Errorf("fact[3] category = %q, want open_question", result[3].Category)
		}

		// verify insight aha → learning
		if result[4].Category != facts.CategoryLearning {
			t.Errorf("fact[4] category = %q, want learning", result[4].Category)
		}

		// verify decision aha → decision
		if result[5].Category != facts.CategoryDecision {
			t.Errorf("fact[5] category = %q, want decision", result[5].Category)
		}
	})

	t.Run("nil agent summary — only aha + context", func(t *testing.T) {
		input := sessionInput{
			DirName: "2026-01-06T14-32-ryan-Ox7f3a",
			Date:    "2026-01-06",
			Who:     "ryan",
			Summary: &sessionsummary.SummarizeResponse{
				Title:   "Quick Fix",
				Summary: "Fixed a bug",
				AhaMoments: []sessionsummary.AhaMoment{
					{Seq: 1, Type: "insight", Highlight: "Root cause was race condition", Why: "Goroutine leak"},
				},
			},
		}

		result := sessionSummaryToFacts(input)
		if len(result) != 2 { // 1 context + 1 learning
			t.Fatalf("got %d facts, want 2", len(result))
		}
	})

	t.Run("empty summary — no facts", func(t *testing.T) {
		input := sessionInput{
			DirName: "2026-01-06T14-32-ryan-Ox7f3a",
			Date:    "2026-01-06",
			Who:     "ryan",
			Summary: &sessionsummary.SummarizeResponse{},
		}

		result := sessionSummaryToFacts(input)
		if len(result) != 0 {
			t.Errorf("got %d facts, want 0 for empty summary", len(result))
		}
	})

	t.Run("question-only aha moments — no aha facts emitted", func(t *testing.T) {
		input := sessionInput{
			DirName: "2026-01-06T14-32-ryan-Ox7f3a",
			Date:    "2026-01-06",
			Who:     "ryan",
			Summary: &sessionsummary.SummarizeResponse{
				Title:   "Exploration",
				Summary: "Explored options",
				AhaMoments: []sessionsummary.AhaMoment{
					{Seq: 1, Type: "question", Highlight: "What about X?", Why: "Unclear"},
					{Seq: 2, Type: "question", Highlight: "What about Y?", Why: "Also unclear"},
				},
			},
		}

		result := sessionSummaryToFacts(input)
		// 1 context fact only; question-type aha moments skipped
		if len(result) != 1 {
			t.Fatalf("got %d facts, want 1 (context only)", len(result))
		}
		if result[0].Category != facts.CategoryContext {
			t.Errorf("fact[0] category = %q, want context", result[0].Category)
		}
	})

	t.Run("action item without priority — empty summary", func(t *testing.T) {
		input := sessionInput{
			DirName: "2026-01-06T14-32-ryan-Ox7f3a",
			Date:    "2026-01-06",
			Who:     "ryan",
			Summary: &sessionsummary.SummarizeResponse{
				Title:   "Quick task",
				Summary: "Did a thing",
				AgentSummary: &sessionsummary.AgentSummary{
					ActionItems: []sessionsummary.ActionItem{
						{Task: "Do something", Assignee: "alice"},
					},
				},
			},
		}

		result := sessionSummaryToFacts(input)
		// context + action_item = 2
		if len(result) != 2 {
			t.Fatalf("got %d facts, want 2", len(result))
		}
		if result[1].Summary != "" {
			t.Errorf("action item summary = %q, want empty (no priority)", result[1].Summary)
		}
	})

	t.Run("decision owner falls back to session user", func(t *testing.T) {
		input := sessionInput{
			DirName: "2026-01-06T14-32-ryan-Ox7f3a",
			Date:    "2026-01-06",
			Who:     "ryan",
			Summary: &sessionsummary.SummarizeResponse{
				Title:   "Design Review",
				Summary: "Reviewed architecture",
				AgentSummary: &sessionsummary.AgentSummary{
					Decisions: []sessionsummary.Decision{
						{What: "Use microservices", Why: "Scale independently"},
					},
				},
			},
		}

		result := sessionSummaryToFacts(input)
		// context + decision = 2 facts
		if len(result) != 2 {
			t.Fatalf("got %d facts, want 2", len(result))
		}
		// decision should fall back to session user
		if result[1].Who != "ryan" {
			t.Errorf("decision who = %q, want ryan (fallback)", result[1].Who)
		}
	})
}

func TestReadPendingSessionFacts(t *testing.T) {
	t.Run("reads facts grouped by date", func(t *testing.T) {
		tcPath := t.TempDir()

		// create session fact files
		for _, item := range []struct {
			date, name, content string
		}{
			{"2026-03-10", "2026-03-10T14-23-ryan-Ox7f3a.jsonl",
				`{"_meta":{"schema_version":"2","source_type":"session","recorded_at":"2026-03-10T00:00:00Z"}}
{"headline":"fact1","source_type":"session","timestamp":"2026-03-10T00:00:00Z"}`},
			{"2026-03-11", "2026-03-11T09-00-alice-OxABCD.jsonl",
				`{"_meta":{"schema_version":"2","source_type":"session","recorded_at":"2026-03-11T00:00:00Z"}}
{"headline":"fact2","source_type":"session","timestamp":"2026-03-11T00:00:00Z"}`},
		} {
			dir := filepath.Join(tcPath, "memory", ".session-facts", item.date)
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, item.name), []byte(item.content), 0o644)
		}

		result, err := readPendingSessionFacts(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("got %d date groups, want 2", len(result))
		}
		if len(result["2026-03-10"]) != 1 {
			t.Errorf("2026-03-10 has %d entries, want 1", len(result["2026-03-10"]))
		}
		if len(result["2026-03-11"]) != 1 {
			t.Errorf("2026-03-11 has %d entries, want 1", len(result["2026-03-11"]))
		}
	})

	t.Run("filters by file mtime not directory date", func(t *testing.T) {
		tcPath := t.TempDir()

		// create two files: one old (backdate mtime), one new (current mtime)
		for _, item := range []struct {
			date, name string
		}{
			{"2026-03-09", "2026-03-09T14-23-ryan-Ox7f3a.jsonl"},
			{"2026-03-11", "2026-03-11T09-00-alice-OxABCD.jsonl"},
		} {
			dir := filepath.Join(tcPath, "memory", ".session-facts", item.date)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, item.name), []byte(`{"_meta":{"schema_version":"2","source_type":"session","recorded_at":"`+item.date+`T00:00:00Z"}}
{"headline":"fact","source_type":"session"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		// backdate the old file's mtime to before since
		oldFile := filepath.Join(tcPath, "memory", ".session-facts", "2026-03-09", "2026-03-09T14-23-ryan-Ox7f3a.jsonl")
		oldTime := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
		os.Chtimes(oldFile, oldTime, oldTime)

		// since = 1 second ago — new file (current mtime) is after since, old file is before
		since := time.Now().Add(-time.Second)
		result, err := readPendingSessionFacts(tcPath, since)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Errorf("got %d date groups, want 1 (only fresh file)", len(result))
		}
		if _, ok := result["2026-03-11"]; !ok {
			t.Error("expected 2026-03-11 in results (fresh mtime)")
		}
	})

	t.Run("late-arriving session included despite old date", func(t *testing.T) {
		tcPath := t.TempDir()

		// simulate a March 10 session extracted today (fresh mtime)
		dir := filepath.Join(tcPath, "memory", ".session-facts", "2026-03-10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "2026-03-10T14-23-ryan-Ox7f3a.jsonl"),
			[]byte(`{"_meta":{"schema_version":"2","source_type":"session","recorded_at":"2026-03-10T00:00:00Z"}}
{"headline":"late fact","source_type":"session"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// since = March 25 — file mtime is now (fresh), so it should be included
		since, _ := time.Parse("2006-01-02", "2026-03-25")
		result, err := readPendingSessionFacts(tcPath, since)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result["2026-03-10"]) != 1 {
			t.Errorf("late-arriving session should be included, got %d entries", len(result["2026-03-10"]))
		}
	})

	t.Run("multiple files in single date dir", func(t *testing.T) {
		tcPath := t.TempDir()
		dir := filepath.Join(tcPath, "memory", ".session-facts", "2026-03-10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{
			"2026-03-10T09-00-ryan-Ox1111.jsonl",
			"2026-03-10T14-00-alice-Ox2222.jsonl",
		} {
			content := `{"_meta":{"schema_version":"2","source_type":"session","recorded_at":"2026-03-10T00:00:00Z"}}
{"headline":"fact","source_type":"session"}`
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		result, err := readPendingSessionFacts(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result["2026-03-10"]) != 2 {
			t.Errorf("got %d entries for 2026-03-10, want 2", len(result["2026-03-10"]))
		}
	})

	t.Run("zero since — all files included", func(t *testing.T) {
		tcPath := t.TempDir()
		dir := filepath.Join(tcPath, "memory", ".session-facts", "2026-03-10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(`{"_meta":{"schema_version":"2"}}
{"headline":"fact"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		result, err := readPendingSessionFacts(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result["2026-03-10"]) != 1 {
			t.Errorf("zero since should include all, got %d entries", len(result["2026-03-10"]))
		}
	})

	t.Run("skips non-jsonl files and subdirectories", func(t *testing.T) {
		tcPath := t.TempDir()
		dir := filepath.Join(tcPath, "memory", ".session-facts", "2026-03-10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// valid file
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(`{"_meta":{"schema_version":"2"}}
{"headline":"fact"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		// non-jsonl file (should be skipped)
		if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
		// subdirectory (should be skipped)
		if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
			t.Fatal(err)
		}

		result, err := readPendingSessionFacts(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result["2026-03-10"]) != 1 {
			t.Errorf("got %d entries, want 1 (only .jsonl files)", len(result["2026-03-10"]))
		}
	})

	t.Run("nonexistent dir — returns nil", func(t *testing.T) {
		tcPath := t.TempDir()
		result, err := readPendingSessionFacts(tcPath, time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("got %v, want nil", result)
		}
	})
}

func TestSessionContentHash(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions", "test-session")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "summary.json"), []byte(`{"title":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	hash1 := sessionContentHash(ledgerPath, "test-session")
	hash2 := sessionContentHash(ledgerPath, "test-session")

	if hash1 == "" {
		t.Error("hash should not be empty")
	}
	if hash1 != hash2 {
		t.Errorf("hash not deterministic: %q != %q", hash1, hash2)
	}

	// different content should produce different hash
	if err := os.WriteFile(filepath.Join(sessionsDir, "summary.json"), []byte(`{"title":"changed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hash3 := sessionContentHash(ledgerPath, "test-session")
	if hash1 == hash3 {
		t.Error("different content should produce different hash")
	}

	// missing summary.json returns empty string
	hash4 := sessionContentHash(ledgerPath, "nonexistent-session")
	if hash4 != "" {
		t.Errorf("missing file should return empty hash, got %q", hash4)
	}
}

func TestScanPendingSessionsPopulatesHash(t *testing.T) {
	ledgerPath := t.TempDir()
	tcPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	createSessionDir(t, sessionsDir, "2026-03-10T14-23-ryan-Ox7f3a", testSummary("Test", 0.8))

	pending, err := scanPendingSessions(ledgerPath, tcPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want 1", len(pending))
	}
	if pending[0].Hash == "" {
		t.Error("Hash should be populated by scanPendingSessions")
	}
	// verify it matches sessionContentHash
	expected := sessionContentHash(ledgerPath, "2026-03-10T14-23-ryan-Ox7f3a")
	if pending[0].Hash != expected {
		t.Errorf("Hash = %q, want %q", pending[0].Hash, expected)
	}
}

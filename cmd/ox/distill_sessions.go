package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/facts"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
)

// sessionNameRe parses session directory names.
// Format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
// Captures: date (group 1), username (group 2).
var sessionNameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T\d{2}-\d{2}-(.+)-\w+$`)

// minSessionQuality is the minimum quality score for a session to be included
// in fact extraction. Sessions scoring below this are considered too low-signal.
// A zero score means the LLM didn't provide one (backward compat) — include those.
const minSessionQuality = 0.2

// sessionInput holds parsed data for a single session directory.
type sessionInput struct {
	DirName   string                            // e.g., "2026-01-06T14-32-ryan-Ox7f3a"
	Date      string                            // "2026-01-06" (extracted from dir name)
	Who       string                            // username extracted from dir name
	Hash      string                            // content hash from scan, avoids re-reading summary.json
	StartedAt time.Time                         // from raw.jsonl _meta.started_at; zero if unavailable
	Summary   *sessionsummary.SummarizeResponse // parsed summary.json
}

// extractSessionFacts scans the given ledger for sessions with summary.json,
// transforms structured session data into facts, and writes JSONL files
// to memory/.session-facts/<date>/ in the team context.
//
// repoID identifies the repo for per-repo state tracking.
// ledgerPath is the path to the ledger directory containing sessions/.
// ep is the endpoint used to build per-fact SourceURL for clickable citations
// in distilled summaries (gh #476). Empty endpoint degrades to label-only.
//
// No LLM calls — pure data transformation from structured summary.json.
func extractSessionFacts(cmd *cobra.Command, tc *config.TeamContext, repoID, ledgerPath, ep string, since time.Time) error {
	if ledgerPath == "" {
		slog.Debug("no ledger path, skipping session fact extraction", "repo", repoID)
		return nil
	}
	if _, err := os.Stat(filepath.Join(ledgerPath, "sessions")); err != nil {
		slog.Debug("no sessions directory in ledger, skipping", "repo", repoID, "path", ledgerPath)
		return nil
	}

	pending, err := scanPendingSessions(ledgerPath, tc.Path, since)
	if err != nil {
		return fmt.Errorf("scan sessions: %w", err)
	}

	if len(pending) == 0 {
		return nil
	}

	if distillDryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Pending sessions: %d\n", len(pending))
		for _, s := range pending {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", s.DirName, s.Summary.Title)
		}
		return nil
	}

	for _, s := range pending {
		// Use actual UTC StartedAt when available; fall back to date-only string
		// so parseFactDate uses the directory date as-is (no false UTC instant).
		recordedAt := s.Date
		if !s.StartedAt.IsZero() {
			recordedAt = s.StartedAt.UTC().Format(time.RFC3339)
		}

		extractedFacts := sessionSummaryToFacts(s, repoID, ep)
		if len(extractedFacts) == 0 {
			slog.Debug("no facts extracted from session", "session", s.DirName)
			// write empty marker so scanPendingSessions skips this session
			markerUID, _ := uuid.NewV7() // error only if crypto/rand fails — zero UUID collision is acceptable
			markerFile := filepath.Join("memory", ".session-facts", s.Date, s.DirName+"-"+markerUID.String()+".jsonl")
			markerHeader := facts.FileHeader{
				Meta: facts.FileMeta{
					SchemaVersion: facts.SchemaVersion,
					SourceType:    facts.SourceSession,
					RecordedAt:    recordedAt,
					SourceHash:    s.Hash,
				},
			}
			if err := facts.WriteFacts(filepath.Join(tc.Path, markerFile), markerHeader, nil); err != nil {
				slog.Warn("failed to write empty session fact marker", "session", s.DirName, "error", err)
				continue
			}
			if err := commitMemoryFile(tc.Path, markerFile, fmt.Sprintf("memory: mark session %s (no facts)", s.DirName)); err != nil {
				slog.Warn("failed to commit session fact marker", "session", s.DirName, "error", err)
				if removeErr := os.Remove(filepath.Join(tc.Path, markerFile)); removeErr != nil && !os.IsNotExist(removeErr) {
					slog.Warn("failed to clean up uncommitted session fact marker", "session", s.DirName, "error", removeErr)
				}
			}
			continue
		}

		header := facts.FileHeader{
			Meta: facts.FileMeta{
				SchemaVersion: facts.SchemaVersion,
				SourceType:    facts.SourceSession,
				RecordedAt:    recordedAt,
				SourceHash:    s.Hash,
			},
		}

		uid, _ := uuid.NewV7() // error only if crypto/rand fails — zero UUID collision is acceptable
		factFile := filepath.Join("memory", ".session-facts", s.Date, s.DirName+"-"+uid.String()+".jsonl")
		fullPath := filepath.Join(tc.Path, factFile)

		if err := facts.WriteFacts(fullPath, header, extractedFacts); err != nil {
			slog.Warn("failed to write session facts", "session", s.DirName, "error", err)
			continue
		}

		if err := commitMemoryFile(tc.Path, factFile, fmt.Sprintf("memory: extract facts from session %s", s.DirName)); err != nil {
			slog.Warn("failed to commit session facts", "session", s.DirName, "error", err)
			if removeErr := os.Remove(fullPath); removeErr != nil && !os.IsNotExist(removeErr) {
				slog.Warn("failed to clean up uncommitted session facts", "session", s.DirName, "error", removeErr)
			}
			continue
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Extracted %d facts from session: %s\n", len(extractedFacts), s.Summary.Title)
	}

	return nil
}

// readSessionStartedAt reads the first line of raw.jsonl in a session directory
// and parses the _meta.started_at field as an RFC3339 timestamp.
// Returns zero time if raw.jsonl is missing or _meta is unparseable.
func readSessionStartedAt(sessionDir string) time.Time {
	f, err := os.Open(filepath.Join(sessionDir, "raw.jsonl"))
	if err != nil {
		return time.Time{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return time.Time{}
	}

	var meta struct {
		Meta struct {
			StartedAt string `json:"started_at"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil || meta.Meta.StartedAt == "" {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339, meta.Meta.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// scanPendingSessions reads the sessions/ directory in the ledger and returns
// sessions that need fact extraction. A session is skipped if its fact file
// already exists and the embedded source_hash matches the current summary hash.
// Legacy fact files without source_hash are re-extracted.
func scanPendingSessions(ledgerPath, tcPath string, since time.Time) ([]sessionInput, error) {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var pending []sessionInput
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()

		// parse date and username from session name
		date, who := parseSessionName(dirName)
		if date == "" {
			slog.Debug("skip session dir with unparseable name", "dir", dirName)
			continue
		}

		// check if summary.json exists
		summaryPath := filepath.Join(sessionsDir, dirName, "summary.json")
		summaryData, err := os.ReadFile(summaryPath)
		if err != nil {
			continue // no summary.json, skip
		}

		// derive the final date before dedup check — started_at may differ from dir name
		startedAt := readSessionStartedAt(filepath.Join(sessionsDir, dirName))
		sessionDate := date
		if !startedAt.IsZero() {
			sessionDate = startedAt.UTC().Format("2006-01-02")
		}

		// skip sessions older than the lookback window
		// truncate since to start-of-day so date-only comparison is fair
		if !since.IsZero() {
			sinceDate := since.Truncate(24 * time.Hour)
			sessionTime, parseErr := time.Parse("2006-01-02", sessionDate)
			if parseErr == nil && sessionTime.Before(sinceDate) {
				continue
			}
		}

		// compute content hash for change detection
		currentHash := contentHash(string(summaryData))

		// check if fact file already exists with matching source_hash (UUID7 glob + legacy fallback)
		sessionFactsDir := filepath.Join(tcPath, "memory", ".session-facts", sessionDate)
		existingHash := findLatestFactFileSourceHash(sessionFactsDir, dirName+"-*.jsonl")
		if existingHash == "" {
			existingHash = readFactFileSourceHash(filepath.Join(sessionFactsDir, dirName+".jsonl"))
		}
		if existingHash == currentHash {
			continue // fact file is up to date
		}

		// parse summary.json
		var summary sessionsummary.SummarizeResponse
		if err := json.Unmarshal(summaryData, &summary); err != nil {
			slog.Debug("malformed summary.json, skipping", "session", dirName, "error", err)
			continue
		}

		// quality gate: skip low-quality sessions
		if summary.QualityScore > 0 && summary.QualityScore < minSessionQuality {
			slog.Debug("skip low-quality session", "session", dirName, "score", summary.QualityScore)
			continue
		}

		pending = append(pending, sessionInput{
			DirName:   dirName,
			Date:      sessionDate,
			Who:       who,
			Hash:      currentHash,
			StartedAt: startedAt,
			Summary:   &summary,
		})
	}

	// sort by date (oldest first)
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].DirName < pending[j].DirName
	})

	return pending, nil
}

// parseSessionName extracts the date and username from a session directory name.
// Format: YYYY-MM-DDTHH-MM-<username>-<sessionID>
// Returns empty strings if the name doesn't match the expected format.
func parseSessionName(name string) (date, who string) {
	m := sessionNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// sessionContentHash computes a content hash of a session's summary.json.
func sessionContentHash(ledgerPath, dirName string) string {
	data, err := os.ReadFile(filepath.Join(ledgerPath, "sessions", dirName, "summary.json"))
	if err != nil {
		slog.Debug("failed to hash session summary", "session", dirName, "error", err)
		return ""
	}
	return contentHash(string(data))
}

// sessionSummaryToFacts transforms a session summary into uniform facts.
// Pure data transformation — no LLM calls.
//
// repoID + ep populate per-fact SourceURL for clickable citations in distilled
// summaries (gh #476). When either is empty, SourceURL is left empty and the
// citation pipeline degrades to a label-only entry. We deliberately do NOT
// fall through to buildSessionURL with an empty cfg, because that helper's
// cfg.GetEndpoint() would resolve to the SAGEOX_ENDPOINT env var or the
// default — silently producing a wrong URL in tests / unconfigured runs.
func sessionSummaryToFacts(input sessionInput, repoID, ep string) []facts.Fact {
	var result []facts.Fact
	s := input.Summary
	ts := input.Date
	if !input.StartedAt.IsZero() {
		ts = input.StartedAt.UTC().Format(time.RFC3339)
	}
	ref := "sessions/" + input.DirName
	var sourceURL string
	if repoID != "" && ep != "" {
		sourceURL = buildSessionURL(&config.ProjectConfig{RepoID: repoID, Endpoint: ep}, input.DirName)
	}
	sourceTitle := s.Title // already loaded; falls back to dirname-derived label at citation time

	mkFact := func(headline, summary, rationale, who, category string) facts.Fact {
		return facts.Fact{
			Headline:    headline,
			Summary:     summary,
			Rationale:   rationale,
			SourceType:  facts.SourceSession,
			SourceRef:   ref,
			SourceURL:   sourceURL,
			SourceTitle: sourceTitle,
			Timestamp:   ts,
			Category:    category,
			Who:         who,
		}
	}

	// session context fact (title + summary)
	if s.Title != "" && s.Summary != "" {
		summary := s.Summary
		if len(s.KeyActions) > 0 {
			summary += " Key actions: " + strings.Join(s.KeyActions, "; ")
		}
		result = append(result, mkFact(s.Title, summary, "", input.Who, facts.CategoryContext))
	}

	if s.AgentSummary != nil {
		// decisions
		for _, d := range s.AgentSummary.Decisions {
			who := d.Owner
			if who == "" {
				who = input.Who
			}
			result = append(result, mkFact(d.What, "", d.Why, who, facts.CategoryDecision))
		}

		// action items
		for _, a := range s.AgentSummary.ActionItems {
			who := a.Assignee
			if who == "" {
				who = input.Who
			}
			var summary string
			if a.Priority != "" {
				summary = fmt.Sprintf("Priority: %s", a.Priority)
			}
			result = append(result, mkFact(a.Task, summary, "", who, facts.CategoryActionItem))
		}

		// open questions
		for _, q := range s.AgentSummary.OpenQuestions {
			result = append(result, mkFact(q.Question, q.Context, "", input.Who, facts.CategoryOpenQuestion))
		}
	}

	// aha moments — skip "question" type (overlaps with OpenQuestions)
	for _, aha := range s.AhaMoments {
		switch aha.Type {
		case "insight", "breakthrough", "synthesis":
			result = append(result, mkFact(aha.Highlight, aha.Why, "", input.Who, facts.CategoryLearning))
		case "decision":
			result = append(result, mkFact(aha.Highlight, aha.Why, "", input.Who, facts.CategoryDecision))
		}
	}

	return result
}

// readPendingSessionFacts reads fact files from memory/.session-facts/<date>/
// within the lookback window, grouped by session date.
//
// The parent directory name is the authoritative session date.
// extractSessionFacts writes each fact under .session-facts/<s.Date>/, where
// s.Date comes from the session's parsed StartedAt. Directory-level filtering
// lets us skip whole old directories without opening any file inside them.
//
// We deliberately do NOT filter by file mtime. mtime is not clone-stable:
// git clone resets every file's mtime to checkout time, so on a fresh clone
// (blue/green GC, new machine) an mtime filter would let every historical
// fact through regardless of the session's actual date. Path-based filtering
// is identical across clones, and the alreadyDistilled check in distillDaily
// (via idx.distilledSources) handles dedup of facts already consumed by
// existing daily summaries.
func readPendingSessionFacts(tcPath string, since time.Time) (map[string][]discussionFactEntry, error) {
	sessionFactsDir := filepath.Join(tcPath, "memory", ".session-facts")
	dateDirs, err := os.ReadDir(sessionFactsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session-facts dir: %w", err)
	}

	var cutoffDate string
	if !since.IsZero() {
		cutoffDate = since.UTC().Format("2006-01-02")
	}

	result := make(map[string][]discussionFactEntry)

	for _, dateDir := range dateDirs {
		if !dateDir.IsDir() {
			continue
		}

		date := dateDir.Name()
		if _, err := time.Parse("2006-01-02", date); err != nil {
			continue
		}

		// path-based filter: skip the whole directory if it's before the cutoff
		if cutoffDate != "" && date < cutoffDate {
			continue
		}

		datePath := filepath.Join(sessionFactsDir, date)
		files, err := os.ReadDir(datePath)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}

			data, err := os.ReadFile(filepath.Join(datePath, f.Name()))
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}

			result[date] = append(result[date], discussionFactEntry{
				Content: content,
				RelPath: filepath.Join("memory", ".session-facts", date, f.Name()),
				Date:    date,
			})
		}
	}

	return result, nil
}

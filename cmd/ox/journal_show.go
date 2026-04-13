package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sageox/ox/internal/journal/read"
	"github.com/spf13/cobra"
)

// journalShowFlags captures the user-controllable inputs to
// `ox journal show`. Positional IDs come in via the cobra args slice
// (not a flag), so they are NOT represented here. The field set is
// deliberately narrower than `list` — show does not take --since,
// --until, --tz, --layer, --all-teams, or --limit. Its scope is a set
// of explicit IDs against one team.
//
// There is no --latest flag. Cobra rejects unknown flags at parse
// time, so a user passing --latest gets a usage error from cobra
// itself before runJournalShow sees the command.
type journalShowFlags struct {
	Team   string
	Format string
}

var journalShowFlagSet journalShowFlags

func init() {
	journalShowCmd.RunE = runJournalShow
	f := journalShowCmd.Flags()
	f.StringVar(&journalShowFlagSet.Team, "team", "", "team slug, id, or name (defaults to the repo's active team)")
	f.StringVar(&journalShowFlagSet.Format, "format", "json", "output format: json|text|content")
}

// runJournalShow is the RunE for `ox journal show`. It validates
// flags, resolves the active team, walks the daily layer via
// read.LoadEntries, and emits the partial-success envelope per plan
// §3 Unit 4. The show command is daily-only for Unit 4 — weekly and
// monthly show are out of scope and not wired up.
//
// Per-ID failures DO NOT abort the call: a mix of ok / not_found /
// ambiguous entries is a success envelope (exit 0) with per-entry
// Status and Error fields populated. Only when every requested ID
// failed does the command emit a success:false envelope and return
// exit 1 via journalExitError.
func runJournalShow(cmd *cobra.Command, args []string) error {
	flags := journalShowFlagSet
	start := time.Now()
	elapsed := func() int64 { return time.Since(start).Milliseconds() }

	if len(args) == 0 {
		return emitJournalShowUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError("at least one <id> is required"), elapsed())
	}

	if flags.Format != "json" && flags.Format != "text" && flags.Format != "content" {
		return emitJournalShowUsageError(cmd.OutOrStdout(), flags.Format,
			newJournalUsageError(
				fmt.Sprintf("--format must be json|text|content, got %q", flags.Format)),
			elapsed())
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			journalShowErrorFormat(flags.Format), "project_not_found", err.Error(), elapsed())
	}

	teams, err := resolveJournalTeams(projectRoot, flags.Team, false)
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			journalShowErrorFormat(flags.Format), "team_not_found", err.Error(), elapsed())
	}

	// show has no --since/--until; it reads the whole journal regardless
	// of date. NoTimeFilter short-circuits the window filter in listEntries
	// so the index scan returns every daily file under Teams. See
	// ReadQuery.NoTimeFilter doc.
	q := read.ReadQuery{
		NoTimeFilter: true,
		Layer:        read.LayerDaily,
		Teams:        teams,
		WantBody:     true,
	}
	entries, err := read.LoadEntries(context.Background(), q, args)
	if err != nil {
		return writeJournalRuntimeError(cmd.OutOrStdout(),
			journalShowErrorFormat(flags.Format), "read_failed", err.Error(), elapsed())
	}

	allFailed := true
	var firstErr *read.EntryError
	for i := range entries {
		if entries[i].Status == "ok" {
			allFailed = false
		}
		if firstErr == nil && entries[i].Error != nil {
			e := *entries[i].Error
			firstErr = &e
		}
	}
	if allFailed && firstErr != nil {
		env := &journalEnvelope{
			Success:   false,
			ElapsedMS: elapsed(),
			Error: &journalEnvelopeError{
				Code:      firstErr.Code,
				Message:   firstErr.Message,
				Retryable: false,
			},
		}
		writeJournalEnvelope(cmd.OutOrStdout(), journalShowErrorFormat(flags.Format), env)
		return &journalExitError{ExitCode: 1, Envelope: *env}
	}

	if flags.Format == "content" {
		renderJournalShowContent(cmd.OutOrStdout(), entries)
		return nil
	}

	env := buildJournalShowEnvelope(entries, elapsed())
	if flags.Format == "text" {
		renderJournalShowText(cmd.OutOrStdout(), env)
		return nil
	}
	writeJournalEnvelope(cmd.OutOrStdout(), flags.Format, env)
	return nil
}

// emitJournalShowUsageError wraps a usage error into a typed exit
// value, writes the envelope to stdout in the caller's requested
// format (defaulting to JSON when the format itself is being
// rejected), and returns the exit-2 error for main.go to unwrap.
// Mirrors the journal_list pattern so behavior is consistent.
func emitJournalShowUsageError(w io.Writer, format string, u *journalUsageError, elapsedMS int64) error {
	exit := newJournalUsageExit(u, elapsedMS)
	writeJournalEnvelope(w, journalShowErrorFormat(format), &exit.Envelope)
	return exit
}

// journalShowErrorFormat maps show's three output formats to the
// format writeJournalEnvelope understands for error rendering. JSON
// and text pass through; content falls back to JSON so a caller in
// content mode still gets a machine-parseable failure instead of a
// silent stdout.
func journalShowErrorFormat(format string) string {
	if format == "content" {
		return "json"
	}
	return format
}

// buildJournalShowEnvelope maps a LoadEntries result into the shared
// journalEnvelope. Unlike list, show populates BodyMD + Citations on
// every ok row and leaves Window nil (show is not window-scoped).
// Per-entry Status/Error are stamped onto envelope rows via the
// extended fields on journalEnvelopeEntry — see journal_envelope.go.
func buildJournalShowEnvelope(entries []read.Entry, elapsedMS int64) *journalEnvelope {
	out := make([]journalEnvelopeEntry, 0, len(entries))
	for _, e := range entries {
		row := journalEnvelopeEntry{
			ID:            e.ID,
			Layer:         string(e.Layer),
			Date:          e.Date,
			Team:          e.Team,
			Path:          e.RelPath,
			FactCount:     e.FactCount,
			CitationCount: e.CitationCount,
			SourceFiles:   nonNilStringSlice(e.SourceFiles),
			Citations:     e.Citations,
			BodyMD:        e.BodyMD,
			Status:        e.Status,
		}
		if !e.CreatedAt.IsZero() {
			row.CreatedAt = rfc3339Z(e.CreatedAt)
		}
		if e.Error != nil {
			row.Error = &journalEnvelopeError{
				Code:      e.Error.Code,
				Message:   e.Error.Message,
				Retryable: false,
			}
		}
		out = append(out, row)
	}
	return &journalEnvelope{
		Success:   true,
		Type:      "journal_show",
		ElapsedMS: elapsedMS,
		Data: &journalEnvelopeData{
			Entries: out,
		},
	}
}

// renderJournalShowContent writes ONLY the markdown bodies of every ok
// entry to stdout, with the per-entry marker and the \n---\n separator
// from spec §3.5. Failed entries are skipped — the partial-success
// contract keeps the content stream clean; callers who need failure
// detail must use --format=json.
//
// The marker appears BEFORE each body so the first entry's output
// looks like:
//
//	<!-- entry: 2026-04-12-019c8a3f-0001 -->
//	# Daily Memory — 2026-04-12
//	...
//
// and subsequent entries are preceded by "\n---\n<marker>\n".
func renderJournalShowContent(w io.Writer, entries []read.Entry) {
	first := true
	for _, e := range entries {
		if e.Status != "ok" {
			continue
		}
		if first {
			fmt.Fprintf(w, "<!-- entry: %s -->\n", e.ID)
			first = false
		} else {
			fmt.Fprintf(w, "\n---\n<!-- entry: %s -->\n", e.ID)
		}
		w.Write([]byte(e.BodyMD))
		if len(e.BodyMD) > 0 && e.BodyMD[len(e.BodyMD)-1] != '\n' {
			w.Write([]byte{'\n'})
		}
	}
}

// renderJournalShowText is the --format=text renderer. Shape is pinned
// by spec §3.5:587-596:
//
//	entry 2026-04-12-019c8a3f  daily  team=sageox
//	  path: memory/daily/2026-04-12-019c8a3f-....md
//	  facts: 14   citations: 9   source files: 11
//
//	  # Daily Memory — 2026-04-12
//	  ...
//
// Each ok entry emits the header + path + counts block, a blank line,
// and then the body markdown with every line prefixed by two spaces.
// Failed rows (not_found / ambiguous) fall back to a shorter header +
// error line so a human skim still sees the id and why it missed.
// Entries are separated by a blank line so consecutive bodies do not
// run together.
func renderJournalShowText(w io.Writer, env *journalEnvelope) {
	if env.Data == nil || len(env.Data.Entries) == 0 {
		fmt.Fprintln(w, "(no entries)")
		return
	}
	for i, e := range env.Data.Entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		team := e.Team
		if team == "" {
			team = "-"
		}
		status := e.Status
		if status == "" {
			status = "ok"
		}
		if status != "ok" {
			fmt.Fprintf(w, "entry %s  %s  team=%s  status=%s\n",
				e.ID, e.Layer, team, status)
			if e.Error != nil {
				fmt.Fprintf(w, "  error: %s: %s\n", e.Error.Code, e.Error.Message)
			}
			continue
		}
		fmt.Fprintf(w, "entry %s  %s  team=%s\n", e.ID, e.Layer, team)
		fmt.Fprintf(w, "  path: %s\n", e.Path)
		fmt.Fprintf(w, "  facts: %d   citations: %d   source files: %d\n",
			e.FactCount, e.CitationCount, len(e.SourceFiles))
		if e.BodyMD == "" {
			continue
		}
		fmt.Fprintln(w)
		body := strings.TrimRight(e.BodyMD, "\n")
		for _, line := range strings.Split(body, "\n") {
			if line == "" {
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}
}

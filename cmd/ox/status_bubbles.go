package main

// status_bubbles.go — `ox status` knowledge-bubbles summary line.
//
// One scannable line sourced from the KB API (the only source of bubble
// rows under ox ADR-028). Team contexts and ledgers are conversation
// stores with their own status sections — they are not bubbles and do
// not appear in this count.
//
// Format:
//
//	Knowledge bubbles: <total> (<n> personal, <n> profile, <n> team, <n> repo[, <n> custom][, <n> unknown])
//
// Zero buckets are omitted. Total=0 collapses to `Knowledge bubbles: 0`
// (no parens). Fetch errors degrade gracefully to `(unavailable)` so the
// rest of `ox status` still renders.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/status"
)

// statusBubblesSummary is the small intermediate value that renderers and
// JSON emitters both consume. Keeps presentation logic out of the fetch.
type statusBubblesSummary struct {
	// Total is the sum of all per-type counts.
	Total int

	// ByType is keyed by kb_type slug ("personal", "profile", ...). Only
	// non-zero buckets appear so order-of-iteration callers can skip the
	// is-zero check.
	ByType map[string]int

	// Warnings are non-fatal errors from the KB fetch.
	Warnings []kb.Warning

	// Unavailable reports that the fetch could not run at all. Renderers
	// show "(unavailable)" and skip the breakdown.
	Unavailable bool
}

// statusBubblesTypeOrder is the canonical render order for the by-type
// breakdown. Matches kb_list.go's kbTypePriority so `ox status` and
// `ox kb list` agree on which bucket appears first.
var statusBubblesTypeOrder = []api.KBType{
	api.KBTypePersonal,
	api.KBTypeProfile,
	api.KBTypeTeam,
	api.KBTypeRepo,
	api.KBTypeCustom,
	api.KBType("channel"),
	api.KBTypeUnknown,
}

// summarizeBubbles tallies per-type counts from a KB fetch. Empty/unknown
// types collapse to "unknown" so a forward-compat row never silently
// vanishes from the count.
func summarizeBubbles(res kb.ListResult) statusBubblesSummary {
	by := make(map[string]int)
	for _, b := range res.Bubbles {
		key := string(b.Type)
		if b.Type == "" || b.Type == api.KBTypeUnknown {
			key = string(api.KBTypeUnknown)
		}
		by[key]++
	}
	// drop zero entries (defensive — should never happen since we only
	// increment, but keeps the shape predictable for JSON consumers)
	for k, v := range by {
		if v == 0 {
			delete(by, k)
		}
	}
	return statusBubblesSummary{
		Total:    len(res.Bubbles),
		ByType:   by,
		Warnings: res.Warnings,
	}
}

// formatBubblesLine builds the human-readable summary string with no
// styling applied. Splitting style off makes this trivially testable:
// the assertion is the literal string the user sees.
func formatBubblesLine(s statusBubblesSummary) string {
	if s.Unavailable {
		return "Knowledge bubbles: (unavailable)"
	}
	if s.Total == 0 {
		return "Knowledge bubbles: 0"
	}
	parts := make([]string, 0, len(s.ByType))
	for _, t := range statusBubblesTypeOrder {
		k := string(t)
		if n, ok := s.ByType[k]; ok && n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	// stable order for any custom keys not in statusBubblesTypeOrder
	// (forward-compat: server adds a new type before the CLI knows about
	// it). Sort by key so output stays deterministic.
	if len(parts) < nonZeroBuckets(s.ByType) {
		extras := make([]string, 0)
		known := knownTypeSet()
		for k, n := range s.ByType {
			if _, ok := known[k]; ok {
				continue
			}
			if n > 0 {
				extras = append(extras, fmt.Sprintf("%d %s", n, k))
			}
		}
		sort.Strings(extras)
		parts = append(parts, extras...)
	}
	return fmt.Sprintf("Knowledge bubbles: %d (%s)", s.Total, strings.Join(parts, ", "))
}

// nonZeroBuckets counts entries with value > 0. Used to detect when
// statusBubblesTypeOrder didn't cover every key.
func nonZeroBuckets(m map[string]int) int {
	n := 0
	for _, v := range m {
		if v > 0 {
			n++
		}
	}
	return n
}

// knownTypeSet returns the set of type slugs the CLI knows about, used to
// detect forward-compat extras in formatBubblesLine.
func knownTypeSet() map[string]struct{} {
	out := make(map[string]struct{}, len(statusBubblesTypeOrder))
	for _, t := range statusBubblesTypeOrder {
		out[string(t)] = struct{}{}
	}
	return out
}

// renderBubblesLine produces the styled `ox status` block: one main line
// plus an optional warnings hint. Returns an empty string only if the
// caller passed a zero-value summary (defensive).
func renderBubblesLine(s statusBubblesSummary) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(statusLabelStyle.Render("Knowledge bubbles"))
	main := formatBubblesLine(s)
	// strip the "Knowledge bubbles: " prefix — the label column already
	// shows the field name, so the value column shouldn't repeat it.
	value := strings.TrimPrefix(main, "Knowledge bubbles: ")
	if s.Unavailable {
		b.WriteString(statusMutedStyle.Render(value))
	} else {
		b.WriteString(statusValueStyle.Render(value))
	}
	if len(s.Warnings) > 0 && !s.Unavailable {
		b.WriteString(" ")
		b.WriteString(statusWarningStyle.Render("(warnings: see ox doctor)"))
	}
	b.WriteString("\n")
	return b.String()
}

// buildBubblesJSON converts a summary into the BubblesJSON payload. Uses
// status.BubblesJSON directly so the shape stays in sync with the type
// declared in internal/status/types.go.
func buildBubblesJSON(s statusBubblesSummary) *status.BubblesJSON {
	if s.Unavailable {
		// surface unavailability as zero counts + a synthetic warning
		// rather than omitting the field — JSON consumers should see
		// "the fetch ran, it produced nothing" explicitly.
		return &status.BubblesJSON{
			Total: 0,
			Warnings: []status.BubbleWarningJSON{
				{Error: "kb fetch unavailable"},
			},
		}
	}
	out := &status.BubblesJSON{
		Total: s.Total,
	}
	if len(s.ByType) > 0 {
		out.ByType = make(map[string]int, len(s.ByType))
		for k, v := range s.ByType {
			out.ByType[k] = v
		}
	}
	if len(s.Warnings) > 0 {
		out.Warnings = make([]status.BubbleWarningJSON, 0, len(s.Warnings))
		for _, w := range s.Warnings {
			out.Warnings = append(out.Warnings, status.BubbleWarningJSON{Error: w.Err})
		}
	}
	return out
}

// collectBubblesSummary fetches bubbles with a short timeout and returns a
// summary. Fetch problems never propagate upward — `ox status` must keep
// rendering the rest of the report.
func collectBubblesSummary(fetch statusBubblesFetch) statusBubblesSummary {
	if fetch == nil {
		return statusBubblesSummary{Unavailable: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return summarizeBubbles(fetch(ctx))
}

// statusBubblesFetch is the seam between status rendering and the KB fetch,
// so tests can inject canned results without auth/endpoint plumbing.
type statusBubblesFetch func(ctx context.Context) kb.ListResult

// statusBubblesFetchForRoot is the production wiring: same client
// construction as `ox kb list` so the two surfaces cannot disagree.
// Tests assign a fake.
var statusBubblesFetchForRoot = func(projectRoot string) statusBubblesFetch {
	source, ep := newDefaultKBListSource(projectRoot)
	scopes := ambientKBScopes(projectRoot)
	return func(ctx context.Context) kb.ListResult {
		return kb.FetchBubbles(ctx, source, ep, scopes)
	}
}

// commonDir returns the longest shared leading directory of a and b.
func commonDir(a, b string) string {
	if a == b {
		return a
	}
	sep := string(os.PathSeparator)
	as, bs := strings.Split(a, sep), strings.Split(b, sep)
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	i := 0
	for i < n && as[i] == bs[i] {
		i++
	}
	return strings.Join(as[:i], sep)
}

// renderSlugRef styles a slug reference: the sigil (@ for an owner, # for a
// bubble) is muted so the slug itself stands out — e.g. dim("@") + bright("sageox").
func renderSlugRef(sigil, slug string) string {
	return statusMutedStyle.Render(sigil) + statusValueStyle.Render(slug)
}

// padCell right-pads a (possibly ANSI-styled) cell to width w using its
// visible width, so color codes don't break column alignment.
func padCell(cell string, w int) string {
	if gap := w - lipgloss.Width(cell); gap > 0 {
		return cell + strings.Repeat(" ", gap)
	}
	return cell
}

// renderBubbleStatus renders the dense status cell for a bubble's local
// checkout: a crisp freshness age when clean ("✓ 2h"), the actionable count
// when dirty ("⚠ 6 uncommitted"), a red ⚠ when wedged, or a clone hint.
func renderBubbleStatus(st gitRepoStatus, cloned, bootstrapping bool) string {
	switch {
	case !cloned:
		if bootstrapping {
			return statusMutedStyle.Render("⟳ setting up")
		}
		return statusWarningStyle.Render("⚠ not cloned")
	case st.Error != "":
		return statusErrorStyle.Render("✗ " + st.Error)
	case st.IsWedged():
		// ⚠ glyph in error color — wedged needs eyes like uncommitted, but is worse
		if st.RebaseInProgress {
			return statusErrorStyle.Render("⚠ rebase wedged")
		}
		return statusErrorStyle.Render("⚠ diverged")
	case st.UncommittedCount > 0:
		return statusWarningStyle.Render(fmt.Sprintf("⚠ %d uncommitted", st.UncommittedCount))
	case st.HasLastSync:
		return statusSuccessStyle.Render("✓ " + status.CompactAge(st.LastSync))
	default:
		return statusSuccessStyle.Render("✓ synced")
	}
}

// shortenHome replaces the user's home directory prefix with ~ for display.
func shortenHome(path string) string {
	if path == "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

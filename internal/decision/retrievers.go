package decision

import (
	"context"
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/ledgersearch"
)

func init() {
	RegisterRetriever(sessionsRetriever{})
	RegisterRetriever(murmursRetriever{})
}

// ------------------------------------------------------------- sessions ----

type sessionsRetriever struct{}

func (sessionsRetriever) Name() string { return "prior-sessions" }

// Retrieve surfaces prior coding sessions (and captured plans) from the local
// ledger that match the DR topic — the "why was this built" trail. Zero
// network; fail-open when no ledger.
func (sessionsRetriever) Retrieve(_ context.Context, env *Env, in Input) ([]ContextItem, error) {
	terms := in.Terms()
	if terms == "" || env.LedgerPath == "" {
		return nil, nil
	}
	results, err := ledgersearch.Search(ledgersearch.Options{
		LedgerPath: env.LedgerPath,
		Query:      terms,
		Limit:      5,
	})
	if err != nil || len(results) == 0 {
		return nil, nil
	}

	var out []ContextItem
	for _, r := range results {
		if r.DocType == "murmur" {
			continue // murmursRetriever owns those (and they get no cite)
		}
		if r.Score < minBundleScore {
			continue
		}
		item := ContextItem{
			Kind:    "session",
			Title:   r.DocType + " — " + r.SourceID,
			Ref:     r.SourceID,
			Snippet: strings.TrimSpace(r.Text),
			Score:   r.Score,
			When:    r.CreatedAt,
			Author:  sessionUser(r.SourceID),
		}
		scheme := "session"
		if r.DocType == "plan" || r.DocType == "plan-feedback" {
			scheme = "plan"
			item.Kind = "plan"
		}
		item.Cite = &Cite{
			ProseHint: fmt.Sprintf("A prior %s (%s) covers this ground.", r.DocType, r.SourceID),
			Comment:   fmt.Sprintf("<!-- SOURCE: sageox %s:%s -->", scheme, r.SourceID),
		}
		out = append(out, item)
	}
	return out, nil
}

// sessionUser extracts the user token from a session dir name
// ("2026-07-02T09-14-ryan-a1b2" → "ryan"). Best-effort.
func sessionUser(sourceID string) string {
	parts := strings.Split(sourceID, "-")
	if len(parts) < 3 {
		return ""
	}
	// name sits between the time fields and the trailing id
	return parts[len(parts)-2]
}

// -------------------------------------------------------------- murmurs ----

type murmursRetriever struct{}

func (murmursRetriever) Name() string { return "recent-decision-murmurs" }

// Retrieve surfaces recent murmurs matching the topic — AWARENESS ONLY.
// Murmurs are short-lived (~12h retention), so they never get a Cite: a
// committed DR must not reference a source that evaporates.
func (murmursRetriever) Retrieve(_ context.Context, env *Env, in Input) ([]ContextItem, error) {
	terms := tokenize(in.Terms())
	if len(terms) == 0 || env.LedgerPath == "" {
		return nil, nil
	}
	murmurs, err := ledger.ReadMurmursInWindow(env.LedgerPath, 24)
	if err != nil || len(murmurs) == 0 {
		return nil, nil
	}

	var out []ContextItem
	for _, m := range murmurs {
		content := strings.ToLower(m.Content)
		matched := 0
		for _, t := range terms {
			if strings.Contains(content, t) {
				matched++
			}
		}
		if matched*2 < len(terms) { // require half the terms
			continue
		}
		snippet := m.Content
		if len(snippet) > 160 {
			snippet = snippet[:160] + "…"
		}
		out = append(out, ContextItem{
			Kind:    "murmur",
			Title:   "active work — " + m.Topic,
			Ref:     m.ID,
			Snippet: snippet,
			Score:   0.6,
			Author:  m.PrincipalID,
			When:    m.Timestamp.Format("2006-01-02"),
			// no Cite: non-durable by design
		})
		if len(out) >= 3 {
			break
		}
	}
	return out, nil
}

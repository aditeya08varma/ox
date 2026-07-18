package decision

import (
	"regexp"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// looseIDRe matches loose DR-id query forms: "ADR-021", "adr 21", "021".
var looseIDRe = regexp.MustCompile(`(?i)^([a-z]{2,4})?[\s-]?0*(\d{1,4})$`)

// RelevantDR is a scored corpus hit for external consumers (the ox plan
// context bundle ties plans back to the DRs that shaped them).
type RelevantDR struct {
	ID      string
	Title   string
	RelPath string
	Excerpt string
	Date    string
	Score   float64
}

// Relevant walks this repo's DR corpus fresh and returns the records most
// relevant to query. Zero LLM/network; fail-open (empty on any miss). Exported
// for internal/plan so `ox plan enrich` surfaces the decisions a plan builds
// on — subtly, as context items the render turns into inline markers.
func Relevant(gitRoot, query string, limit int) []RelevantDR {
	if gitRoot == "" || strings.TrimSpace(query) == "" || limit <= 0 {
		return nil
	}
	var cfg *config.DecisionConfig
	if pc, err := config.LoadProjectConfig(gitRoot); err == nil && pc != nil {
		cfg = pc.Decision
	}
	corpus := LoadCorpus(gitRoot, cfg)
	if len(corpus) == 0 {
		return nil
	}
	var out []RelevantDR
	for _, s := range scoreCorpus(corpus, query) {
		if s.score < minBundleScore || len(out) >= limit {
			break
		}
		out = append(out, RelevantDR{
			ID:      s.rec.ID,
			Title:   s.rec.Title,
			RelPath: s.rec.RelPath,
			Excerpt: s.rec.Excerpt,
			Date:    s.rec.Date,
			Score:   s.score,
		})
	}
	return out
}

// scored pairs a corpus record with its lexical relevance to the input terms.
type scored struct {
	rec   Record
	score float64
}

// scoreCorpus ranks corpus records against query terms with the house lexical
// approach (ledgersearch family): tokenize, AND-lean scoring, field weights —
// exact-ID short-circuit 1.0, title ×3, anchors/status/deciders ×2, excerpt ×1.
// Deterministic order: score desc, date desc, id asc. Full-text body search
// stays codedb's job (`ox code search`); this scorer only ranks the structured
// fields the parser extracted.
func scoreCorpus(corpus []Record, query string) []scored {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	qUpper := strings.ToUpper(strings.TrimSpace(query))

	var out []scored
	for _, rec := range corpus {
		if rec.ID != "" && rec.ID == normalizeLooseID(qUpper) {
			out = append(out, scored{rec: rec, score: 1.0})
			continue
		}
		s := fieldScore(terms, rec)
		if s > 0 {
			out = append(out, scored{rec: rec, score: s})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].rec.Date != out[j].rec.Date {
			return out[i].rec.Date > out[j].rec.Date
		}
		return out[i].rec.ID < out[j].rec.ID
	})
	return out
}

func fieldScore(terms []string, rec Record) float64 {
	title := strings.ToLower(rec.Title)
	var anchors strings.Builder
	for _, d := range rec.DSections {
		anchors.WriteString(strings.ToLower(d.Heading))
		anchors.WriteByte(' ')
	}
	mid := anchors.String() + strings.ToLower(rec.Status) + " " + strings.ToLower(strings.Join(rec.Deciders, " "))
	excerpt := strings.ToLower(rec.Excerpt)

	matched := 0
	var raw float64
	for _, t := range terms {
		hit := false
		if strings.Contains(title, t) {
			raw += 3
			hit = true
		}
		if strings.Contains(mid, t) {
			raw += 2
			hit = true
		}
		if strings.Contains(excerpt, t) {
			raw += 1
			hit = true
		}
		if hit {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	// coverage-dominant score: fraction of terms matched, with the weighted
	// hits as a small tiebreaker. Requiring most terms keeps AND semantics
	// without zeroing near-misses on long titles.
	coverage := float64(matched) / float64(len(terms))
	if coverage < 0.5 {
		return 0
	}
	return coverage*0.85 + minF(raw/float64(6*len(terms)), 1.0)*0.15
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// tokenize mirrors the ledgersearch tokenizer: lowercase alphanumeric terms,
// punctuation stripped, stopword-light (short tokens dropped).
func tokenize(q string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		t := cur.String()
		cur.Reset()
		if len(t) < 3 {
			return
		}
		switch t {
		case "the", "and", "for", "with", "this", "that", "into", "over", "our":
			return
		}
		out = append(out, t)
	}
	for _, r := range strings.ToLower(q) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// normalizeLooseID canonicalizes loose id forms ("adr 21", "ADR-021", "021")
// to the catalog form "ADR-021"; empty when the input isn't id-shaped.
func normalizeLooseID(q string) string {
	m := looseIDRe.FindStringSubmatch(strings.TrimSpace(q))
	if m == nil {
		return ""
	}
	prefix := strings.ToUpper(m[1])
	if prefix == "" {
		prefix = "ADR"
	}
	return normalizeRefToken(prefix, m[2])
}

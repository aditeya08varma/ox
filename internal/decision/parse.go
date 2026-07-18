package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Record is one cataloged Decision Record, extracted deterministically (zero
// LLM) with a tolerant parser: real corpora mix templates, so every field is
// best-effort and absence is recorded, never fatal.
type Record struct {
	// ID is the canonical prefixed id ("ADR-021", "DDR-004"); empty when the
	// file carries no number.
	ID     string `json:"id,omitempty"`
	Prefix string `json:"prefix,omitempty"` // ADR | DDR | … (upper-cased)
	Number int    `json:"number,omitempty"`
	// DRType classifies the record: "adr" | "ddr" | "other".
	DRType   string   `json:"dr_type,omitempty"`
	Title    string   `json:"title"`
	Status   string   `json:"status,omitempty"`
	Date     string   `json:"date,omitempty"`
	Deciders []string `json:"deciders,omitempty"`
	Corpus   string   `json:"corpus"`
	Path     string   `json:"path"`
	RelPath  string   `json:"rel_path,omitempty"`
	Mtime    int64    `json:"mtime"`
	Size     int64    `json:"size"`
	// ContentHash is a short sha256 prefix for change detection and dedup of
	// moved files.
	ContentHash string `json:"content_hash,omitempty"`
	// DSections are the numbered sub-decision anchors ("D1".."Dn") cited from
	// other documents — the sageox-mono convention.
	DSections []DAnchor `json:"d_sections,omitempty"`
	// Amendments are the dated inline amendment markers.
	Amendments []Amendment `json:"amendments,omitempty"`
	// Refs are outbound DR tokens found in the body ("ADR-046"), self excluded.
	Refs []string `json:"refs,omitempty"`
	// Supersedes / SupersededBy are parsed from status and explicit lines.
	Supersedes   []string `json:"supersedes,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty"`
	Excerpt      string   `json:"excerpt,omitempty"`
}

// DAnchor is one numbered sub-decision heading inside a DR.
type DAnchor struct {
	ID      string `json:"id"` // "D4"
	Heading string `json:"heading,omitempty"`
}

// Amendment is one dated amendment marker inside a DR.
type Amendment struct {
	Date    string `json:"date"`
	Excerpt string `json:"excerpt,omitempty"`
}

var (
	// filename forms: ADR-021-slug.md · ddr_3.md · 002-daemon-architecture.md · adr-ephemeral-mode.md
	fileIDRe  = regexp.MustCompile(`(?i)^([a-z]{2,4})[-_](\d{1,4})\b`)
	fileNumRe = regexp.MustCompile(`^(\d{1,4})[-_]`)
	// heading form: "# ADR-021: Title" (also captures the title tail)
	headIDRe = regexp.MustCompile(`(?i)^#\s+([a-z]{2,4})-(\d{1,4})\s*[:—-]?\s*(.*)$`)

	frontmatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)
	fmFieldRe     = regexp.MustCompile(`(?m)^(title|status|date|deciders|type)\s*:\s*(.+)$`)

	// metadata lines: "**Status**: Accepted" / "Status: Accepted" / "- **Status:** Accepted"
	metaLineRe = regexp.MustCompile(`(?im)^[-*\s]*\*{0,2}(status|date|deciders|decision makers|supersedes)\*{0,2}\s*[:：]\s*\*{0,2}([^*\n]+)`)

	// D-section anchors: "### D4: …" or numbered decision subheads "### 4. …" / "### 4 —"
	dHeadRe   = regexp.MustCompile(`(?m)^#{2,4}\s+D(\d{1,2})\b[:.\s—-]*(.*)$`)
	numHeadRe = regexp.MustCompile(`(?m)^###\s+(\d{1,2})[.)\s:—-]+(.*)$`)

	amendmentRe = regexp.MustCompile(`(?m)\*\*Amendment \((\d{4}-\d{2}-\d{2})[a-z]?\)[^*]*\*\*[:.]?\s*([^\n]{0,140})`)

	// outbound DR tokens: "ADR-046", "ADR 046", "adr046", "DDR-3"
	refTokenRe = regexp.MustCompile(`(?i)\b(ADR|DDR)[\s-]?(\d{1,4})\b`)

	supersededByRe = regexp.MustCompile(`(?i)superseded\s+by\s+((?:ADR|DDR)[\s-]?\d{1,4})`)
	h1Re           = regexp.MustCompile(`(?m)^#\s+(.+)$`)
)

// ParseContent extracts a Record from markdown. path may be "" (stdin draft);
// corpus/mtime/size are stamped by the caller.
func ParseContent(path, content string) Record {
	rec := Record{Path: path}

	sum := sha256.Sum256([]byte(content))
	rec.ContentHash = hex.EncodeToString(sum[:6])

	fm := map[string]string{}
	if m := frontmatterRe.FindStringSubmatch(content); m != nil {
		for _, f := range fmFieldRe.FindAllStringSubmatch(m[1], -1) {
			fm[strings.ToLower(f[1])] = strings.Trim(strings.TrimSpace(f[2]), `"'`)
		}
	}

	base := baseName(path)
	if m := fileIDRe.FindStringSubmatch(base); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			rec.Prefix = strings.ToUpper(m[1])
			rec.Number = n
		}
	} else if m := fileNumRe.FindStringSubmatch(base); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			// numeric-only filenames ("002-daemon-architecture.md") normalize
			// to the ADR prefix — the observed legacy convention.
			rec.Prefix = "ADR"
			rec.Number = n
		}
	}

	// H1 may carry both id and title ("# ADR-021: Plan context, not inference").
	if m := headIDRe.FindStringSubmatch(firstH1Line(content)); m != nil {
		if rec.Number == 0 {
			if n, err := strconv.Atoi(m[2]); err == nil {
				rec.Prefix = strings.ToUpper(m[1])
				rec.Number = n
			}
		}
		if t := strings.TrimSpace(m[3]); t != "" {
			rec.Title = t
		}
	}
	if rec.Number > 0 {
		rec.ID = fmt.Sprintf("%s-%03d", rec.Prefix, rec.Number)
	}

	// Title precedence: frontmatter > H1 (tail already applied) > full H1 > filename stem.
	if t := fm["title"]; t != "" {
		rec.Title = t
	}
	if rec.Title == "" {
		if m := h1Re.FindStringSubmatch(content); m != nil {
			rec.Title = strings.TrimSpace(m[1])
		}
	}
	if rec.Title == "" && base != "" {
		rec.Title = strings.TrimSuffix(base, ".md")
	}

	for _, m := range metaLineRe.FindAllStringSubmatch(content, -1) {
		key, val := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		switch key {
		case "status":
			if rec.Status == "" {
				rec.Status = val
			}
		case "date":
			if rec.Date == "" {
				rec.Date = val
			}
		case "deciders", "decision makers":
			if len(rec.Deciders) == 0 {
				rec.Deciders = splitList(val)
			}
		case "supersedes":
			for _, r := range refTokenRe.FindAllStringSubmatch(val, -1) {
				rec.Supersedes = append(rec.Supersedes, normalizeRefToken(r[1], r[2]))
			}
		}
	}
	if rec.Status == "" {
		rec.Status = fm["status"]
	}
	if rec.Date == "" {
		rec.Date = fm["date"]
	}

	if m := supersededByRe.FindStringSubmatch(content); m != nil {
		toks := refTokenRe.FindStringSubmatch(m[1])
		if toks != nil {
			rec.SupersededBy = normalizeRefToken(toks[1], toks[2])
		}
	}

	rec.DSections = parseDAnchors(content)
	for _, m := range amendmentRe.FindAllStringSubmatch(content, -1) {
		rec.Amendments = append(rec.Amendments, Amendment{Date: m[1], Excerpt: strings.TrimSpace(m[2])})
	}

	seenRefs := map[string]struct{}{}
	for _, m := range refTokenRe.FindAllStringSubmatch(content, -1) {
		ref := normalizeRefToken(m[1], m[2])
		if ref == rec.ID {
			continue // self-reference
		}
		if _, ok := seenRefs[ref]; ok {
			continue
		}
		seenRefs[ref] = struct{}{}
		rec.Refs = append(rec.Refs, ref)
	}

	rec.DRType = classifyDRType(rec.Prefix, path, fm["type"])
	rec.Excerpt = extractExcerpt(content)
	return rec
}

// IsRecord reports whether the parse found something DR-shaped worth
// cataloging: a number, or a title plus DR metadata (status/date). Plain
// markdown with neither is skipped silently.
func (r Record) IsRecord() bool {
	if r.Number > 0 {
		return true
	}
	return r.Title != "" && (r.Status != "" || r.Date != "")
}

func parseDAnchors(content string) []DAnchor {
	var out []DAnchor
	seen := map[string]struct{}{}
	add := func(id, heading string) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, DAnchor{ID: id, Heading: strings.TrimSpace(heading)})
	}
	for _, m := range dHeadRe.FindAllStringSubmatch(content, -1) {
		add("D"+m[1], m[2])
	}
	// numbered subheads inside the "## Decision" section also anchor as D<n>
	if sec := sectionBody(content, "Decision"); sec != "" {
		for _, m := range numHeadRe.FindAllStringSubmatch(sec, -1) {
			add("D"+m[1], m[2])
		}
	}
	return out
}

// sectionBody returns the body of the "## <name>" section (until the next H2).
func sectionBody(content, name string) string {
	re := regexp.MustCompile(`(?ms)^##\s+` + regexp.QuoteMeta(name) + `\b.*?$`)
	loc := re.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	rest := content[loc[1]:]
	if next := regexp.MustCompile(`(?m)^##\s`).FindStringIndex(rest); next != nil {
		return rest[:next[0]]
	}
	return rest
}

func extractExcerpt(content string) string {
	body := sectionBody(content, "Context")
	if body == "" {
		body = content
	}
	for _, para := range strings.Split(body, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" || strings.HasPrefix(para, "#") || strings.HasPrefix(para, "**Status") ||
			strings.HasPrefix(para, "|") || strings.HasPrefix(para, "---") || strings.HasPrefix(para, "<!--") {
			continue
		}
		para = strings.Join(strings.Fields(para), " ")
		if len(para) > 200 {
			para = para[:200] + "…"
		}
		return para
	}
	return ""
}

func classifyDRType(prefix, path, fmType string) string {
	switch strings.ToLower(fmType) {
	case "adr", "ddr":
		return strings.ToLower(fmType)
	}
	switch prefix {
	case "ADR":
		return "adr"
	case "DDR":
		return "ddr"
	}
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, "adr"), strings.Contains(lower, "architecture decision"):
		return "adr"
	case strings.Contains(lower, "ddr"), strings.Contains(lower, "design decision"):
		return "ddr"
	}
	return "other"
}

func normalizeRefToken(prefix, num string) string {
	n, err := strconv.Atoi(num)
	if err != nil {
		return strings.ToUpper(prefix) + "-" + num
	}
	return fmt.Sprintf("%s-%03d", strings.ToUpper(prefix), n)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '/' }) {
		part = strings.TrimSpace(strings.Trim(part, "*_"))
		if part != "" && !strings.EqualFold(part, "and") {
			out = append(out, part)
		}
	}
	return out
}

func firstH1Line(content string) string {
	if m := h1Re.FindString(content); m != "" {
		return m
	}
	return ""
}

func baseName(path string) string {
	if path == "" {
		return ""
	}
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

package search

import "strings"

// SearchType determined by the type: filter.
type SearchType int

const (
	SearchTypeCode SearchType = iota
	SearchTypeDiff
	SearchTypeCommit
	SearchTypeSymbol
	SearchTypeComment
	SearchTypePR
	SearchTypeIssue
)

func (st SearchType) String() string {
	switch st {
	case SearchTypeDiff:
		return "diff"
	case SearchTypeCommit:
		return "commit"
	case SearchTypeSymbol:
		return "symbol"
	case SearchTypeComment:
		return "comment"
	case SearchTypePR:
		return "pr"
	case SearchTypeIssue:
		return "issue"
	default:
		return "code"
	}
}

// SelectType determined by the select: filter.
type SelectType int

const (
	SelectNone SelectType = iota
	SelectRepo
	SelectFile
	SelectSymbol
	SelectSymbolKind
)

// Filters parsed from the query string.
type Filters struct {
	Repo        string
	NegRepo     string
	File        string
	NegFile     string
	fileAny     []string
	Lang        string
	NegLang     string
	Rev         string
	Count       int // 0 means default (20)
	Case        bool
	Author      string
	NegAuthor   string
	Before      string
	After       string
	Message     string
	NegMessage  string
	Select      SelectType
	SelectKind  string // for SelectSymbolKind
	Calls       string
	CalledBy    string
	Returns     string
	Depth       int // for multi-hop calls:/calledby: (default 1, max 10)
	CommentKind string
	State       string
	// Confidence is the minimum confidence threshold for ADR-019 symbol_edges
	// queries. Values: "extracted" | "inferred" | "ambiguous". When set,
	// calls:/calledby: query the resolved symbol_edges table instead of the
	// LIKE-name-match path on symbol_refs. Empty (default) keeps the existing
	// name-match behavior so older codedbs (no edges yet) still work.
	Confidence string
}

// ParsedQuery is the result of parsing a query string.
type ParsedQuery struct {
	// SearchTerms grouped by OR. Each group is space-joined AND terms.
	SearchTerms []string
	Type        SearchType
	IsRegex     bool
	Filters     Filters
}

// SearchPattern returns all OR groups joined with " OR ".
func (q *ParsedQuery) SearchPattern() string {
	return strings.Join(q.SearchTerms, " OR ")
}

// HasEmptyPattern returns true if there are no search terms.
func (q *ParsedQuery) HasEmptyPattern() bool {
	if len(q.SearchTerms) == 0 {
		return true
	}
	for _, t := range q.SearchTerms {
		if t != "" {
			return false
		}
	}
	return true
}

// RestrictFiles adds an internal file-path OR filter. It is intentionally not
// parsed from query text; callers use it for UI flags that need pre-limit
// filtering without extending the public query syntax.
func (q *ParsedQuery) RestrictFiles(patterns []string) {
	if q == nil {
		return
	}
	q.Filters.fileAny = append([]string(nil), patterns...)
}

// TranslatedQuery is SQL ready for execution with bound parameters.
type TranslatedQuery struct {
	SQL        string
	Params     []string
	SearchType SearchType
}

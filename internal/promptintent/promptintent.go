// Package promptintent implements lightweight classification of a user's
// freshly submitted prompt to decide whether it is worth firing a local
// recall query against the ledger / team-context cache.
//
// Two gates run in order:
//
//  1. Length gate (MinPromptChars) — rules out "yes", "ok", "go", "fix this"
//     and similar low-signal turns where recall would mostly be noise.
//  2. Intent classifier — only prompts that look like a *question* or
//     *recall request* are forwarded. Prompts that begin with a clear
//     edit/action verb (fix, add, write, ...) are skipped because the user
//     is already mid-flow and wants execution, not retrieval.
//
// The package is intentionally dependency-free and pure-string so it can
// be unit-tested in isolation and reused outside the hook handler.
package promptintent

import (
	"regexp"
	"strings"
)

// MinPromptChars is the lower bound below which the prompt is considered
// too short to bother running recall on. ~40 tokens ≈ 160 chars by the
// rough 4-chars-per-token English heuristic used across LLM tooling.
const MinPromptChars = 160

// recallPatterns match phrases that signal the user is trying to recall
// or discover something. They are intentionally broad — false positives
// here only cost a 100ms local lookup, false negatives lose discovery.
var recallPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bfind\b`),
	regexp.MustCompile(`(?i)\brecall\b`),
	regexp.MustCompile(`(?i)\bhow\s+did\s+we\b`),
	regexp.MustCompile(`(?i)\bhow\s+do\s+we\b`),
	regexp.MustCompile(`(?i)\bhas\s+anyone\b`),
	regexp.MustCompile(`(?i)\bwhat\s+is\b`),
	regexp.MustCompile(`(?i)\bwhat\s+are\b`),
	regexp.MustCompile(`(?i)\bexplain\b`),
	regexp.MustCompile(`(?i)\bwhere\s+is\b`),
	regexp.MustCompile(`(?i)\bwhy\s+does\b`),
	regexp.MustCompile(`(?i)\bwhy\s+did\b`),
	regexp.MustCompile(`(?i)\bwhen\s+did\b`),
	regexp.MustCompile(`(?i)\bwho\s+(wrote|owns|added|changed)\b`),
	regexp.MustCompile(`(?i)\bsearch\s+for\b`),
	regexp.MustCompile(`(?i)\blook\s+up\b`),
}

// actionVerbs are leading words that imply the user wants execution, not
// retrieval. Matched only at the start of the prompt (after whitespace)
// so that "explain why fix X failed" still triggers recall.
var actionVerbs = map[string]struct{}{
	"edit":      {},
	"fix":       {},
	"add":       {},
	"make":      {},
	"write":     {},
	"create":    {},
	"delete":    {},
	"remove":    {},
	"refactor":  {},
	"rename":    {},
	"implement": {},
	"build":     {},
	"run":       {},
	"commit":    {},
	"push":      {},
	"merge":     {},
	"rebase":    {},
	"deploy":    {},
}

// LooksLikeRecall reports whether the given prompt is worth running a
// local recall query against. It returns true only when:
//   - the prompt is at least MinPromptChars long, AND
//   - it does NOT start with a recognized action verb, AND
//   - it matches at least one recall pattern.
//
// The function is pure: no I/O, no allocations beyond regex matching.
func LooksLikeRecall(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	if len(trimmed) < MinPromptChars {
		return false
	}
	if startsWithActionVerb(trimmed) {
		return false
	}
	for _, re := range recallPatterns {
		if re.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// startsWithActionVerb returns true if the first whitespace-delimited
// token of the prompt is a recognized action verb. Comparison is
// case-insensitive and strips trailing punctuation from the leading word.
func startsWithActionVerb(s string) bool {
	first := s
	if idx := strings.IndexAny(s, " \t\n"); idx >= 0 {
		first = s[:idx]
	}
	first = strings.TrimRight(first, ",.;:!?")
	first = strings.ToLower(first)
	_, ok := actionVerbs[first]
	return ok
}

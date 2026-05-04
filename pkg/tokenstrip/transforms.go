package tokenstrip

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bbalet/stopwords"
	"golang.org/x/text/unicode/norm"
)

// state carries per-run config and compiled regexes.
type state struct {
	opts       Options
	thinkingRe *regexp.Regexp
	multiSpace *regexp.Regexp
	multiNL    *regexp.Regexp
	trailingWS *regexp.Regexp
	synonymRes []synonymRule
}

type synonymRule struct {
	re      *regexp.Regexp
	replace string
}

// Invisible / unusual whitespace codepoints handled by stripZeroWidth.
// Codepoints listed explicitly (not as a range) so future edits are
// reviewable without decoding numeric ranges.
const (
	zwSpace        = "\u200b" // ZERO WIDTH SPACE
	zwNonJoiner    = "\u200c" // ZERO WIDTH NON-JOINER
	zwJoiner       = "\u200d" // ZERO WIDTH JOINER
	leftToRightMrk = "\u200e" // LEFT-TO-RIGHT MARK
	rightToLeftMrk = "\u200f" // RIGHT-TO-LEFT MARK
	zwNoBreakSpace = "\ufeff" // ZERO WIDTH NO-BREAK SPACE / BOM
	noBreakSpace   = "\u00a0" // NO-BREAK SPACE
	narrowNBSP     = "\u202f" // NARROW NO-BREAK SPACE
)

// zeroWidthTriggerSet is the set of codepoints that should trigger the
// replacer. Kept in sync with zeroWidthReplacer below.
const zeroWidthTriggerSet = zwSpace + zwNonJoiner + zwJoiner +
	leftToRightMrk + rightToLeftMrk + zwNoBreakSpace +
	noBreakSpace + narrowNBSP

// zeroWidthReplacer strips zero-width codepoints and collapses no-break
// spaces to regular ASCII space. Single-pass via strings.NewReplacer.
var zeroWidthReplacer = strings.NewReplacer(
	zwSpace, "",
	zwNonJoiner, "",
	zwJoiner, "",
	leftToRightMrk, "",
	rightToLeftMrk, "",
	zwNoBreakSpace, "",
	noBreakSpace, " ",
	narrowNBSP, " ",
)

func newState(opts Options) *state {
	s := &state{
		opts: opts,
		// (?s) lets . match newlines; non-greedy captures one block at a time.
		thinkingRe: regexp.MustCompile(`(?s)<thinking>(.*?)</thinking>`),
		multiSpace: regexp.MustCompile(`[ \t]{2,}`),
		// Three-or-more newlines collapse to two (preserves paragraph breaks).
		multiNL: regexp.MustCompile(`\n{3,}`),
		// Trailing spaces/tabs before end-of-line OR end-of-string. The
		// alternation matters for the last line of any buffer that doesn't
		// end with a newline — otherwise trailing whitespace there survives.
		trailingWS: regexp.MustCompile(`[ \t]+(\n|$)`),
	}

	if opts.EnableSynonymSub && len(opts.SynonymTable) > 0 {
		s.synonymRes = make([]synonymRule, 0, len(opts.SynonymTable))
		// Iterate keys in sorted order for deterministic rule construction.
		// Go map range order is randomized; the rules are applied sequentially
		// at match time, and non-deterministic ordering would make one run
		// produce different output than another given the same input. The
		// package advertises determinism.
		keys := make([]string, 0, len(opts.SynonymTable))
		for k := range opts.SynonymTable {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := opts.SynonymTable[k]
			// Whole-word, case-insensitive match. regexp.QuoteMeta guards
			// against regex metacharacters in user-supplied keys.
			pattern := `(?i)\b` + regexp.QuoteMeta(k) + `\b`
			s.synonymRes = append(s.synonymRes, synonymRule{
				re:      regexp.MustCompile(pattern),
				replace: v,
			})
		}
	}

	return s
}

// transform dispatches by entry type. On parse failure the line passes
// through verbatim — we prefer correctness over aggression.
func (s *state) transform(line []byte, stats *Stats) ([]byte, error) {
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return line, nil
	}

	// Hard rule: user turns and header entries are never touched. Intent
	// signal and session metadata are sacred.
	if e.Type == "user" || e.Type == "header" {
		return line, nil
	}

	// Only assistant-authored content is in scope for these transforms.
	// For other types (tool, system, tool_mark, tool_ref, unknown) we skip
	// to avoid mutating summarizer scaffolding.
	if e.Type != "assistant" {
		return line, nil
	}

	if e.Content == "" {
		return line, nil
	}

	origContent := e.Content
	newContent := e.Content

	if nfc := nfcNormalize(newContent); nfc != newContent {
		stats.NFCNormalized++
		newContent = nfc
	}
	if zw := stripZeroWidth(newContent); zw != newContent {
		stats.ZeroWidthStripped++
		newContent = zw
	}
	if canon := s.canonWS(newContent); canon != newContent {
		stats.WhitespaceCanonicalized++
		newContent = canon
	}

	// Thinking-block-only transforms. The transform applied depends on
	// DropThinkingMode:
	//   - None (default): stop-word + synonym strip on inner text;
	//     block structure preserved.
	//   - FirstSentence: keep just the first sentence of inner text;
	//     elide the rest. Token savings without losing reasoning framing.
	//   - All: drop the whole block (including tags). Maximum savings.
	//
	// The summary schema doesn't require thinking content, but on long
	// Sonnet/Opus sessions thinking blocks can be 30–50% of assistant
	// prose. The first-sentence variant is the conservative middle:
	// keeps the hint of what reasoning was happening without the cost.
	newContent = s.thinkingRe.ReplaceAllStringFunc(newContent, func(block string) string {
		m := s.thinkingRe.FindStringSubmatch(block)
		if len(m) < 2 {
			return block
		}
		inner := m[1]

		switch s.opts.DropThinkingMode {
		case DropThinkingAll:
			stats.ThinkingBlocksDropped++
			return ""
		case DropThinkingFirstSentence:
			trimmed := firstSentence(inner)
			if trimmed != inner {
				stats.ThinkingBlocksTrimmed++
				return "<thinking>" + trimmed + " ...[reasoning elided]</thinking>"
			}
			// Single-sentence thinking block — fall through to stop-word
			// strip so the entry still benefits from baseline reduction.
			fallthrough
		default:
			before := inner
			stripped := stopwords.CleanString(inner, s.opts.StopWordLanguage, false)
			if stripped != inner {
				stats.StopWordsRemoved++
				inner = stripped
			}
			if s.opts.EnableSynonymSub {
				subbed := s.applySynonyms(inner)
				if subbed != inner {
					stats.SynonymsSubstituted++
					inner = subbed
				}
			}
			if inner == before {
				return block
			}
			return "<thinking>" + inner + "</thinking>"
		}
	})

	if newContent == origContent {
		return line, nil
	}

	// Preserve unknown top-level fields: decode the line as a generic map,
	// overwrite only "content", re-encode. Same round-trip pattern as
	// pkg/tokenopt's transformLossless.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		e.Content = newContent
		out, merr := json.Marshal(&e)
		if merr != nil {
			return nil, fmt.Errorf("marshal: %w", merr)
		}
		return out, nil
	}
	b, err := json.Marshal(newContent)
	if err != nil {
		return nil, fmt.Errorf("marshal content: %w", err)
	}
	raw["content"] = b
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal entry: %w", err)
	}
	return out, nil
}

// nfcNormalize returns the NFC-normalized form. Lossless — composes any
// decomposed sequences so downstream tokenizers don't split single
// logical characters into multiple tokens.
func nfcNormalize(in string) string {
	if norm.NFC.IsNormalString(in) {
		return in
	}
	return norm.NFC.String(in)
}

// stripZeroWidth removes zero-width codepoints and collapses no-break
// spaces to regular space. These codepoints are token-expensive and
// semantically invisible (or nearly so).
func stripZeroWidth(in string) string {
	// Fast path: common case has none of these. strings.ContainsAny is a
	// cheap scan and avoids allocating when there's nothing to do.
	if !strings.ContainsAny(in, zeroWidthTriggerSet) {
		return in
	}
	return zeroWidthReplacer.Replace(in)
}

// canonWS collapses runs of spaces/tabs to a single space, trims trailing
// whitespace per line, and caps consecutive newlines at two (paragraph
// break preserved, excess collapsed).
func (s *state) canonWS(in string) string {
	out := in
	out = s.trailingWS.ReplaceAllString(out, "\n")
	out = s.multiSpace.ReplaceAllString(out, " ")
	out = s.multiNL.ReplaceAllString(out, "\n\n")
	return out
}

// applySynonyms runs each compiled synonym regex in insertion order.
// Only called on text inside <thinking> blocks.
func (s *state) applySynonyms(in string) string {
	out := in
	for _, rule := range s.synonymRes {
		out = rule.re.ReplaceAllString(out, rule.replace)
	}
	return out
}

// firstSentence returns the first sentence of s, defined as everything
// up to and including the first ".", "!", or "?" followed by whitespace
// or end-of-string. Returns s unchanged if no sentence boundary is found
// (caller should fall through to the per-sentence transforms).
//
// Why a custom function instead of a sentence-boundary library: thinking
// blocks are short (rarely > a few sentences), the heuristic is good
// enough for "keep the framing line," and pulling in NLP dependencies
// would violate this package's stdlib-mostly contract.
func firstSentence(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		// Sentence boundary requires whitespace or EOS after the punct.
		// Catches "I'll do X. Then Y." (split after first period) but
		// not "version 1.0" (no whitespace after the dot, stays together).
		next := i + 1
		if next >= len(s) {
			return s[:next] // punct at end is its own sentence
		}
		nc := s[next]
		if nc == ' ' || nc == '\t' || nc == '\n' || nc == '\r' {
			return s[:next]
		}
	}
	return s
}

// estimateTokens is a coarse "~4 chars per token" heuristic. Anthropic's
// public rule of thumb is ~3.5-4 chars/token for English text; for JSON
// with lots of short keys and structure it's closer to 3. We use 4 as a
// middle-of-the-road approximation. Swap for a real BPE tokenizer if
// exact counts become important.
func estimateTokens(b []byte) int64 {
	if len(b) == 0 {
		return 0
	}
	// Integer division rounds down; for a non-empty input we want at
	// least 1 token, otherwise TokensInEstimate can be 0 despite real
	// content and skew Reduction() math.
	t := int64(len(b)) / 4
	if t == 0 {
		return 1
	}
	return t
}

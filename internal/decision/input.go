package decision

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Input is the enrich subject: a topic (pre-draft consult), a draft on stdin,
// or an existing DR file.
type Input struct {
	// Path is set when enriching an existing file (--file).
	Path string
	// Raw is the draft/file markdown. Empty in topic mode.
	Raw string
	// Topic is the pre-draft subject (--topic). Empty in draft/file mode.
	Topic string
	// Parsed identity of the draft/file (zero-valued in topic mode).
	Record Record
}

// maxInputBytes caps stdin/file reads: a DR is a short document; anything
// larger is a wrong file, not a decision record.
const maxInputBytes = 2 << 20

// ResolveInput builds the enrich Input from --topic, --file, or stdin, in
// that precedence order.
func ResolveInput(topic, file string, stdin io.Reader) (Input, error) {
	if t := strings.TrimSpace(topic); t != "" {
		return Input{Topic: t}, nil
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return Input{}, fmt.Errorf("read %s: %w", file, err)
		}
		rec := ParseContent(file, string(data))
		return Input{Path: file, Raw: string(data), Record: rec}, nil
	}
	if stdin != nil {
		if f, ok := stdin.(*os.File); ok {
			if fi, err := f.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
				return Input{}, nil // interactive terminal, nothing piped
			}
		}
		data, err := io.ReadAll(io.LimitReader(stdin, maxInputBytes))
		if err != nil {
			return Input{}, fmt.Errorf("read stdin: %w", err)
		}
		raw := strings.TrimSpace(string(data))
		if raw == "" {
			return Input{}, nil
		}
		rec := ParseContent("", raw)
		return Input{Raw: raw, Record: rec}, nil
	}
	return Input{}, nil
}

// Terms returns the lexical search terms for this input: the topic in topic
// mode, else the parsed title (falling back to the first heading line of the
// raw draft).
func (in Input) Terms() string {
	if in.Topic != "" {
		return in.Topic
	}
	if in.Record.Title != "" {
		return in.Record.Title
	}
	for _, line := range strings.Split(in.Raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimLeft(line, "# ")
		}
	}
	return ""
}

// sourceCommentRe matches the machine citation comments enrich emits:
// <!-- SOURCE: sageox discussion:2026-05-28-… --> (scheme:value captured).
var sourceCommentRe = regexp.MustCompile(`<!--\s*SOURCE:\s*sageox\s+([a-z]+:[^\s>]+)\s*-->`)

// SourceRefs extracts every machine citation ref from the input body.
func (in Input) SourceRefs() []string {
	var out []string
	for _, m := range sourceCommentRe.FindAllStringSubmatch(in.Raw, -1) {
		out = append(out, m[1])
	}
	return out
}

// visibleCreditRe matches VISIBLE SageOx credit phrasings in prose. HTML
// comments are stripped before matching so invisible machine refs never count
// against the credit cap.
var (
	htmlCommentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	visibleCreditRe = regexp.MustCompile(`(?i)(surfaced by sageox|guided by sageox|sageox surfaced|via sageox|by sageox)`)
)

// VisibleSageoxCredits counts visible SageOx credit phrases in the body —
// enforcing the house cap (≤2 per DR, 3 only when SageOx meaningfully steered
// the decision process).
func (in Input) VisibleSageoxCredits() int {
	stripped := htmlCommentRe.ReplaceAllString(in.Raw, "")
	return len(visibleCreditRe.FindAllString(stripped, -1))
}

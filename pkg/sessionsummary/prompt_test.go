package sessionsummary

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildSummaryPrompt(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "hello"},
		{Type: EntryTypeAssistant, Content: "hi"},
	}

	t.Run("includes session file path", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "")
		assert.Contains(t, result, "/tmp/raw.jsonl")
	})

	t.Run("describes both raw and summary-input entry shapes", func(t *testing.T) {
		// Prompt must stay agnostic to which file it points at, because
		// callers pass either raw.jsonl or the tokenopt-optimized file.
		// The summarizer needs to recognize tool_mark in the optimized
		// case (and the optional `count` field for batched runs); since
		// the design dropped tool_name/brief/output, the prompt no longer
		// mentions "brief" — only "tool_mark" and the count semantics.
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "")
		assert.Contains(t, result, `"tool_mark"`)
		assert.Contains(t, result, "count")
		assert.Contains(t, result, `"user"`)
		assert.Contains(t, result, `"assistant"`)
	})

	t.Run("includes push step when ledger dir provided", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "/ledger/sessions/abc")
		assert.Contains(t, result, "ox session push-summary")
		assert.Contains(t, result, "/ledger/sessions/abc")
	})

	t.Run("omits push step when ledger dir empty", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "")
		assert.NotContains(t, result, "ox session push-summary")
	})

	t.Run("empty entries still produces a usable prompt", func(t *testing.T) {
		// Prompt no longer states a hardcoded entry count — that count was
		// unreliable once tokenopt started dropping system entries and
		// collapsing tools. The prompt should still reference the file and
		// describe the format.
		result := BuildSummaryPrompt(nil, "/tmp/raw.jsonl", "")
		assert.Contains(t, result, "/tmp/raw.jsonl")
		assert.Contains(t, result, "JSONL format")
	})

	t.Run("path with spaces", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/my session/raw.jsonl", "")
		assert.Contains(t, result, "/tmp/my session/raw.jsonl")
	})

	t.Run("includes agent_summary guidelines", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "")
		assert.True(t, strings.Contains(result, "agent_summary"))
		assert.True(t, strings.Contains(result, "quality_score"))
	})
}

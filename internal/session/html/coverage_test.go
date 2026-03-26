package html

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RenderDiffHTML
// ---------------------------------------------------------------------------

func TestRenderDiffHTML(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		checks []string // substrings the output must contain
		absent []string // substrings the output must NOT contain
	}{
		{
			name:   "empty input",
			input:  "",
			checks: nil,
		},
		{
			name:  "added lines get diff-add class",
			input: "+added line",
			checks: []string{
				`class="diff-add"`,
				"added line",
			},
		},
		{
			name:  "removed lines get diff-remove class",
			input: "-removed line",
			checks: []string{
				`class="diff-remove"`,
				"removed line",
			},
		},
		{
			name:  "header lines get diff-header class",
			input: "@@ -1,3 +1,4 @@",
			checks: []string{
				`class="diff-header"`,
			},
		},
		{
			name:  "+++ prefix is NOT treated as an add",
			input: "+++ a/file.go",
			absent: []string{
				`class="diff-add"`,
			},
		},
		{
			name:  "--- prefix is NOT treated as a remove",
			input: "--- b/file.go",
			absent: []string{
				`class="diff-remove"`,
			},
		},
		{
			name:  "context line has no class wrapper",
			input: " unchanged context line",
			absent: []string{
				`class="diff-`,
			},
		},
		{
			name:  "multi-line diff",
			input: "@@ -1,2 +1,3 @@\n context\n+added\n-removed",
			checks: []string{
				`class="diff-header"`,
				`class="diff-add"`,
				`class="diff-remove"`,
			},
		},
		{
			name:   "HTML special chars are escaped",
			input:  "+<script>alert('xss')</script>",
			checks: []string{"&lt;script&gt;"},
			absent: []string{"<script>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := string(RenderDiffHTML(tt.input))
			for _, s := range tt.checks {
				assert.Contains(t, result, s)
			}
			for _, s := range tt.absent {
				assert.NotContains(t, result, s)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsDiffOutput
// ---------------------------------------------------------------------------

func TestIsDiffOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"plain text", "just some normal output", false},
		{"@@ header", "@@ -1,3 +1,4 @@\n context", true},
		{"+++ file header", "+++ a/file.go", true},
		{"--- file header", "--- b/file.go", true},
		{"added lines only", "+added\n+another", true},
		{"removed lines only", "-removed\n-another", true},
		{"single hyphen word", "non-diff text", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsDiffOutput(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// ComputeFallbackDuration
// ---------------------------------------------------------------------------

func TestComputeFallbackDuration(t *testing.T) {
	t1 := time.Date(2026, 1, 20, 14, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 20, 14, 5, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 20, 14, 10, 0, 0, time.UTC)

	tests := []struct {
		name         string
		messages     []MessageView
		wantDuration time.Duration
		wantFirst    time.Time
		wantLast     time.Time
	}{
		{
			name:         "empty messages",
			messages:     nil,
			wantDuration: 0,
		},
		{
			name: "all zero timestamps",
			messages: []MessageView{
				{ID: 1, Type: "user"},
				{ID: 2, Type: "assistant"},
			},
			wantDuration: 0,
		},
		{
			name: "single message",
			messages: []MessageView{
				{ID: 1, Type: "user", Timestamp: t1},
			},
			wantDuration: 0,
			wantFirst:    t1,
			wantLast:     t1,
		},
		{
			name: "three messages spanning 10 minutes",
			messages: []MessageView{
				{ID: 1, Type: "user", Timestamp: t1},
				{ID: 2, Type: "assistant", Timestamp: t2},
				{ID: 3, Type: "user", Timestamp: t3},
			},
			wantDuration: 10 * time.Minute,
			wantFirst:    t1,
			wantLast:     t3,
		},
		{
			name: "skips zero timestamps",
			messages: []MessageView{
				{ID: 1, Type: "user", Timestamp: t1},
				{ID: 2, Type: "assistant"}, // zero
				{ID: 3, Type: "user", Timestamp: t3},
			},
			wantDuration: 10 * time.Minute,
			wantFirst:    t1,
			wantLast:     t3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dur, first, last := ComputeFallbackDuration(tt.messages)
			assert.Equal(t, tt.wantDuration, dur)
			if !tt.wantFirst.IsZero() {
				assert.Equal(t, tt.wantFirst, first)
			}
			if !tt.wantLast.IsZero() {
				assert.Equal(t, tt.wantLast, last)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// escapeHTML
// ---------------------------------------------------------------------------

func TestEscapeHTML(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain text", "plain text"},
		{"<b>bold</b>", "&lt;b&gt;bold&lt;/b&gt;"},
		{`"quoted"`, "&quot;quoted&quot;"},
		{"it's", "it&#39;s"},
		{"a & b", "a &amp; b"},
		{"", ""},
		{"<script>alert('xss')</script>", "&lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, escapeHTML(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// truncateOutput
// ---------------------------------------------------------------------------

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string unchanged", "hello", 100, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"truncated with ellipsis", "hello world", 8, "hello..."},
		{"empty string", "", 10, ""},
		{"very small max", "abcdef", 4, "a..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncateOutput(tt.input, tt.maxLen))
		})
	}
}

// ---------------------------------------------------------------------------
// ToolCategory
// ---------------------------------------------------------------------------

func TestToolCategory(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Read", "read"},
		{"Glob", "read"},
		{"Grep", "read"},
		{"Edit", "edit"},
		{"Write", "edit"},
		{"MultiEdit", "edit"},
		{"NotebookEdit", "edit"},
		{"Bash", "exec"},
		{"Execute", "exec"},
		{"WebSearch", "search"},
		{"WebFetch", "search"},
		{"Task", "agent"},
		{"SomeUnknownTool", "other"},
		{"", "other"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, ToolCategory(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// formatAgentName
// ---------------------------------------------------------------------------

func TestFormatAgentName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-code", "Claude Code"},
		{"claudecode", "Claude Code"},
		{"claude", "Claude"},
		{"anthropic", "Claude"},
		{"cursor", "Cursor"},
		{"copilot", "GitHub Copilot"},
		{"github-copilot", "GitHub Copilot"},
		{"some-custom-agent", "Some Custom Agent"},
		{"snake_case_agent", "Snake Case Agent"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, formatAgentName(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// FormatToolInputCompact
// ---------------------------------------------------------------------------

func TestFormatToolInputCompact(t *testing.T) {
	tests := []struct {
		name   string
		tool   *ToolCallView
		checks []string
		absent []string
	}{
		{
			name:   "nil tool",
			tool:   nil,
			checks: nil,
		},
		{
			name:   "empty name defaults to Tool",
			tool:   &ToolCallView{},
			checks: []string{"Tool", "&gt;_"},
		},
		{
			name:   "Bash with JSON command",
			tool:   &ToolCallView{Name: "Bash", Input: `{"command":"git status"}`},
			checks: []string{"Bash", "git status", "&gt;_"},
		},
		{
			name:   "Read with JSON file_path",
			tool:   &ToolCallView{Name: "Read", Input: `{"file_path":"/Users/dev/project/main.go"}`},
			checks: []string{"Read", "project/main.go"},
		},
		{
			name:   "Edit with diff stats",
			tool:   &ToolCallView{Name: "Edit", Input: `{"file_path":"/home/dev/src/handler.go"}`, Output: "+line1\n+line2\n-old"},
			checks: []string{"Edit", "src/handler.go", "+2 / -1 lines"},
		},
		{
			name:   "Write with file path",
			tool:   &ToolCallView{Name: "Write", Input: `{"file_path":"/Users/dev/project/new.go"}`},
			checks: []string{"Write", "project/new.go"},
		},
		{
			name:   "Grep with pattern and path",
			tool:   &ToolCallView{Name: "Grep", Input: `{"pattern":"func main","path":"/Users/dev/src"}`},
			checks: []string{"Grep", "func main", "src"},
		},
		{
			name:   "Grep with only pattern",
			tool:   &ToolCallView{Name: "Grep", Input: `{"pattern":"TODO"}`},
			checks: []string{"Grep", "TODO"},
		},
		{
			name:   "Glob with pattern",
			tool:   &ToolCallView{Name: "Glob", Input: `{"pattern":"**/*.go"}`},
			checks: []string{"Glob", "**/*.go"},
		},
		{
			name:   "unknown tool with JSON falls back to first string value",
			tool:   &ToolCallView{Name: "CustomTool", Input: `{"arg":"some value"}`},
			checks: []string{"CustomTool", "some value"},
		},
		{
			name:   "unknown tool with non-JSON input shows name only",
			tool:   &ToolCallView{Name: "CustomTool", Input: "not json"},
			checks: []string{"CustomTool"},
		},
		{
			name:   "MultiEdit with file path",
			tool:   &ToolCallView{Name: "MultiEdit", Input: `{"file_path":"/Users/dev/project/api.go"}`, Output: "+new\n-old"},
			checks: []string{"MultiEdit", "project/api.go", "+1 / -1 lines"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := string(FormatToolInputCompact(tt.tool))
			for _, s := range tt.checks {
				assert.Contains(t, result, s, "expected %q in output", s)
			}
			for _, s := range tt.absent {
				assert.NotContains(t, result, s)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatValue
// ---------------------------------------------------------------------------

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"float", 3.14, "3.14"},
		{"nil", nil, "<nil>"},
		{"bool", true, "true"},
		{
			"map",
			map[string]any{"key": "val"},
			"{\n  \"key\": \"val\"\n}",
		},
		{
			"slice",
			[]any{"a", "b"},
			"[\n  \"a\",\n  \"b\"\n]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatValue(tt.val)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// generateTitle
// ---------------------------------------------------------------------------

func TestGenerateTitle(t *testing.T) {
	tests := []struct {
		name string
		sess *session.StoredSession
		want string
	}{
		{
			name: "with agent type",
			sess: &session.StoredSession{Meta: &session.StoreMeta{AgentType: "claude-code"}},
			want: "claude-code Session",
		},
		{
			name: "with filename, no agent type",
			sess: &session.StoredSession{
				Meta: &session.StoreMeta{},
				Info: session.SessionInfo{Filename: "raw.jsonl"},
			},
			want: "raw",
		},
		{
			name: "no agent type, no filename",
			sess: &session.StoredSession{Meta: &session.StoreMeta{}},
			want: "Agent Session",
		},
		{
			name: "nil meta with filename",
			sess: &session.StoredSession{
				Info: session.SessionInfo{Filename: "session-2026.jsonl"},
			},
			want: "session-2026",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, generateTitle(tt.sess))
		})
	}
}

// ---------------------------------------------------------------------------
// GenerateWithSummary
// ---------------------------------------------------------------------------

func TestGenerateWithSummary(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := &session.StoredSession{
		Meta: &session.StoreMeta{
			AgentType: "claude-code",
			Username:  "testuser",
		},
		Entries: []map[string]any{
			{"type": "user", "content": "Implement the new feature", "ts": "2026-01-20T14:00:00Z"},
			{"type": "assistant", "content": "Done implementing the feature.", "ts": "2026-01-20T14:05:00Z"},
		},
	}

	summary := &session.SummarizeResponse{
		Title:      "Feature Implementation Session",
		Summary:    "Implemented a new feature for user profiles.",
		KeyActions: []string{"Created handler", "Added tests"},
		Outcome:    "success",
		TopicsFound: []string{"api", "testing"},
		ChapterTitles: []string{"Planning & Execution"},
		AhaMoments: []session.AhaMoment{
			{Seq: 1, Role: "user", Type: "question", Highlight: "key question", Why: "set direction"},
		},
		SageoxInsights: []session.SageoxInsight{
			{Seq: 2, Topic: "go-patterns", Insight: "used idiomatic patterns", Impact: "cleaner code"},
		},
	}

	htmlBytes, err := gen.GenerateWithSummary(sess, summary)
	require.NoError(t, err)

	htmlStr := string(htmlBytes)
	assert.Contains(t, htmlStr, "Feature Implementation Session", "title from summary")
	assert.Contains(t, htmlStr, "Implemented a new feature", "summary text")
	assert.Contains(t, htmlStr, "Created handler", "key action")
	assert.Contains(t, htmlStr, "success", "outcome")
	assert.Contains(t, htmlStr, "<!DOCTYPE html>")
}

func TestGenerateWithSummary_NilSession(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	_, err = gen.GenerateWithSummary(nil, nil)
	assert.Error(t, err)
}

func TestGenerateWithSummary_NilSummary(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := &session.StoredSession{
		Meta:    &session.StoreMeta{AgentType: "claude-code"},
		Entries: []map[string]any{},
	}

	htmlBytes, err := gen.GenerateWithSummary(sess, nil)
	require.NoError(t, err)
	assert.Contains(t, string(htmlBytes), "<!DOCTYPE html>")
}

// ---------------------------------------------------------------------------
// GenerateToFileWithSummary
// ---------------------------------------------------------------------------

func TestGenerateToFileWithSummary(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "session.html")

	sess := &session.StoredSession{
		Meta: &session.StoreMeta{AgentType: "claude-code"},
		Entries: []map[string]any{
			{"type": "user", "content": "Hello", "ts": "2026-01-20T14:00:00Z"},
		},
	}

	summary := &session.SummarizeResponse{
		Title:   "Test Session",
		Summary: "A test session.",
		Outcome: "success",
	}

	err = gen.GenerateToFileWithSummary(sess, summary, outputPath)
	require.NoError(t, err)

	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "<!DOCTYPE html>")
	assert.Contains(t, string(content), "Test Session")
}

func TestGenerateToFileWithSummary_BadPath(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := &session.StoredSession{
		Meta:    &session.StoreMeta{},
		Entries: []map[string]any{},
	}

	err = gen.GenerateToFileWithSummary(sess, nil, "/nonexistent/dir/file.html")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// BuildTemplateData (exported wrapper)
// ---------------------------------------------------------------------------

func TestBuildTemplateData(t *testing.T) {
	sess := &session.StoredSession{
		Meta: &session.StoreMeta{
			AgentType: "claude-code",
			Username:  "dev",
			CreatedAt: time.Date(2026, 1, 20, 14, 0, 0, 0, time.UTC),
		},
		Entries: []map[string]any{
			{"type": "user", "content": "Fix the bug", "ts": "2026-01-20T14:00:01Z"},
			{"type": "assistant", "content": "Fixed it.", "ts": "2026-01-20T14:01:00Z"},
		},
		Footer: map[string]any{
			"closed_at": "2026-01-20T14:30:00Z",
		},
	}

	summary := &session.SummarizeResponse{
		Title:       "Bug Fix Session",
		Summary:     "Fixed a critical bug.",
		KeyActions:  []string{"Identified root cause", "Applied fix"},
		Outcome:     "success",
		TopicsFound: []string{"debugging"},
		Diagrams:    []string{"graph LR; A-->B"},
	}

	data := BuildTemplateData(sess, summary)
	require.NotNil(t, data)

	assert.Equal(t, "Bug Fix Session", data.Title)
	assert.NotNil(t, data.Summary)
	assert.Equal(t, "Fixed a critical bug.", data.Summary.Text)
	assert.Equal(t, []string{"Identified root cause", "Applied fix"}, data.Summary.KeyActions)
	assert.Equal(t, []string{"debugging"}, data.Summary.TopicsFound)
	assert.Equal(t, []string{"graph LR; A-->B"}, data.Summary.Diagrams)

	assert.NotNil(t, data.Metadata)
	assert.Equal(t, "dev", data.Metadata.Username)
	assert.Equal(t, "claude-code", data.Metadata.AgentType)
	assert.False(t, data.Metadata.EndedAt.IsZero(), "end time should be parsed from footer")

	assert.NotNil(t, data.Statistics)
	assert.Equal(t, 2, data.Statistics.TotalMessages)
	assert.Equal(t, 1, data.Statistics.UserMessages)

	assert.NotEmpty(t, data.Messages)
	assert.NotEmpty(t, data.Chapters)
}

// ---------------------------------------------------------------------------
// isRealUserMessage edge cases
// ---------------------------------------------------------------------------

func TestIsRealUserMessage(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"whitespace only", "   \n\t  ", false},
		{"slash command", "/clear", false},
		{"slash command with leading tag", "<p>/ox-session-start</p>", false},
		{"system reminder", "some <system-reminder>noise</system-reminder>", false},
		{"escaped system reminder", "&lt;system-reminder&gt;stuff&lt;/system-reminder&gt;", false},
		{"ox-hash marker", "<!-- ox-hash: abc123 -->content", false},
		{"escaped ox-hash", "&lt;!-- ox-hash: abc123 --&gt;", false},
		{"real message", "Fix the login bug", true},
		{"message with html tags", "<p>Fix the login bug</p>", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := MessageView{Content: template.HTML(tt.content)}
			assert.Equal(t, tt.want, isRealUserMessage(msg))
		})
	}
}

// ---------------------------------------------------------------------------
// mergeShortAssistantBlocks
// ---------------------------------------------------------------------------

func TestMergeShortAssistantBlocks(t *testing.T) {
	wb1 := &WorkBlockView{
		Messages:   []MessageView{{ID: 1, Type: "tool", ToolCall: &ToolCallView{Name: "Read"}}},
		ToolCounts: map[string]int{"Read": 1},
		TotalTools: 1,
	}
	wb2 := &WorkBlockView{
		Messages:   []MessageView{{ID: 3, Type: "tool", ToolCall: &ToolCallView{Name: "Edit"}}},
		ToolCounts: map[string]int{"Edit": 1},
		TotalTools: 1,
		HasEdits:   true,
	}

	t.Run("short assistant between work blocks gets merged", func(t *testing.T) {
		items := []ChapterItem{
			{IsWorkBlock: true, WorkBlock: wb1},
			{Message: &MessageView{ID: 2, Type: "assistant", Content: "ok"}},
			{IsWorkBlock: true, WorkBlock: wb2},
		}

		// need fresh copies since merge mutates
		wb1Copy := *wb1
		wb1Copy.ToolCounts = map[string]int{"Read": 1}
		wb1Copy.Messages = append([]MessageView{}, wb1.Messages...)
		items[0].WorkBlock = &wb1Copy

		wb2Copy := *wb2
		wb2Copy.ToolCounts = map[string]int{"Edit": 1}
		wb2Copy.Messages = append([]MessageView{}, wb2.Messages...)
		items[2].WorkBlock = &wb2Copy

		result := mergeShortAssistantBlocks(items)
		assert.Len(t, result, 1, "should merge into single work block")
		assert.True(t, result[0].IsWorkBlock)
		assert.Equal(t, 2, result[0].WorkBlock.TotalTools)
		assert.True(t, result[0].WorkBlock.HasEdits)
	})

	t.Run("long assistant between work blocks stays separate", func(t *testing.T) {
		longContent := template.HTML(strings.Repeat("x", 250))
		items := []ChapterItem{
			{IsWorkBlock: true, WorkBlock: &WorkBlockView{TotalTools: 1, ToolCounts: map[string]int{"Read": 1}}},
			{Message: &MessageView{ID: 2, Type: "assistant", Content: longContent}},
			{IsWorkBlock: true, WorkBlock: &WorkBlockView{TotalTools: 1, ToolCounts: map[string]int{"Edit": 1}}},
		}

		result := mergeShortAssistantBlocks(items)
		assert.Len(t, result, 3, "long assistant should not be merged")
	})

	t.Run("short assistant not between two work blocks stays", func(t *testing.T) {
		items := []ChapterItem{
			{Message: &MessageView{ID: 1, Type: "user", Content: "hi"}},
			{Message: &MessageView{ID: 2, Type: "assistant", Content: "ok"}},
			{IsWorkBlock: true, WorkBlock: &WorkBlockView{TotalTools: 1, ToolCounts: map[string]int{"Read": 1}}},
		}

		result := mergeShortAssistantBlocks(items)
		assert.Len(t, result, 3, "not between two work blocks, so no merge")
	})
}

// ---------------------------------------------------------------------------
// isTestCommand
// ---------------------------------------------------------------------------

func TestIsTestCommand(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"go test ./...", true},
		{"pytest -v tests/", true},
		{"jest --coverage", true},
		{"vitest run", true},
		{"make test", true},
		{"npm test", true},
		{"npm run test", true},
		{"yarn test", true},
		{"pnpm test", true},
		{"cargo test", true},
		{"mix test", true},
		{"rspec spec/", true},
		{"phpunit tests/", true},
		{"go build ./...", false},
		{"git status", false},
		{"echo test", false},
		{"ls -la", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, isTestCommand(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// autoChapterTitle - testing/execution branches
// ---------------------------------------------------------------------------

func TestAutoChapterTitle_Testing(t *testing.T) {
	// testing: testCount > bashCount/2 AND readCount < editCount AND editCount < bashCount
	items := []ChapterItem{
		{Message: &MessageView{Type: "user", Content: "run tests"}},
		{IsWorkBlock: true, WorkBlock: &WorkBlockView{
			ToolCounts: map[string]int{"Bash": 6, "Edit": 2, "Read": 1},
			TotalTools: 9,
			Messages: []MessageView{
				{ToolCall: &ToolCallView{Name: "Bash", Input: "go test ./..."}},
				{ToolCall: &ToolCallView{Name: "Bash", Input: "go test ./pkg/..."}},
				{ToolCall: &ToolCallView{Name: "Bash", Input: "go test -v ./internal/..."}},
				{ToolCall: &ToolCallView{Name: "Bash", Input: "go test -race ./..."}},
				{ToolCall: &ToolCallView{Name: "Bash", Input: "git status"}},
				{ToolCall: &ToolCallView{Name: "Bash", Input: "git diff"}},
				{ToolCall: &ToolCallView{Name: "Edit"}},
				{ToolCall: &ToolCallView{Name: "Edit"}},
				{ToolCall: &ToolCallView{Name: "Read"}},
			},
		}},
	}

	title := autoChapterTitle(items, 2)
	assert.Equal(t, "Testing", title)
}

func TestAutoChapterTitle_Execution(t *testing.T) {
	// bash dominates with no significant test commands
	items := []ChapterItem{
		{Message: &MessageView{Type: "user", Content: "deploy"}},
		{IsWorkBlock: true, WorkBlock: &WorkBlockView{
			ToolCounts: map[string]int{"Bash": 10, "Read": 2, "Edit": 1},
			TotalTools: 13,
			Messages: []MessageView{
				{ToolCall: &ToolCallView{Name: "Bash", Input: "make build"}},
				{ToolCall: &ToolCallView{Name: "Bash", Input: "docker build ."}},
			},
		}},
	}

	title := autoChapterTitle(items, 2)
	assert.Equal(t, "Execution", title)
}

func TestAutoChapterTitle_TurnN(t *testing.T) {
	// no tools, not chapter 1
	items := []ChapterItem{
		{Message: &MessageView{Type: "user", Content: "question"}},
		{Message: &MessageView{Type: "assistant", Content: "answer"}},
	}

	title := autoChapterTitle(items, 5)
	assert.Equal(t, "Turn 5", title)
}

// ---------------------------------------------------------------------------
// FormatWorkBlockSummary edge cases
// ---------------------------------------------------------------------------

func TestFormatWorkBlockSummary_NilBlock(t *testing.T) {
	assert.Equal(t, "", FormatWorkBlockSummary(nil))
}

func TestFormatWorkBlockSummary_SystemMessagesOnly(t *testing.T) {
	wb := &WorkBlockView{
		TotalTools: 0,
		Messages: []MessageView{
			{Type: "system"},
			{Type: "system"},
		},
	}
	assert.Equal(t, "2 system messages", FormatWorkBlockSummary(wb))
}

func TestFormatWorkBlockSummary_SingleSystemMessage(t *testing.T) {
	wb := &WorkBlockView{
		TotalTools: 0,
		Messages: []MessageView{
			{Type: "system"},
		},
	}
	assert.Equal(t, "1 system message", FormatWorkBlockSummary(wb))
}

func TestFormatWorkBlockSummary_EmptyBlock(t *testing.T) {
	wb := &WorkBlockView{
		TotalTools: 0,
		Messages:   []MessageView{},
	}
	assert.Equal(t, "", FormatWorkBlockSummary(wb))
}

// ---------------------------------------------------------------------------
// ExtractFilePathFromInput edge cases
// ---------------------------------------------------------------------------

func TestExtractFilePathFromInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"json with file_path", `{"file_path":"/src/main.go"}`, "/src/main.go"},
		{"json with path fallback", `{"path":"/src/main.go"}`, "/src/main.go"},
		{"json with neither", `{"command":"ls"}`, ""},
		{"non-json", "not json at all", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractFilePathFromInput(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// RenderMarkdown edge cases
// ---------------------------------------------------------------------------

func TestRenderMarkdown_Empty(t *testing.T) {
	assert.Equal(t, template.HTML(""), RenderMarkdown(""))
}

func TestRenderMarkdown_InlineFormatting(t *testing.T) {
	result := string(RenderMarkdown("**bold** and *italic*"))
	assert.Contains(t, result, "<strong>bold</strong>")
	assert.Contains(t, result, "<em>italic</em>")
}

func TestRenderMarkdown_Links(t *testing.T) {
	result := string(RenderMarkdown("[example](https://example.com)"))
	assert.Contains(t, result, `<a href="https://example.com"`)
	assert.Contains(t, result, "example</a>")
}

func TestRenderMarkdown_Lists(t *testing.T) {
	result := string(RenderMarkdown("- item one\n- item two\n- item three"))
	assert.Contains(t, result, "<ul>")
	assert.Contains(t, result, "<li>")
	assert.Contains(t, result, "item one")
}

func TestRenderMarkdown_GFMTable(t *testing.T) {
	input := "| Col A | Col B |\n|-------|-------|\n| 1     | 2     |"
	result := string(RenderMarkdown(input))
	assert.Contains(t, result, "<table>")
	assert.Contains(t, result, "Col A")
}

func TestRenderMarkdown_GFMStrikethrough(t *testing.T) {
	result := string(RenderMarkdown("~~deleted~~"))
	assert.Contains(t, result, "<del>deleted</del>")
}

func TestRenderMarkdown_SpecialChars(t *testing.T) {
	// angle brackets in non-HTML context should be escaped or handled safely
	result := string(RenderMarkdown("Use `map[string]any` for generic maps"))
	assert.Contains(t, result, "<code>")
	assert.Contains(t, result, "map[string]any")
}

// ---------------------------------------------------------------------------
// shortenPath
// ---------------------------------------------------------------------------

func TestShortenPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/dev/project/main.go", "project/main.go"},
		{"/home/dev/project/main.go", "project/main.go"},
		{"/var/data/file.txt", "/var/data/file.txt"},
		{"relative/path.go", "relative/path.go"},
		{"/Users/dev/a", "a"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, shortenPath(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// reclassifyByContent - unicode prefix detection
// ---------------------------------------------------------------------------

func TestReclassifyByContent_ToolPrefixes(t *testing.T) {
	// U+23FA (black circle for record) used by Claude Code for tool display
	got := reclassifyByContent("user", "\u23fa Read file.go", "")
	assert.Equal(t, "tool", got)

	// U+23BF used by Claude Code for tool result display
	got = reclassifyByContent("user", "\u23bf result output", "")
	assert.Equal(t, "tool", got)
}

func TestReclassifyByContent_SkipsNonUser(t *testing.T) {
	// reclassification only applies to "user" type messages
	got := reclassifyByContent("system", "\u23fa some content", "")
	assert.Equal(t, "system", got)
}

// ---------------------------------------------------------------------------
// buildMessageView - tool entry paths via nested data
// ---------------------------------------------------------------------------

func TestBuildMessageView_ToolCallFormat(t *testing.T) {
	entry := map[string]any{
		"type":      "tool_call",
		"timestamp": "2026-01-20T14:00:00Z",
		"data": map[string]any{
			"tool_name": "Bash",
			"input":     `{"command":"make build"}`,
		},
	}

	msg := buildMessageView(1, entry, "User", "Assistant")
	assert.Equal(t, "tool", msg.Type)
	assert.NotNil(t, msg.ToolCall)
	assert.Equal(t, "Bash", msg.ToolCall.Name)
}

func TestBuildMessageView_ToolResultFormat(t *testing.T) {
	entry := map[string]any{
		"type":      "tool_result",
		"timestamp": "2026-01-20T14:00:00Z",
		"data": map[string]any{
			"tool_name": "Read",
			"output":    "package main\nfunc main() {}",
		},
	}

	msg := buildMessageView(2, entry, "User", "Assistant")
	assert.Equal(t, "tool", msg.Type)
	assert.NotNil(t, msg.ToolCall)
	assert.Equal(t, "Read", msg.ToolCall.Name)
}

func TestBuildMessageView_RootLevelTool(t *testing.T) {
	entry := map[string]any{
		"type":        "tool",
		"tool_name":   "Grep",
		"tool_input":  `{"pattern":"func main"}`,
		"tool_output": "main.go:10:func main() {",
		"ts":          "2026-01-20T14:00:00Z",
	}

	msg := buildMessageView(3, entry, "User", "Assistant")
	assert.Equal(t, "tool", msg.Type)
	assert.NotNil(t, msg.ToolCall)
	assert.Equal(t, "Grep", msg.ToolCall.Name)
	assert.Contains(t, msg.ToolCall.Output, "main.go")
}

func TestBuildMessageView_NestedGenericContent(t *testing.T) {
	// a "data" entry type that is not message/tool_call/tool_result
	// should still extract content from data.content
	entry := map[string]any{
		"type":      "custom_type",
		"timestamp": "2026-01-20T14:00:00Z",
		"data": map[string]any{
			"content": "some generic content",
		},
	}

	msg := buildMessageView(1, entry, "User", "Assistant")
	assert.Contains(t, string(msg.Content), "some generic content")
}

// ---------------------------------------------------------------------------
// CountDiffLines
// ---------------------------------------------------------------------------

func TestCountDiffLines(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		added   int
		removed int
	}{
		{"empty", "", 0, 0},
		{"added only", "+line1\n+line2", 2, 0},
		{"removed only", "-line1\n-line2", 0, 2},
		{"mixed", "+added\n-removed\n context", 1, 1},
		{"+++ is not counted as add", "+++ a/file.go", 0, 0},
		{"--- is not counted as remove", "--- b/file.go", 0, 0},
		{"full diff", "--- a/f.go\n+++ b/f.go\n@@ -1 +1 @@\n-old\n+new", 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, r := CountDiffLines(tt.input)
			assert.Equal(t, tt.added, a, "added")
			assert.Equal(t, tt.removed, r, "removed")
		})
	}
}

// ---------------------------------------------------------------------------
// FormatToolSummary
// ---------------------------------------------------------------------------

func TestFormatToolSummary(t *testing.T) {
	tests := []struct {
		name string
		tool *ToolCallView
		want string
	}{
		{"nil tool", nil, ""},
		{"empty name", &ToolCallView{}, ""},
		{"name only", &ToolCallView{Name: "Bash"}, "Bash"},
		{
			"with file path",
			&ToolCallView{Name: "Read", Input: `{"file_path":"/src/main.go"}`},
			"Read(main.go)",
		},
		{
			"with diff stats",
			&ToolCallView{Name: "Edit", Input: `{"file_path":"/src/handler.go"}`, Output: "+new\n-old"},
			"Edit(handler.go) \u2014 +1 / -1 lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatToolSummary(tt.tool))
		})
	}
}

// ---------------------------------------------------------------------------
// stripPreamble
// ---------------------------------------------------------------------------

func TestStripPreamble_AllNoise(t *testing.T) {
	messages := []MessageView{
		{Type: "user", Content: "/ox-session-start"},
		{Type: "system", Content: "system context"},
		{Type: "user", Content: "<!-- ox-hash: abc -->skill content"},
	}

	// no real user message found, returns original
	result := stripPreamble(messages)
	assert.Equal(t, messages, result)
}

func TestStripPreamble_FindsRealMessage(t *testing.T) {
	messages := []MessageView{
		{Type: "user", Content: "/clear"},
		{Type: "system", Content: "context"},
		{Type: "user", Content: "Fix the bug please"},
		{Type: "assistant", Content: "On it"},
	}

	result := stripPreamble(messages)
	assert.Len(t, result, 2)
	assert.Contains(t, string(result[0].Content), "Fix the bug")
}

// ---------------------------------------------------------------------------
// TemplateFuncMap
// ---------------------------------------------------------------------------

func TestTemplateFuncMap_AddFunc(t *testing.T) {
	fm := TemplateFuncMap()
	addFn, ok := fm["add"]
	require.True(t, ok, "add function should exist")

	fn := addFn.(func(int, int) int)
	assert.Equal(t, 5, fn(2, 3))
	assert.Equal(t, 0, fn(-1, 1))
}

func TestTemplateFuncMap_AllFunctionsPresent(t *testing.T) {
	fm := TemplateFuncMap()
	expected := []string{"add", "renderDiffHTML", "isDiffOutput", "toolCategory"}
	for _, name := range expected {
		_, ok := fm[name]
		assert.True(t, ok, "expected function %q in TemplateFuncMap", name)
	}
}

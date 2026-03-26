package cli

import (
	"strings"
	"testing"
)

func TestParseSlogLine_UnterminatedQuote(t *testing.T) {
	// quoted msg that never closes should use everything after opening quote as msg
	line := `time=2026-03-09T18:27:44.282-07:00 level=INFO msg="unterminated message`
	got, ok := ParseSlogLine(line)
	if !ok {
		t.Fatal("expected ParseSlogLine to return ok=true for unterminated quote")
	}
	if got.Message != "unterminated message" {
		t.Errorf("Message = %q, want %q", got.Message, "unterminated message")
	}
	if got.Attrs != "" {
		t.Errorf("Attrs = %q, want empty", got.Attrs)
	}
}

func TestParseSlogLine_EmptyQuotedMessage(t *testing.T) {
	line := `time=2026-03-09T18:27:44.282-07:00 level=INFO msg="" key=val`
	got, ok := ParseSlogLine(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Message != "" {
		t.Errorf("Message = %q, want empty", got.Message)
	}
	if got.Attrs != "key=val" {
		t.Errorf("Attrs = %q, want %q", got.Attrs, "key=val")
	}
}

func TestParseSlogLine_MsgWithEqualsSign(t *testing.T) {
	// unquoted msg containing no spaces
	line := `time=2026-03-09T18:27:44.282-07:00 level=INFO msg=key=val extra=1`
	got, ok := ParseSlogLine(line)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// unquoted msg takes until first space
	if got.Message != "key=val" {
		t.Errorf("Message = %q, want %q", got.Message, "key=val")
	}
	if got.Attrs != "extra=1" {
		t.Errorf("Attrs = %q, want %q", got.Attrs, "extra=1")
	}
}

func TestFormatSlogLine_AllLevels(t *testing.T) {
	levels := []string{"DEBUG", "INFO", "WARN", "ERROR", "CUSTOM"}
	for _, level := range levels {
		line := `time=2026-03-09T18:27:44.282-07:00 level=` + level + ` msg="test"`
		got := FormatSlogLine(line)
		if !strings.Contains(got, level) {
			t.Errorf("FormatSlogLine with level %s should contain level in output, got: %q", level, got)
		}
		if !strings.Contains(got, "test") {
			t.Errorf("FormatSlogLine with level %s should contain message in output, got: %q", level, got)
		}
	}
}

func TestFormatSlogLine_WithAttrs(t *testing.T) {
	line := `time=2026-03-09T18:27:44.282-07:00 level=INFO msg="hello" foo=bar baz=42`
	got := FormatSlogLine(line)
	if !strings.Contains(got, "hello") {
		t.Errorf("should contain message, got: %q", got)
	}
	if !strings.Contains(got, "foo=bar baz=42") {
		t.Errorf("should contain attrs, got: %q", got)
	}
}

func TestFormatSlogLine_NoAttrs(t *testing.T) {
	line := `time=2026-03-09T18:27:44.282-07:00 level=INFO msg="hello"`
	got := FormatSlogLine(line)
	if !strings.Contains(got, "hello") {
		t.Errorf("should contain message, got: %q", got)
	}
	// should not have trailing separator for attrs section
	if !strings.Contains(got, "18:27:44.282") {
		t.Errorf("should contain compact timestamp, got: %q", got)
	}
}

func TestCompactTimestamp_Formats(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "rfc3339 without fractional",
			raw:  "2026-01-15T09:30:00Z",
			want: "09:30:00.000",
		},
		{
			name: "rfc3339 with microseconds",
			raw:  "2026-01-15T09:30:00.123456Z",
			want: "09:30:00.123",
		},
		{
			name: "positive UTC offset",
			raw:  "2026-01-15T09:30:00.500+05:30",
			want: "09:30:00.500",
		},
		{
			name: "empty string falls back",
			raw:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compactTimestamp(tt.raw)
			if got != tt.want {
				t.Errorf("compactTimestamp(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

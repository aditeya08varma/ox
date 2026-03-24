package facts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFacts_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	header := FileHeader{Meta: FileMeta{
		SchemaVersion: "2",
		SourceType:    SourceGitHub,
		RecordedAt:    "2026-03-23T14:00:00Z",
	}}
	facts := []Fact{
		{Headline: "Adopted rate limiting", Summary: "Token bucket at 100 req/min", SourceType: SourceGitHub, Timestamp: "2026-03-23T14:00:00Z", Category: CategoryShip},
		{Headline: "Fixed auth bug", SourceType: SourceGitHub, Timestamp: "2026-03-23T15:00:00Z"},
	}

	if err := WriteFacts(path, header, facts); err != nil {
		t.Fatal(err)
	}

	// Read back and verify
	gotHeader, gotFacts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}

	if gotHeader.Meta.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want %q", gotHeader.Meta.SchemaVersion, "2")
	}
	if gotHeader.Meta.SourceType != SourceGitHub {
		t.Errorf("source_type = %q, want %q", gotHeader.Meta.SourceType, SourceGitHub)
	}
	if len(gotFacts) != 2 {
		t.Fatalf("got %d facts, want 2", len(gotFacts))
	}
	if gotFacts[0].Headline != "Adopted rate limiting" {
		t.Errorf("fact[0].Headline = %q", gotFacts[0].Headline)
	}
	if gotFacts[0].Category != CategoryShip {
		t.Errorf("fact[0].Category = %q, want %q", gotFacts[0].Category, CategoryShip)
	}
	if gotFacts[1].Headline != "Fixed auth bug" {
		t.Errorf("fact[1].Headline = %q", gotFacts[1].Headline)
	}
	if gotFacts[1].Category != "" {
		t.Errorf("fact[1].Category = %q, want empty", gotFacts[1].Category)
	}
}

func TestWriteFacts_EmptyFacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")

	header := FileHeader{Meta: FileMeta{
		SchemaVersion: "2",
		SourceType:    SourceObservation,
		RecordedAt:    "2026-03-23T14:00:00Z",
	}}

	if err := WriteFacts(path, header, nil); err != nil {
		t.Fatal(err)
	}

	gotHeader, gotFacts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Meta.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want %q", gotHeader.Meta.SchemaVersion, "2")
	}
	if len(gotFacts) != 0 {
		t.Errorf("got %d facts, want 0", len(gotFacts))
	}
}

func TestWriteFacts_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "test.jsonl")

	header := FileHeader{Meta: FileMeta{SchemaVersion: "2", SourceType: SourceObservation, RecordedAt: "2026-03-23T14:00:00Z"}}
	if err := WriteFacts(path, header, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestReadFacts_V2Format(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v2.jsonl")

	content := `{"_meta":{"schema_version":"2","source_type":"discussion","recorded_at":"2026-03-10T14:23:00Z"}}
{"headline":"Chose PostgreSQL","summary":"For analytics store","source_type":"discussion","timestamp":"2026-03-10T14:23:00Z","category":"decision"}
{"headline":"Token bucket selected","source_type":"discussion","timestamp":"2026-03-10T14:23:00Z","category":"decision","who":"Sarah"}
`
	_ = os.WriteFile(path, []byte(content), 0o644)

	header, facts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if header.Meta.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want %q", header.Meta.SchemaVersion, "2")
	}
	if header.Meta.SourceType != SourceDiscussion {
		t.Errorf("source_type = %q, want %q", header.Meta.SourceType, SourceDiscussion)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	if facts[0].Headline != "Chose PostgreSQL" {
		t.Errorf("fact[0].Headline = %q", facts[0].Headline)
	}
	if facts[1].Who != "Sarah" {
		t.Errorf("fact[1].Who = %q", facts[1].Who)
	}
}

func TestReadFacts_V1ObservationFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v1.jsonl")

	content := `{"schema_version":"1","recorded_at":"2026-03-01T12:00:00Z"}
{"content":"We decided to use PostgreSQL"}
{"content":"Auth module needs refactoring"}
`
	_ = os.WriteFile(path, []byte(content), 0o644)

	header, facts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if header.Meta.SchemaVersion != "1" {
		t.Errorf("schema_version = %q, want %q", header.Meta.SchemaVersion, "1")
	}
	if header.Meta.SourceType != SourceObservation {
		t.Errorf("source_type = %q, want %q", header.Meta.SourceType, SourceObservation)
	}
	if header.Meta.RecordedAt != "2026-03-01T12:00:00Z" {
		t.Errorf("recorded_at = %q", header.Meta.RecordedAt)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	if facts[0].Headline != "We decided to use PostgreSQL" {
		t.Errorf("fact[0].Headline = %q", facts[0].Headline)
	}
	if facts[0].SourceType != SourceObservation {
		t.Errorf("fact[0].SourceType = %q", facts[0].SourceType)
	}
	if facts[1].Headline != "Auth module needs refactoring" {
		t.Errorf("fact[1].Headline = %q", facts[1].Headline)
	}
}

func TestReadFacts_NoHeader_RawJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	// Legacy github facts format: no header, just fact lines
	content := `{"headline":"Adopted rate limiting","summary":"Token bucket","source_type":"github","source_ref":"https://github.com/org/repo/pull/152","timestamp":"2026-03-18T14:30:00Z"}
{"headline":"Fixed auth bug","source_type":"github","timestamp":"2026-03-19T10:00:00Z"}
`
	_ = os.WriteFile(path, []byte(content), 0o644)

	header, facts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	// No header detected
	if header.Meta.SchemaVersion != "" {
		t.Errorf("expected empty schema_version for raw JSONL, got %q", header.Meta.SchemaVersion)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	if facts[0].Headline != "Adopted rate limiting" {
		t.Errorf("fact[0].Headline = %q", facts[0].Headline)
	}
	if facts[0].SourceRef != "https://github.com/org/repo/pull/152" {
		t.Errorf("fact[0].SourceRef = %q", facts[0].SourceRef)
	}
}

func TestReadFacts_BlankLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blanks.jsonl")

	content := `{"_meta":{"schema_version":"2","source_type":"observation","recorded_at":"2026-03-01T12:00:00Z"}}

{"headline":"First","source_type":"observation","timestamp":"2026-03-01T12:00:00Z"}

{"headline":"Second","source_type":"observation","timestamp":"2026-03-01T12:00:00Z"}

`
	_ = os.WriteFile(path, []byte(content), 0o644)

	_, facts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
}

func TestReadFacts_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.jsonl")

	content := `{"_meta":{"schema_version":"2","source_type":"github","recorded_at":"2026-03-01T12:00:00Z"}}
{"headline":"Valid fact","source_type":"github","timestamp":"2026-03-01T12:00:00Z"}
this is not json
{"headline":"Another valid","source_type":"github","timestamp":"2026-03-01T13:00:00Z"}
`
	_ = os.WriteFile(path, []byte(content), 0o644)

	_, facts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2 (malformed line skipped)", len(facts))
	}
}

func TestReadFacts_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(path, []byte(""), 0o644)

	header, facts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if header.Meta.SchemaVersion != "" {
		t.Errorf("expected empty header for empty file")
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for empty file, got %d", len(facts))
	}
}

func TestParseFacts_V1MixedWithBlankLines(t *testing.T) {
	content := `{"schema_version":"1","recorded_at":"2026-03-01T12:00:00Z"}
{"content":"First obs"}

{"content":"Second obs"}
`
	header, facts, err := ParseFacts([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if header.Meta.SchemaVersion != "1" {
		t.Errorf("schema_version = %q", header.Meta.SchemaVersion)
	}
	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	if facts[0].Headline != "First obs" {
		t.Errorf("fact[0].Headline = %q", facts[0].Headline)
	}
}

func TestParseFactLine_V2Fact(t *testing.T) {
	line := `{"headline":"Test fact","summary":"Details","source_type":"github","timestamp":"2026-03-01T12:00:00Z","category":"ship"}`
	f, err := ParseFactLine([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if f.Headline != "Test fact" {
		t.Errorf("Headline = %q", f.Headline)
	}
	if f.Category != CategoryShip {
		t.Errorf("Category = %q", f.Category)
	}
}

func TestParseFactLine_LegacyContent(t *testing.T) {
	line := `{"content":"We chose PostgreSQL"}`
	f, err := ParseFactLine([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if f.Headline != "We chose PostgreSQL" {
		t.Errorf("Headline = %q, want %q", f.Headline, "We chose PostgreSQL")
	}
	if f.SourceType != SourceObservation {
		t.Errorf("SourceType = %q, want %q", f.SourceType, SourceObservation)
	}
}

func TestParseFactLine_EmptyJSON(t *testing.T) {
	_, err := ParseFactLine([]byte(`{}`))
	if err == nil {
		t.Error("expected error for empty JSON object")
	}
}

func TestParseFactLine_InvalidJSON(t *testing.T) {
	_, err := ParseFactLine([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestWriteReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.jsonl")

	header := FileHeader{Meta: FileMeta{
		SchemaVersion: SchemaVersion,
		SourceType:    SourceObservation,
		RecordedAt:    "2026-03-23T14:00:00Z",
	}}
	original := []Fact{
		{Headline: "Special chars: \"quotes\" and \\backslash", SourceType: SourceObservation, Timestamp: "2026-03-23T14:00:00Z"},
		{Headline: "Unicode: 日本語テスト", SourceType: SourceObservation, Timestamp: "2026-03-23T14:00:00Z"},
		{Headline: "Tabs\tand\nnewlines", SourceType: SourceObservation, Timestamp: "2026-03-23T14:00:00Z"},
	}

	if err := WriteFacts(path, header, original); err != nil {
		t.Fatal(err)
	}

	gotHeader, gotFacts, err := ReadFacts(path)
	if err != nil {
		t.Fatal(err)
	}

	if gotHeader.Meta.SchemaVersion != SchemaVersion {
		t.Errorf("header schema_version = %q", gotHeader.Meta.SchemaVersion)
	}
	if len(gotFacts) != len(original) {
		t.Fatalf("got %d facts, want %d", len(gotFacts), len(original))
	}
	for i := range original {
		if gotFacts[i].Headline != original[i].Headline {
			t.Errorf("fact[%d].Headline = %q, want %q", i, gotFacts[i].Headline, original[i].Headline)
		}
	}
}

func TestFactJSON_OmitsEmptyFields(t *testing.T) {
	f := Fact{
		Headline:   "Test",
		SourceType: SourceObservation,
		Timestamp:  "2026-03-01T12:00:00Z",
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, field := range []string{"summary", "rationale", "who", "source_ref", "category"} {
		if strings.Contains(s, field) {
			t.Errorf("JSON should omit empty %q field, got: %s", field, s)
		}
	}
}

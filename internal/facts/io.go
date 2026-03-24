package facts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteFacts writes a JSONL fact file with a header line followed by fact lines.
// Parent directories are created as needed.
func WriteFacts(path string, header FileHeader, facts []Fact) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	w := bufio.NewWriter(f)

	headerBytes, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal header: %w", err)
	}
	_, _ = w.Write(headerBytes)
	_ = w.WriteByte('\n')

	for _, fact := range facts {
		line, err := json.Marshal(fact)
		if err != nil {
			return fmt.Errorf("marshal fact: %w", err)
		}
		_, _ = w.Write(line)
		_ = w.WriteByte('\n')
	}

	if err := w.Flush(); err != nil {
		return err
	}
	closed = true
	return f.Close()
}

// legacyV1Header is the v1 observation header format (top-level, no _meta wrapper).
type legacyV1Header struct {
	SchemaVersion string `json:"schema_version"`
	RecordedAt    string `json:"recorded_at"`
}

// legacyObservation is the v1 observation content format.
type legacyObservation struct {
	Content string `json:"content"`
}

// ReadFacts reads a JSONL fact file and returns the header and facts.
// Handles three formats for backward compatibility:
//   - v2: _meta header wrapper + Fact lines
//   - v1 observation: top-level schema_version/recorded_at + {"content":"..."} lines
//   - Raw JSONL (no header): all lines parsed as facts (legacy github format)
//
// Blank lines and malformed JSON lines are skipped.
func ReadFacts(path string) (FileHeader, []Fact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileHeader{}, nil, err
	}
	return ParseFacts(data)
}

// ParseFacts parses JSONL fact data from bytes. See ReadFacts for format details.
func ParseFacts(data []byte) (FileHeader, []Fact, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() {
		return FileHeader{}, nil, nil // empty file
	}

	firstLine := strings.TrimSpace(scanner.Text())
	if firstLine == "" {
		return FileHeader{}, nil, nil
	}

	header, headerParsed := parseHeader([]byte(firstLine))

	var facts []Fact

	// If no header was detected, first line is a fact line.
	if !headerParsed {
		if f, err := ParseFactLine([]byte(firstLine)); err == nil {
			facts = append(facts, f)
		}
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		f, err := ParseFactLine([]byte(line))
		if err != nil {
			continue // skip malformed lines
		}
		facts = append(facts, f)
	}

	return header, facts, scanner.Err()
}

// parseHeader tries to parse a line as a v2 FileHeader or v1 legacyV1Header.
// Returns the header and true if parsing succeeded, or a zero header and false.
func parseHeader(line []byte) (FileHeader, bool) {
	// Try v2: {"_meta":{...}}
	var v2 FileHeader
	if err := json.Unmarshal(line, &v2); err == nil && v2.Meta.SchemaVersion != "" {
		return v2, true
	}

	// Try v1: {"schema_version":"1","recorded_at":"..."}
	var v1 legacyV1Header
	if err := json.Unmarshal(line, &v1); err == nil && v1.SchemaVersion != "" {
		return FileHeader{
			Meta: FileMeta{
				SchemaVersion: v1.SchemaVersion,
				SourceType:    SourceObservation,
				RecordedAt:    v1.RecordedAt,
			},
		}, true
	}

	return FileHeader{}, false
}

// ParseFactLine parses a single JSONL line into a Fact.
// Handles legacy observation format: {"content":"..."} is mapped to Fact{Headline: content}.
func ParseFactLine(line []byte) (Fact, error) {
	var f Fact
	if err := json.Unmarshal(line, &f); err != nil {
		return Fact{}, err
	}

	// If headline is populated, this is a v2 fact.
	if f.Headline != "" {
		return f, nil
	}

	// Try legacy observation format: {"content":"..."}
	var legacy legacyObservation
	if err := json.Unmarshal(line, &legacy); err == nil && legacy.Content != "" {
		return Fact{
			Headline:   legacy.Content,
			SourceType: SourceObservation,
		}, nil
	}

	return Fact{}, fmt.Errorf("no headline or content in fact line")
}

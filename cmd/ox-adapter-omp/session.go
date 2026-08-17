// session.go handles OMP transcript parsing and discovery.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/session/omppaths"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type sessionCandidate struct {
	path    string
	modTime time.Time
}

type ompRecord struct {
	Type      string      `json:"type"`
	Timestamp string      `json:"timestamp,omitempty"`
	Version   int         `json:"version,omitempty"`
	Model     string      `json:"model,omitempty"`
	ModelID   string      `json:"modelId,omitempty"`
	Message   *ompMessage `json:"message,omitempty"`
	CWD       string      `json:"cwd,omitempty"`
}

type ompMessage struct {
	Role       string     `json:"role"`
	Content    []ompBlock `json:"content"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	ToolName   string     `json:"toolName,omitempty"`
	IsError    bool       `json:"isError,omitempty"`
}

type ompBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

var ompSupportedVersions = map[int]bool{3: true}

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, err := readOMPFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadResult{Entries: entries, Metadata: extractOMPMetadata(p.SessionFile)}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	meta := extractOMPMetadata(p.SessionFile)
	if meta == nil {
		return &adapterprotocol.ReadMetadataResult{}, nil
	}
	return &adapterprotocol.ReadMetadataResult{AgentVersion: meta.AgentVersion, Model: meta.Model}, nil
}

func readOMPFile(path string) ([]adapterprotocol.RawEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var entries []adapterprotocol.RawEntry
	scanner := newOMPScanner(f)
	for scanner.Scan() {
		if line := scanner.Bytes(); len(line) > 0 {
			entries = append(entries, parseOMPLine(line)...)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}
	return entries, nil
}

func readOMPFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	return adapterruntime.TailJSONL(path, offset, func(line []byte) ([]adapterprotocol.RawEntry, error) {
		return parseOMPLine(line), nil
	})
}

func parseOMPLine(line []byte) []adapterprotocol.RawEntry {
	var rec ompRecord
	if err := json.Unmarshal(line, &rec); err != nil || rec.Type != "message" || rec.Message == nil {
		return nil
	}

	ts, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
	msg := rec.Message
	if msg.Role == "toolResult" {
		return []adapterprotocol.RawEntry{
			adapterruntime.ToolResultWithID(ts, blockText(msg.Content), msg.IsError, msg.ToolCallID),
		}
	}

	var entries []adapterprotocol.RawEntry
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				entries = append(entries, makeOMPEntry(msg.Role, ts, block.Text))
			}
		case "toolCall":
			entries = append(entries, adapterruntime.ToolUseWithID(ts, block.Name, string(block.Arguments), block.ID))
		case "thinking":
			// Reasoning is intentionally excluded from the shared Ledger.
		}
	}
	return entries
}

func blockText(blocks []ompBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func makeOMPEntry(role string, ts time.Time, content string) adapterprotocol.RawEntry {
	switch role {
	case "user":
		return adapterruntime.UserEntry(ts, content)
	case "system":
		return adapterruntime.SystemEntry(ts, content)
	default:
		return adapterruntime.AssistantEntry(ts, content)
	}
}

type ompSessionRoot struct {
	path   string
	direct bool
}

func ompSessionRoots() ([]ompSessionRoot, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	sharedRoots := omppaths.SessionRoots(home)
	roots := make([]ompSessionRoot, 0, len(sharedRoots))
	for _, root := range sharedRoots {
		roots = append(roots, ompSessionRoot{path: root.Path, direct: root.Direct})
	}
	return roots, nil
}

func ompSessionDirNames(cwd string) []string {
	home, _ := os.UserHomeDir()
	return omppaths.ProjectDirectoryNames(cwd, home)
}

func findOMPSession(repoRoot, agentID, since, agentSessionID string) (string, error) {
	roots, err := ompSessionRoots()
	if err != nil {
		return "", err
	}
	if agentSessionID != "" {
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", err
		}
	}

	sinceTime := time.Time{}
	if since != "" {
		sinceTime, _ = time.Parse(time.RFC3339, since)
	}
	var candidates []sessionCandidate
	for _, root := range roots {
		for _, dir := range ompSearchDirs(root, repoRoot) {
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
					continue
				}
				if agentSessionID != "" && entry.Name() != agentSessionID+".jsonl" && !strings.HasSuffix(entry.Name(), "_"+agentSessionID+".jsonl") {
					continue
				}
				path := filepath.Join(dir, entry.Name())
				if repoRoot != "" && !sameOMPProject(sessionHeaderCWD(path), repoRoot) {
					continue
				}
				info, infoErr := entry.Info()
				if infoErr != nil || (!sinceTime.IsZero() && !info.ModTime().After(sinceTime)) {
					continue
				}
				candidates = append(candidates, sessionCandidate{path: path, modTime: info.ModTime()})
			}
		}
	}

	if len(candidates) == 0 {
		if repoRoot != "" {
			return "", fmt.Errorf("no omp sessions found for %s", repoRoot)
		}
		return "", fmt.Errorf("no omp sessions found")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime.After(candidates[j].modTime) })
	if agentID != "" {
		for _, candidate := range candidates {
			if sessionContainsText(candidate.path, agentID) {
				return candidate.path, nil
			}
		}
	}
	return candidates[0].path, nil
}

func ompSearchDirs(root ompSessionRoot, repoRoot string) []string {
	if root.direct {
		return []string{root.path}
	}
	if repoRoot != "" {
		names := ompSessionDirNames(repoRoot)
		out := make([]string, 0, len(names))
		for _, name := range names {
			out = append(out, filepath.Join(root.path, name))
		}
		return out
	}
	entries, err := os.ReadDir(root.path)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, filepath.Join(root.path, entry.Name()))
		}
	}
	return out
}

func sameOMPProject(recorded, requested string) bool {
	return recorded != "" && canonicalOMPPath(recorded) == canonicalOMPPath(requested)
}

func canonicalOMPPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func sessionHeaderCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := newOMPScanner(f)
	for scanner.Scan() {
		var rec ompRecord
		if json.Unmarshal(scanner.Bytes(), &rec) == nil && rec.Type == "session" {
			return rec.CWD
		}
	}
	return ""
}

func sessionContainsText(path, text string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := newOMPScanner(f)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), text) {
			return true
		}
	}
	return false
}

func extractOMPMetadata(path string) *adapterprotocol.SessionMetadata {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	meta := &adapterprotocol.SessionMetadata{}
	scanner := newOMPScanner(f)
	for scanner.Scan() {
		var rec ompRecord
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			continue
		}
		switch rec.Type {
		case "session":
			if rec.Version > 0 {
				meta.AgentVersion = fmt.Sprintf("omp-session-v%d", rec.Version)
			}
		case "model_change":
			if rec.Model != "" {
				meta.Model = rec.Model
			} else if rec.ModelID != "" {
				meta.Model = rec.ModelID
			}
		case "message":
			if meta.AgentVersion == "" && meta.Model == "" {
				return nil
			}
			return meta
		}
	}
	if meta.AgentVersion == "" && meta.Model == "" {
		return nil
	}
	return meta
}

func newOMPScanner(f *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	return scanner
}

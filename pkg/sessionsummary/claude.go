package sessionsummary

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const invokeTimeout = 10 * time.Minute

// defaultSummaryModel is the floating Haiku 4.5 alias. Pinned for
// summarization because the task is structured JSON extraction over a
// fixed schema — well within Haiku's capabilities and 5–15× cheaper
// than the user's local Sonnet/Opus default. Floating (not dated) so
// new Haiku 4.5 point releases roll forward without code changes.
const defaultSummaryModel = "claude-haiku-4-5"

// summaryModelEnv overrides defaultSummaryModel. Empty value (export
// OX_SUMMARY_MODEL=) is meaningful: it restores pre-Haiku behavior by
// passing no --model flag to claude, deferring to the user's local
// default. Useful as a fast rollback.
const summaryModelEnv = "OX_SUMMARY_MODEL"

// DefaultSummaryModel returns the model identifier to pass to `claude -p`
// for summarization. Reads OX_SUMMARY_MODEL if set (including empty), else
// returns "claude-haiku-4-5". Callers pass the result to InvokeClaude.
//
// Empty string return = no --model flag = user's local Claude Code default.
func DefaultSummaryModel() string {
	if v, ok := os.LookupEnv(summaryModelEnv); ok {
		return v // explicit override, including empty for rollback
	}
	return defaultSummaryModel
}

// claudeResultMessage is the minimal structure for parsing Claude's stream-json output.
type claudeResultMessage struct {
	Type   string `json:"type"`
	Result string `json:"result,omitempty"`
}

// InvokeClaude runs `claude --output-format stream-json -p <prompt>` and returns
// the result text. Uses a 10-minute timeout. Returns an error if the claude binary
// is not found, the invocation times out, or no result message appears in output.
//
// model pins the underlying LLM (e.g. "claude-haiku-4-5"). Empty string omits
// --model and defers to the local Claude Code default. Use DefaultSummaryModel()
// for the standard summarization model selection with env-var override.
func InvokeClaude(ctx context.Context, prompt, workDir, model string) (string, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude binary not found in PATH — ensure Claude Code CLI is installed")
	}

	ctx, cancel := context.WithTimeout(ctx, invokeTimeout)
	defer cancel()

	// --permission-mode bypassPermissions: in `-p` non-interactive mode there
	// is no human to approve any tool gated on a permission prompt, so the
	// LLM stalls and narrates the block instead of doing the work.
	// Summary regen has tightly-scoped writes (one /tmp file + one
	// `ox session push-summary` exec) — the permission prompt adds no
	// safety in this context, only failure modes. Filed as bd ox-5cc9.
	args := []string{"--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "-p", prompt)
	cmd := exec.CommandContext(ctx, claudePath, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}

	// discard stderr to avoid blocking
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	// scan JSONL stdout for the result message
	var resultText string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var msg claudeResultMessage
		if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Type == "result" {
			resultText = msg.Result
		}
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("claude timed out after %s", invokeTimeout)
		}
		// non-zero exit is acceptable if we got a result
		if resultText == "" {
			return "", fmt.Errorf("claude exited with error: %w", waitErr)
		}
	}

	if resultText == "" {
		return "", fmt.Errorf("no result message in claude output")
	}

	return resultText, nil
}

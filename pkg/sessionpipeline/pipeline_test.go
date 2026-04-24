package sessionpipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// testStats is a trivial Stats for tests.
type testStats struct {
	bytesIn, bytesOut int64
}

func (t testStats) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int64("bytes_in", t.bytesIn),
		slog.Int64("bytes_out", t.bytesOut),
	)
}

// uppercase is a test stage that upper-cases every byte. Streaming, per-byte.
func uppercase() Stage {
	return StageFunc{
		StageName: "upper",
		Fn: func(ctx context.Context, r io.Reader, w io.Writer) (Stats, error) {
			buf := make([]byte, 4096)
			var in, out int64
			for {
				n, err := r.Read(buf)
				in += int64(n)
				for i := 0; i < n; i++ {
					if buf[i] >= 'a' && buf[i] <= 'z' {
						buf[i] -= 32
					}
				}
				if n > 0 {
					if _, werr := w.Write(buf[:n]); werr != nil {
						return testStats{in, out}, werr
					}
					out += int64(n)
				}
				if err == io.EOF {
					return testStats{in, out}, nil
				}
				if err != nil {
					return testStats{in, out}, err
				}
			}
		},
	}
}

// prependBang prepends "!" to the output. Forces the stage to actually
// process its full input (not just pass through).
func prependBang() Stage {
	return StageFunc{
		StageName: "bang",
		Fn: func(ctx context.Context, r io.Reader, w io.Writer) (Stats, error) {
			if _, err := w.Write([]byte("!")); err != nil {
				return testStats{}, err
			}
			n, err := io.Copy(w, r)
			return testStats{bytesIn: n, bytesOut: n + 1}, err
		},
	}
}

// errStage fails immediately.
func errStage() Stage {
	return StageFunc{
		StageName: "err",
		Fn: func(ctx context.Context, r io.Reader, w io.Writer) (Stats, error) {
			// Drain r so upstream stages don't block on writes.
			_, _ = io.Copy(io.Discard, r)
			return nil, errors.New("boom")
		},
	}
}

func TestRun_ZeroStagesPassThrough(t *testing.T) {
	var out bytes.Buffer
	results, err := Run(context.Background(), strings.NewReader("hello"), &out, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "hello" {
		t.Errorf("expected pass-through 'hello', got %q", out.String())
	}
	if len(results) != 0 {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestRun_SingleStage(t *testing.T) {
	var out bytes.Buffer
	results, err := Run(context.Background(), strings.NewReader("hello"), &out, []Stage{uppercase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "HELLO" {
		t.Errorf("got %q, want HELLO", out.String())
	}
	if len(results) != 1 || results[0].Name != "upper" {
		t.Errorf("expected one result named 'upper', got %v", results)
	}
}

func TestRun_TwoStagesCompose(t *testing.T) {
	// upper then bang: "hello" → "HELLO" → "!HELLO"
	var out bytes.Buffer
	results, err := Run(context.Background(), strings.NewReader("hello"), &out, []Stage{uppercase(), prependBang()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "!HELLO" {
		t.Errorf("got %q, want !HELLO", out.String())
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Err != nil || results[1].Err != nil {
		t.Errorf("stage errors: %v, %v", results[0].Err, results[1].Err)
	}
}

func TestRun_StageErrorPropagates(t *testing.T) {
	var out bytes.Buffer
	results, err := Run(context.Background(), strings.NewReader("hello"), &out, []Stage{uppercase(), errStage()})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "err") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should name failing stage and cause, got: %v", err)
	}
	// Results slice should contain both stages, second with Err set.
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[1].Err == nil {
		t.Errorf("expected results[1].Err to be set")
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled

	// slow stage that respects context
	slow := StageFunc{
		StageName: "slow",
		Fn: func(ctx context.Context, r io.Reader, w io.Writer) (Stats, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	var out bytes.Buffer
	_, err := Run(ctx, strings.NewReader("x"), &out, []Stage{slow})
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

func TestStageFunc_ImplementsStage(t *testing.T) {
	var _ Stage = StageFunc{StageName: "x", Fn: nil}
}

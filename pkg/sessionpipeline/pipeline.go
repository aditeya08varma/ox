package sessionpipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// Stage is a streaming session transform. Implementations read a jsonl
// stream from r, apply their transform, and write a jsonl stream to w.
//
// Streaming is the canonical shape even for transforms that need
// cross-entry state: buffer internally, present an io.Reader at the
// boundary. That keeps this package ignorant of per-transform concerns.
type Stage interface {
	// Name identifies the stage in logs and errors. Short, no spaces.
	// Conventionally the package name or a qualified variant.
	Name() string

	// Apply runs the transform. Must drain r (or honor ctx cancellation)
	// and flush w before returning. Returns implementation-specific Stats
	// that should implement slog.LogValuer for telemetry integration.
	Apply(ctx context.Context, r io.Reader, w io.Writer) (Stats, error)
}

// Stats is what a Stage returns for logging / telemetry. Implementations
// should produce a compact structured representation via slog.LogValuer.
type Stats interface {
	slog.LogValuer
}

// StageFunc adapts a plain func into a Stage. Convenient when the
// transform logic lives as a package-level function.
type StageFunc struct {
	StageName string
	Fn        func(ctx context.Context, r io.Reader, w io.Writer) (Stats, error)
}

func (s StageFunc) Name() string { return s.StageName }
func (s StageFunc) Apply(ctx context.Context, r io.Reader, w io.Writer) (Stats, error) {
	return s.Fn(ctx, r, w)
}

// StageResult records one stage's outcome in a pipeline run.
type StageResult struct {
	Name  string
	Stats Stats
	Err   error
}

// Run composes stages in order, feeding each stage's output into the next
// stage's input via io.Pipe. The first stage reads from src; the last
// stage writes to dst.
//
// On any stage error, all running goroutines are torn down and the error
// is returned with per-stage results captured so far. Stages that
// completed successfully before the failure still appear in results.
//
// Zero stages: src is copied to dst verbatim and nil results are returned.
// This lets callers treat an empty pipeline as a pass-through without a
// special case at the call site.
func Run(ctx context.Context, src io.Reader, dst io.Writer, stages []Stage) ([]StageResult, error) {
	if len(stages) == 0 {
		if _, err := io.Copy(dst, src); err != nil {
			return nil, fmt.Errorf("pass-through copy: %w", err)
		}
		return nil, nil
	}

	// Build the pipe chain. For N stages we need N-1 pipes between them,
	// plus src on the left and dst on the right.
	type hop struct {
		in  io.Reader
		out io.Writer
	}
	hops := make([]hop, len(stages))
	hops[0].in = src
	hops[len(stages)-1].out = dst

	pipeWriters := make([]*io.PipeWriter, 0, len(stages)-1)
	for i := 0; i < len(stages)-1; i++ {
		pr, pw := io.Pipe()
		hops[i].out = pw
		hops[i+1].in = pr
		pipeWriters = append(pipeWriters, pw)
	}

	// Results by stage index so readers see a well-defined order even on
	// partial failure.
	results := make([]StageResult, len(stages))
	errCh := make(chan error, len(stages))
	done := make(chan struct{})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var running int
	for i, stage := range stages {
		running++
		i := i
		stage := stage
		go func() {
			stats, err := stage.Apply(runCtx, hops[i].in, hops[i].out)
			results[i] = StageResult{Name: stage.Name(), Stats: stats, Err: err}
			if stats != nil {
				slog.Info("sessionpipeline stage", "name", stage.Name(), "stats", stats)
			}
			// Close the pipe writer we wrote to (if any) so the next stage
			// sees EOF. On error, CloseWithError propagates so the next
			// stage fails fast instead of blocking on read.
			if i < len(pipeWriters) {
				if err != nil {
					_ = pipeWriters[i].CloseWithError(err)
				} else {
					_ = pipeWriters[i].Close()
				}
			}
			errCh <- err
		}()
	}

	// Collect. First non-nil error cancels the run; we still wait for all
	// goroutines to unwind so we don't leak them.
	go func() {
		for i := 0; i < running; i++ {
			if err := <-errCh; err != nil {
				cancel()
			}
		}
		close(done)
	}()
	<-done

	// Find first error (by stage order) to return.
	for _, r := range results {
		if r.Err != nil {
			return results, fmt.Errorf("stage %q: %w", r.Name, r.Err)
		}
	}
	return results, nil
}

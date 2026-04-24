// Package sessionpipeline defines the shape convention and runner for
// streaming session transforms in the ox session-stop pipeline.
//
// # Why this package exists now (with one consumer)
//
// Today there is one streaming transform (pkg/tokenopt). Normally we would
// wait for a second consumer before extracting an interface. We introduced
// the shape now — deliberately — because:
//
//  1. The planned pipeline has concrete additional stages with known
//     shapes: a REDACT.md-aware LLM redactor (streaming, buffers
//     internally to batch), and eventually a streamified pattern redactor.
//     Pre-baking the Stage interface means the second implementation
//     adopts it on arrival, not as a retrofit across two packages.
//
//  2. The planned pipeline reorders: the realistic sequence is
//     raw → pattern-redact → compress → llm-redact → summarize. Hardcoded
//     function-call sequencing would resist that reordering; a slice of
//     Stage makes it a data change.
//
//  3. Stage composition via io.Pipe is cheap enough (~100 lines) that
//     writing it now and feeding the one current transform through it
//     exercises the seam. If the seam is wrong, we find out today with
//     one consumer, not next quarter with three.
//
// # What we deliberately did NOT do
//
// The existing in-memory redactor (internal/session.Redactor) is NOT
// rewritten to streaming. It works, it's fast, and ROI on the rewrite
// comes only when a second redactor ships. When the LLM redactor lands,
// build it streaming from day 1 — that's the second data point that
// justifies retroactively streaming the first. Until then, the redactor
// keeps its slice-based API and runs before the pipeline (Phase 2),
// writing the canonical raw.jsonl that the pipeline reads from.
//
// # Shape convention
//
// Every streaming session transform should satisfy the Stage interface.
// Transforms that need cross-entry state (LLM redaction batching entries,
// cross-session dedup) buffer internally but still present the streaming
// interface at the boundary. That buffering is their concern, not the
// pipeline's.
//
// # Telemetry
//
// Stage stats types should implement slog.LogValuer. The runner emits a
// per-stage log via slog.Info with the stage name and stats. A single
// telemetry event per pipeline run is emitted at the orchestrator level;
// this package stays out of the telemetry pipeline.
package sessionpipeline

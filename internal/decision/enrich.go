package decision

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"

	"github.com/sageox/ox/internal/config"
)

// registry holds detectors and retrievers; features self-register via init()
// so the orchestrator never changes when a signal is added (the internal/plan
// pattern). Enrich works correctly with zero registrations.
var (
	registryMu sync.RWMutex
	detectors  []Detector
	retrievers []Retriever
)

// RegisterDetector adds a deterministic detector. Nil is ignored.
func RegisterDetector(d Detector) {
	if d == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	detectors = append(detectors, d)
}

// RegisterRetriever adds a context retriever. Nil is ignored.
func RegisterRetriever(r Retriever) {
	if r == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	retrievers = append(retrievers, r)
}

func snapshotRegistry() ([]Detector, []Retriever) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	ds := make([]Detector, len(detectors))
	copy(ds, detectors)
	rs := make([]Retriever, len(retrievers))
	copy(rs, retrievers)
	return ds, rs
}

// Enrich runs every registered detector and retriever, FAIL-OPEN: a panic or
// error in any one is logged and skipped, never aborting the others. Zero LLM
// or network calls — everything reads local data resolved once into Env.
func Enrich(ctx context.Context, in Input, gitRoot string) Result {
	env := buildEnv(gitRoot)
	ds, rs := snapshotRegistry()

	var annotations []Annotation
	for _, d := range ds {
		annotations = append(annotations, runDetector(ctx, d, env, in)...)
	}
	var items []ContextItem
	for _, r := range rs {
		items = append(items, runRetriever(ctx, r, env, in)...)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Ref < items[j].Ref
	})
	if len(items) > bundleCap {
		items = items[:bundleCap]
	}

	sum := summarize(annotations, items)
	info := decisionInfo(in, env)
	var cfg *config.DecisionConfig
	if pc, _ := config.LoadProjectConfig(gitRoot); pc != nil {
		cfg = pc.Decision
	}
	conv := buildConventions(gitRoot, env.Corpus, PrimaryDir(gitRoot, cfg))
	if info.ID == "" && conv.NextNumber > 0 {
		prefix := dominantPrefix(env.Corpus)
		if prefix == "" {
			prefix = "ADR"
		}
		info.SuggestedID = normalizeRefToken(prefix, strconv.Itoa(conv.NextNumber))
	}

	return Result{
		SchemaVersion: SchemaVersion,
		Decision:      info,
		Conventions:   conv,
		Annotations:   annotations,
		Context:       items,
		Signals:       sum,
		Guidance:      buildGuidance(in, sum, conv, annotations, items),
	}
}

// buildEnv resolves the shared read-only environment once per Enrich call.
// Every field is best-effort; consumers fail open on empties.
func buildEnv(gitRoot string) *Env {
	env := &Env{GitRoot: gitRoot}
	if gitRoot == "" {
		return env
	}
	var cfg *config.DecisionConfig
	if pc, _ := config.LoadProjectConfig(gitRoot); pc != nil {
		cfg = pc.Decision
	}
	env.Corpus = LoadCorpus(gitRoot, cfg)
	if pctx, err := config.LoadProjectContext(gitRoot); err == nil && pctx != nil {
		env.LedgerPath = pctx.DefaultLedgerPath()
	}
	return env
}

func runDetector(ctx context.Context, d Detector, env *Env, in Input) (out []Annotation) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("decision detector panicked", "detector", d.Name(), "recover", r)
			out = nil
		}
	}()
	anns, err := d.Detect(ctx, env, in)
	if err != nil {
		slog.Warn("decision detector failed", "detector", d.Name(), "error", err)
		return nil
	}
	return anns
}

func runRetriever(ctx context.Context, r Retriever, env *Env, in Input) (out []ContextItem) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("decision retriever panicked", "retriever", r.Name(), "recover", rec)
			out = nil
		}
	}()
	items, err := r.Retrieve(ctx, env, in)
	if err != nil {
		slog.Warn("decision retriever failed", "retriever", r.Name(), "error", err)
		return nil
	}
	return items
}

func summarize(annotations []Annotation, items []ContextItem) SignalSummary {
	var s SignalSummary
	for _, a := range annotations {
		switch a.Type {
		case BadgeRelatedDecision:
			s.Related++
		case BadgeUnresolvedRef:
			s.UnresolvedRefs++
		case BadgeDiagnostic:
			s.Diagnostics++
		}
	}
	for _, it := range items {
		switch it.Kind {
		case "session", "plan":
			s.PriorSessions++
		case "murmur":
			s.Murmurs++
		}
	}
	s.Material = s.Related > 0 || s.PriorSessions > 0 || s.UnresolvedRefs > 0
	return s
}

func decisionInfo(in Input, env *Env) DecisionInfo {
	info := DecisionInfo{
		ID:     in.Record.ID,
		Title:  in.Record.Title,
		Status: normalizeStatus(in.Record.Status),
	}
	if in.Topic != "" && info.Title == "" {
		info.Title = in.Topic
	}
	return info
}

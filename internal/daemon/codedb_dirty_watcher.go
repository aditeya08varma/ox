package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// dirtyDebounceWindow is how long to wait after the last ChangeAccumulator
	// settle before triggering a dirty overlay rebuild.
	dirtyDebounceWindow = 5 * time.Second

	// dirtyMinInterval is the minimum time between consecutive dirty overlay
	// rebuilds. Caps CPU during sustained editing (branch switches, codegen).
	dirtyMinInterval = 30 * time.Second
)

// DirtyOverlayDebouncer bridges filesystem change events from the
// ChangeAccumulator to CodeDB dirty overlay rebuilds. It applies a debounce
// window (5s) and a minimum interval (30s) to avoid thrashing during active
// coding sessions.
type DirtyOverlayDebouncer struct {
	codedb   *CodeDBManager
	debounce time.Duration
	minGap   time.Duration
	logger   *slog.Logger

	mu       sync.Mutex
	timer    *time.Timer
	lastFire time.Time
	ctx      context.Context
	stopped  bool
	// generation increments on every OnSettled and Stop. Each scheduled fire
	// captures the generation in which it was created; when the fire callback
	// runs, a generation mismatch means a newer OnSettled has superseded it
	// and the fire must be dropped. This replaces relying on time.Timer.Stop()
	// alone, which can't cancel a timer whose callback has already been
	// started in its own goroutine but not yet acquired d.mu.
	generation uint64

	// fireHook is called with the captured lastFire timestamp after the fire
	// has committed. Test-only; nil in production. Exists so tests can observe
	// the exact moment the debouncer considers a fire to have happened,
	// instead of measuring wall-clock time downstream inside RefreshDirtyOverlay
	// where scheduler jitter can compress observed gaps far below minGap.
	fireHook func(time.Time)
}

// NewDirtyOverlayDebouncer creates a debouncer that triggers dirty overlay
// rebuilds after filesystem changes settle.
func NewDirtyOverlayDebouncer(codedb *CodeDBManager, logger *slog.Logger) *DirtyOverlayDebouncer {
	return &DirtyOverlayDebouncer{
		codedb:   codedb,
		debounce: dirtyDebounceWindow,
		minGap:   dirtyMinInterval,
		logger:   logger,
	}
}

// Start sets the context for dirty overlay refreshes.
func (d *DirtyOverlayDebouncer) Start(ctx context.Context) {
	d.mu.Lock()
	d.ctx = ctx
	d.stopped = false
	d.mu.Unlock()
}

// Stop cancels any pending timer and prevents in-flight callbacks from executing.
func (d *DirtyOverlayDebouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	d.generation++
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// OnSettled is the callback wired to ChangeAccumulator.SetOnSettled().
// Each call resets the debounce timer. If the minimum interval hasn't elapsed
// since the last rebuild, the timer is extended to fire at the next allowed time.
func (d *DirtyOverlayDebouncer) OnSettled() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		return
	}

	// Best-effort cancel of any currently-scheduled timer. Stop() may return
	// false if the timer already expired and its callback goroutine has been
	// started but is blocked on d.mu — in that case we can't reach it, and
	// rely on the generation check in fire() to turn it into a no-op.
	if d.timer != nil {
		d.timer.Stop()
	}

	d.generation++
	gen := d.generation

	delay := d.debounce
	if !d.lastFire.IsZero() {
		sinceLastFire := time.Since(d.lastFire)
		if sinceLastFire < d.minGap {
			remaining := d.minGap - sinceLastFire
			if remaining > delay {
				delay = remaining
			}
		}
	}

	d.timer = time.AfterFunc(delay, func() { d.fire(gen) })
}

// fire triggers the dirty overlay rebuild. The gen parameter is the generation
// value captured when this fire was scheduled. If OnSettled (or Stop) has run
// since then, d.generation will have advanced and this fire is stale: it must
// return without touching lastFire or running the refresh, so the next
// generation's fire can enforce minGap against a consistent baseline.
func (d *DirtyOverlayDebouncer) fire(gen uint64) {
	d.mu.Lock()
	if d.stopped || gen != d.generation {
		d.mu.Unlock()
		return
	}
	now := time.Now()
	d.lastFire = now
	d.timer = nil
	ctx := d.ctx
	hook := d.fireHook
	d.mu.Unlock()

	if ctx == nil {
		return
	}

	if hook != nil {
		hook(now)
	}

	d.logger.Debug("dirty overlay debouncer: triggering refresh")
	d.codedb.RefreshDirtyOverlay(ctx)
}

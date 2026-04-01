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
	stopped  bool // prevents stale fire() callbacks from mutating state
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

	if d.timer != nil {
		d.timer.Stop()
	}

	delay := d.debounce
	// enforce minimum interval between rebuilds
	if !d.lastFire.IsZero() {
		sinceLastFire := time.Since(d.lastFire)
		if sinceLastFire < d.minGap {
			remaining := d.minGap - sinceLastFire
			if remaining > delay {
				delay = remaining
			}
		}
	}

	d.timer = time.AfterFunc(delay, d.fire)
}

// fire triggers the dirty overlay rebuild.
// Checks the stopped flag to avoid mutating state after Stop() or when a stale
// timer fires concurrently with OnSettled() installing a new timer.
func (d *DirtyOverlayDebouncer) fire() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.lastFire = time.Now()
	d.timer = nil
	ctx := d.ctx
	d.mu.Unlock()

	if ctx == nil {
		return
	}

	d.logger.Debug("dirty overlay debouncer: triggering refresh")
	d.codedb.RefreshDirtyOverlay(ctx)
}

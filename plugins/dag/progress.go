package reactdag

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// VertexProgress — live state for one vertex
// ---------------------------------------------------------------------------

// VertexProgress is the real-time progress record for a single vertex.
type VertexProgress struct {
	VertexID  string
	State     State
	StartedAt time.Time
	EndedAt   time.Time
	Cached    bool
	CacheTier string
	Err       error
}

// Elapsed returns the elapsed execution time. If still running, uses time.Now().
func (vp VertexProgress) Elapsed() time.Duration {
	if vp.StartedAt.IsZero() {
		return 0
	}
	end := vp.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(vp.StartedAt)
}

// ---------------------------------------------------------------------------
// ProgressTracker — subscribes to EventBus and maintains live state
// ---------------------------------------------------------------------------

// ProgressTracker subscribes to the EventBus and maintains a live per-vertex
// progress table suitable for terminal rendering or programmatic polling.
type ProgressTracker struct {
	mu       sync.RWMutex
	progress map[string]*VertexProgress
	order    []string // insertion order for stable display
	started  time.Time
	ended    time.Time
	unsub    []func()
}

// NewProgressTracker creates a ProgressTracker wired to the given EventBus.
// Call Unsubscribe() when the build is complete to release resources.
func NewProgressTracker(bus *EventBus) *ProgressTracker {
	p := &ProgressTracker{
		progress: make(map[string]*VertexProgress),
		started:  time.Now(),
	}
	p.unsub = []func(){
		bus.Subscribe(EventBuildStart, p.handleBuildStart),
		bus.Subscribe(EventExecutionStart, p.handleExecutionStart),
		bus.Subscribe(EventExecutionEnd, p.handleExecutionEnd),
		bus.Subscribe(EventCacheHit, p.handleCacheHit),
		bus.Subscribe(EventStateChanged, p.handleStateChanged),
		bus.Subscribe(EventBuildEnd, p.handleBuildEnd),
	}
	return p
}

// Unsubscribe removes all EventBus subscriptions.
func (p *ProgressTracker) Unsubscribe() {
	for _, u := range p.unsub {
		u()
	}
}

// Snapshot returns a copy of all progress records in insertion order.
func (p *ProgressTracker) Snapshot() []VertexProgress {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]VertexProgress, 0, len(p.order))
	for _, id := range p.order {
		if vp, ok := p.progress[id]; ok {
			out = append(out, *vp)
		}
	}
	return out
}

// Summary returns live aggregate counts: total, running, completed (fresh),
// cached (served from cache), failed.
func (p *ProgressTracker) Summary() (total, running, fresh, cached, failed int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	total = len(p.progress)
	for _, vp := range p.progress {
		switch {
		case vp.State == StateFailed:
			failed++
		case vp.State == StateCompleted && vp.Cached:
			cached++
		case vp.State == StateCompleted:
			fresh++
		case vp.State != StateInitial:
			running++
		}
	}
	return
}

// Elapsed returns the build wall-clock time so far.
func (p *ProgressTracker) Elapsed() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.ended.IsZero() {
		return p.ended.Sub(p.started)
	}
	return time.Since(p.started)
}

// IsDone reports whether the build has ended (EventBuildEnd received).
func (p *ProgressTracker) IsDone() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.ended.IsZero()
}

// ---------------------------------------------------------------------------
// Event handlers — all satisfy EventHandler (func(context.Context, Event))
// ---------------------------------------------------------------------------

func (p *ProgressTracker) handleBuildStart(_ context.Context, e Event) {
	p.mu.Lock()
	p.started = e.Time
	p.ended = time.Time{}
	p.mu.Unlock()
}

func (p *ProgressTracker) handleExecutionStart(_ context.Context, e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	vp := p.getOrCreate(e.VertexID)
	vp.StartedAt = e.Time
}

func (p *ProgressTracker) handleExecutionEnd(_ context.Context, e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	vp := p.getOrCreate(e.VertexID)
	vp.EndedAt = e.Time
	if errStr, ok := e.Payload["error"].(string); ok && errStr != "" {
		vp.Err = fmt.Errorf("%s", errStr)
	}
}

func (p *ProgressTracker) handleCacheHit(_ context.Context, e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	vp := p.getOrCreate(e.VertexID)
	vp.Cached = true
	if tier, ok := e.Payload["tier"].(string); ok {
		vp.CacheTier = tier
	}
}

func (p *ProgressTracker) handleStateChanged(_ context.Context, e Event) {
	toStr, ok := e.Payload["to"].(string)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	vp := p.getOrCreate(e.VertexID)
	for i, name := range stateNames {
		if name == toStr {
			vp.State = State(i)
			break
		}
	}
	if vp.State.IsTerminal() && vp.EndedAt.IsZero() {
		vp.EndedAt = e.Time
	}
}

func (p *ProgressTracker) handleBuildEnd(_ context.Context, e Event) {
	p.mu.Lock()
	p.ended = e.Time
	p.mu.Unlock()
}

// getOrCreate returns or creates a VertexProgress for id. Caller holds write lock.
func (p *ProgressTracker) getOrCreate(id string) *VertexProgress {
	if vp, ok := p.progress[id]; ok {
		return vp
	}
	vp := &VertexProgress{VertexID: id}
	p.progress[id] = vp
	p.order = append(p.order, id)
	return vp
}

// ---------------------------------------------------------------------------
// ProgressRenderer — writes a live progress display to an io.Writer
// ---------------------------------------------------------------------------

// ProgressRenderer writes a compact progress display to an io.Writer.
// Call Render() on a ticker (e.g., every 100ms) for live terminal output.
// When Animated is true it uses ANSI escape codes to overwrite the previous
// frame in-place, producing a smooth in-place update.
type ProgressRenderer struct {
	w         io.Writer
	tracker   *ProgressTracker
	Animated  bool
	lastLines int
}

// NewProgressRenderer constructs a renderer.
func NewProgressRenderer(w io.Writer, tracker *ProgressTracker, animated bool) *ProgressRenderer {
	return &ProgressRenderer{w: w, tracker: tracker, Animated: animated}
}

// Render writes the current progress state to w.
func (r *ProgressRenderer) Render() {
	snaps := r.tracker.Snapshot()
	total, running, fresh, cached, failed := r.tracker.Summary()
	elapsed := r.tracker.Elapsed()

	var b strings.Builder

	if r.Animated && r.lastLines > 0 {
		fmt.Fprintf(&b, "\033[%dA\033[J", r.lastLines)
	}

	doneIcon := "⟳"
	if r.tracker.IsDone() {
		if failed > 0 {
			doneIcon = "✗"
		} else {
			doneIcon = "✓"
		}
	}

	fmt.Fprintf(&b, "%s Build  %s  total=%-3d run=%-3d ok=%-3d cached=%-3d fail=%d\n",
		doneIcon, fmtDuration(elapsed), total, running, fresh, cached, failed)
	fmt.Fprintln(&b, strings.Repeat("─", 72))

	lines := 2
	for _, vp := range snaps {
		icon := vertexStateIcon(vp)
		dur := fmtDuration(vp.Elapsed())
		if vp.StartedAt.IsZero() {
			dur = "pending"
		}
		cache := ""
		if vp.Cached {
			cache = fmt.Sprintf("[%s]", vp.CacheTier)
		}
		errStr := ""
		if vp.Err != nil {
			errStr = " ✗ " + truncate(vp.Err.Error(), 35)
		}
		fmt.Fprintf(&b, "  %s %-28s %-9s %-8s%s\n",
			icon, truncate(vp.VertexID, 27), dur, cache, errStr)
		lines++
	}

	r.lastLines = lines
	fmt.Fprint(r.w, b.String())
}

// vertexStateIcon returns a one-rune status symbol.
func vertexStateIcon(vp VertexProgress) string {
	switch vp.State {
	case StateCompleted:
		if vp.Cached {
			return "◎"
		}
		return "✓"
	case StateFailed:
		return "✗"
	case StateFastCache, StateSlowCache:
		return "◈"
	default:
		if !vp.StartedAt.IsZero() {
			return "⟳"
		}
		return "·"
	}
}

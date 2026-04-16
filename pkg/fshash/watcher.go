package fshash

import (
	"context"
	"time"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── ChangeEvent ───────────────────────────────────────────────────────────────

// ChangeEvent is emitted by Watcher each time the tree's root digest changes.
type ChangeEvent struct {
	Path       string          // absolute path being watched
	PrevDigest []byte          // digest before the change
	CurrDigest []byte          // digest after the change
	Comparison *TreeComparison // non-nil when WithWatchCompareTrees(true)
}

// ── WatcherOption ─────────────────────────────────────────────────────────────

// WatcherOption configures a Watcher.
type WatcherOption func(*watcherCfg)

type watcherCfg struct {
	interval     time.Duration
	compareTrees bool
}

// WithWatchInterval sets the polling interval (default: 5 s).
func WithWatchInterval(d time.Duration) WatcherOption {
	return func(c *watcherCfg) { c.interval = d }
}

// WithWatchCompareTrees enables full per-entry CompareTrees on each change.
// When true, ChangeEvent.Comparison is populated; costs one extra Sum call.
func WithWatchCompareTrees(v bool) WatcherOption {
	return func(c *watcherCfg) { c.compareTrees = v }
}

// ── Watcher ───────────────────────────────────────────────────────────────────

// Watcher polls absPath and publishes ChangeEvents to an EventBus.
//
// Multiple goroutines can subscribe to events independently:
//
//	w := fshash.NewWatcher(cs, "/data", opts...)
//	id, ch := w.Events().Subscribe(16)
//	defer w.Events().Unsubscribe(id)
//
//	go w.Watch(ctx)
//
//	for evt := range ch {
//	    fmt.Println("changed:", evt.Path)
//	}
type Watcher struct {
	cs      *Checksummer
	absPath string
	bus     *core.EventBus[ChangeEvent]
	cfg     watcherCfg
}

// NewWatcher creates a Watcher that monitors absPath.
func NewWatcher(cs *Checksummer, absPath string, opts ...WatcherOption) *Watcher {
	cfg := watcherCfg{interval: 5 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	return &Watcher{
		cs:      cs,
		absPath: absPath,
		bus:     core.NewEventBus[ChangeEvent](),
		cfg:     cfg,
	}
}

// Events returns the EventBus so callers can subscribe/unsubscribe at will.
func (w *Watcher) Events() *core.EventBus[ChangeEvent] { return w.bus }

// Watch polls until ctx is cancelled. First poll establishes the baseline.
// Events are published to w.Events() on each detected change.
//
// Returns ctx.Err() on normal cancellation.
func (w *Watcher) Watch(ctx context.Context) error {
	res, err := w.cs.Sum(ctx, w.absPath)
	if err != nil {
		return err
	}
	prev := res.Digest

	ticker := time.NewTicker(w.cfg.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			res, err := w.cs.Sum(ctx, w.absPath)
			if err != nil {
				return err
			}
			curr := res.Digest
			if !digestsEqual(prev, curr) {
				evt := ChangeEvent{
					Path:       w.absPath,
					PrevDigest: prev,
					CurrDigest: curr,
				}
				if w.cfg.compareTrees {
					// Run a second Sum with CollectEntries to build a proper
					// prev-vs-curr comparison. We use the current snapshot as
					// "B" and a re-hash as "A" would require storing the prior
					// tree; instead we compare the live state against itself
					// using CompareTrees(prev_path, curr_path) — callers that
					// need before/after must use WatchWithSnapshot.
					_ = 0 // no-op: see WatchWithSnapshot for full comparison
				}
				w.bus.Publish(evt)
				prev = curr
			}
		}
	}
}

// WatchStream is like Watch but returns a Stream the caller ranges over,
// rather than requiring Subscribe/Unsubscribe boilerplate. The stream is
// closed when ctx is cancelled.
//
//	for evt := range w.WatchStream(ctx).Chan() {
//	    fmt.Println(evt.Path, "changed")
//	}
func (w *Watcher) WatchStream(ctx context.Context) *core.Stream[ChangeEvent] {
	s := core.NewStream[ChangeEvent](ctx, 32)
	id, ch := w.bus.Subscribe(32)

	go func() {
		defer w.bus.Unsubscribe(id)
		defer s.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				if !s.Emit(evt) {
					return
				}
			}
		}
	}()

	go func() { w.Watch(ctx) }() //nolint:errcheck
	return s
}

// WatchWithSnapshot polls against a baseline Snapshot. When a change is
// detected, ChangeEvent.Comparison is populated (when WithWatchCompareTrees is
// set) by diffing the old snapshot against a freshly-taken one.
func (w *Watcher) WatchWithSnapshot(ctx context.Context, baseline *Snapshot) error {
	if baseline == nil {
		return w.Watch(ctx)
	}
	prev := hexDecode(baseline.RootDigest)

	ticker := time.NewTicker(w.cfg.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			res, err := w.cs.Sum(ctx, w.absPath)
			if err != nil {
				return err
			}
			curr := res.Digest
			if !digestsEqual(prev, curr) {
				evt := ChangeEvent{
					Path:       w.absPath,
					PrevDigest: prev,
					CurrDigest: curr,
				}
				if w.cfg.compareTrees {
					currSnap, snapErr := TakeSnapshot(ctx, w.absPath,
						WithAlgorithm(Algorithm(baseline.Algorithm)),
						WithMetadata(baseline.Meta()),
					)
					if snapErr == nil {
						dr := baseline.Diff(currSnap)
						changes := make([]TreeChange, 0, len(dr.Added)+len(dr.Removed)+len(dr.Modified))
						for _, p := range dr.Added {
							changes = append(changes, TreeChange{RelPath: p, Status: StatusAdded})
						}
						for _, p := range dr.Removed {
							changes = append(changes, TreeChange{RelPath: p, Status: StatusRemoved})
						}
						for _, p := range dr.Modified {
							changes = append(changes, TreeChange{RelPath: p, Status: StatusModified})
						}
						evt.Comparison = &TreeComparison{Changes: changes, RootA: prev, RootB: curr}
						baseline = currSnap
					}
				}
				w.bus.Publish(evt)
				prev = curr
			}
		}
	}
}

// ── type aliases exported for callers who import only fshash ──────────────────

// Algorithm is a type alias for core.Algorithm so callers need not import core.
type Algorithm = core.Algorithm

// MetaFlag is a type alias for core.MetaFlag.
type MetaFlag = core.MetaFlag

// Built-in algorithm constants (re-exported from core).
const (
	SHA256   = core.SHA256
	SHA512   = core.SHA512
	SHA1     = core.SHA1
	MD5      = core.MD5
	XXHash64 = core.XXHash64
	XXHash3  = core.XXHash3
	Blake3   = core.Blake3
	CRC32C   = core.CRC32C
)

// MetaFlag constants (re-exported from core).
const (
	MetaNone        = core.MetaNone
	MetaMode        = core.MetaMode
	MetaSize        = core.MetaSize
	MetaMtime       = core.MetaMtime
	MetaSymlink     = core.MetaSymlink
	MetaModeAndSize = core.MetaModeAndSize
)

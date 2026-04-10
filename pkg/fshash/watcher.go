package fshash

import (
	"context"
	"time"
)

// ── Watcher ───────────────────────────────────────────────────────────────────

// ChangeEvent is delivered by [Watcher] each time a poll detects a change.
type ChangeEvent struct {
	// Path is the absolute path that was being watched.
	Path string
	// PrevDigest is the digest from the previous poll.
	PrevDigest []byte
	// CurrDigest is the digest from the current poll.
	CurrDigest []byte
	// Comparison contains the full per-entry diff when the Watcher was
	// created with WithWatchCompareTrees(true).  It is nil otherwise.
	Comparison *TreeComparison
}

// WatcherOption configures a [Watcher].
type WatcherOption func(*watcherConfig)

type watcherConfig struct {
	interval     time.Duration
	compareTrees bool
}

// WithWatchInterval sets the polling interval.  Defaults to 5 seconds.
func WithWatchInterval(d time.Duration) WatcherOption {
	return func(c *watcherConfig) { c.interval = d }
}

// WithWatchCompareTrees enables full per-entry comparison on each change.
// When true, [ChangeEvent.Comparison] is populated; when false it is nil.
// Enabling this doubles the number of Sum calls per detected change.
func WithWatchCompareTrees(v bool) WatcherOption {
	return func(c *watcherConfig) { c.compareTrees = v }
}

// Watcher polls absPath at a fixed interval and calls onChange whenever the
// root digest changes.
//
// Usage:
//
//	w := fshash.NewWatcher(cs, "/path/to/dir", func(e fshash.ChangeEvent) {
//	    fmt.Println("changed:", e.Path)
//	})
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	if err := w.Watch(ctx); err != nil && !errors.Is(err, context.Canceled) {
//	    log.Fatal(err)
//	}
type Watcher struct {
	cs       *Checksummer
	absPath  string
	onChange func(ChangeEvent)
	cfg      watcherConfig
}

// NewWatcher creates a [Watcher] that monitors absPath using cs.
// onChange is called on the goroutine that called [Watcher.Watch]; it is
// called synchronously within the poll loop.
func NewWatcher(cs *Checksummer, absPath string, onChange func(ChangeEvent), opts ...WatcherOption) *Watcher {
	cfg := watcherConfig{interval: 5 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	return &Watcher{
		cs:       cs,
		absPath:  absPath,
		onChange: onChange,
		cfg:      cfg,
	}
}

// Watch begins polling.  It blocks until ctx is cancelled or a filesystem
// error occurs.  The first poll establishes the baseline digest; subsequent
// polls compare against the most-recently-seen digest.
//
// Watch returns ctx.Err() on normal cancellation.
func (w *Watcher) Watch(ctx context.Context) error {
	// Establish baseline.
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
			if !equal(prev, curr) {
				evt := ChangeEvent{
					Path:       w.absPath,
					PrevDigest: prev,
					CurrDigest: curr,
				}
				// Note: WithWatchCompareTrees(true) has no effect in Watch() because
				// Watch() does not maintain a snapshot of the previous tree state.
				// Use WatchWithSnapshot() for full before/after comparison.
				w.onChange(evt)
				prev = curr
			}
		}
	}
}

// WatchWithSnapshot is like [Watcher.Watch] but takes an initial [Snapshot]
// as the baseline.  When a change is detected, ChangeEvent.Comparison is
// populated by comparing the live tree against a freshly-taken snapshot of
// the same tree (capturing the before-state requires pre-storing it).
//
// For full before/after comparison, persist a snapshot before each watch
// session and pass it here on restart.
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
			if !equal(prev, curr) {
				evt := ChangeEvent{
					Path:       w.absPath,
					PrevDigest: prev,
					CurrDigest: curr,
				}
				// Take a fresh snapshot and diff against baseline.
				if w.cfg.compareTrees {
					currSnap, snapErr := TakeSnapshot(ctx, w.absPath,
						WithAlgorithm(Algorithm(baseline.Algorithm)),
						WithMetadata(baseline.Meta),
					)
					if snapErr == nil {
						dr := baseline.Diff(currSnap)
						// Wrap DiffResult in TreeComparison for compatibility.
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
						evt.Comparison = &TreeComparison{
							Changes: changes,
							RootA:   prev,
							RootB:   curr,
						}
						// Update baseline to the current snapshot for subsequent polls.
						baseline = currSnap
					}
				}
				w.onChange(evt)
				prev = curr
			}
		}
	}
}

package reactdag

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Watcher — reactive incremental rebuild loop
// ---------------------------------------------------------------------------

// ChangeSource is the source of file-change notifications.
// The returned channel delivers batches of changed files.
// Close() must be called to release resources; it closes the channel.
type ChangeSource interface {
	Changes() <-chan []FileRef
	Close() error
}

// WatcherConfig controls rebuild behaviour.
type WatcherConfig struct {
	// TargetID is the vertex built on every change cycle.
	TargetID string
	// Debounce is the quiet period after the last change before a rebuild fires.
	// Rapid changes within this window are coalesced. Default: 50ms.
	Debounce time.Duration
	// OnBuildStart is called just before each incremental build (optional).
	OnBuildStart func(changedFiles []FileRef)
	// OnBuildEnd is called after each build with its metrics and error (optional).
	OnBuildEnd func(metrics *BuildMetrics, err error)
}

func (c *WatcherConfig) withDefaults() WatcherConfig {
	cp := *c
	if cp.Debounce <= 0 {
		cp.Debounce = 50 * time.Millisecond
	}
	return cp
}

// Watcher watches a ChangeSource and triggers incremental rebuilds on the
// Scheduler whenever files change, honouring a debounce window.
type Watcher struct {
	sched  *Scheduler
	cfg    WatcherConfig
	mu     sync.Mutex
	stopCh chan struct{}
}

// NewWatcher constructs a Watcher bound to the given Scheduler.
func NewWatcher(sched *Scheduler, cfg WatcherConfig) *Watcher {
	return &Watcher{
		sched:  sched,
		cfg:    cfg.withDefaults(),
		stopCh: make(chan struct{}),
	}
}

// Run starts the watch-rebuild loop and blocks until ctx is cancelled or Stop()
// is called. source.Close() is NOT called by Run; the caller owns that lifecycle.
func (w *Watcher) Run(ctx context.Context, source ChangeSource) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// pending carries the latest coalesced change batch (capacity 1 = overwrite).
	pending := make(chan []FileRef, 1)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.debounceLoop(ctx, source.Changes(), pending)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.buildLoop(ctx, pending)
	}()

	select {
	case <-ctx.Done():
		cancel()
	case <-w.stopCh:
		cancel()
	}
	wg.Wait()
	return ctx.Err()
}

// Stop signals the Watcher to stop its run loop.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.stopCh: // already stopped
	default:
		close(w.stopCh)
	}
}

// ---------------------------------------------------------------------------
// Internal loops
// ---------------------------------------------------------------------------

// debounceLoop reads from changes, accumulates refs, and flushes after the
// debounce window elapses without a new event. Last-write-wins for pending.
func (w *Watcher) debounceLoop(ctx context.Context, changes <-chan []FileRef, pending chan []FileRef) {
	var accumulated []FileRef
	var debounceTimer <-chan time.Time

	flush := func() {
		if len(accumulated) == 0 {
			return
		}
		batch := dedupFileRefs(accumulated)
		accumulated = nil
		debounceTimer = nil
		// Overwrite any unread pending batch (last-write-wins).
		select {
		case pending <- batch:
		default:
			// Drain the old one and replace with new.
			select {
			case <-pending:
			default:
			}
			select {
			case pending <- batch:
			default:
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case refs, ok := <-changes:
			if !ok {
				flush()
				return
			}
			accumulated = append(accumulated, refs...)
			// Reset the debounce timer.
			t := time.NewTimer(w.cfg.Debounce)
			debounceTimer = t.C

		case <-debounceTimer:
			flush()
		}
	}
}

// buildLoop drains the pending channel and runs a build for each batch.
func (w *Watcher) buildLoop(ctx context.Context, pending chan []FileRef) {
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-pending:
			if !ok {
				return
			}
			w.runBuild(ctx, batch)
		}
	}
}

// runBuild executes one incremental build and invokes the lifecycle callbacks.
func (w *Watcher) runBuild(ctx context.Context, changedFiles []FileRef) {
	if w.cfg.OnBuildStart != nil {
		w.cfg.OnBuildStart(changedFiles)
	}
	metrics, err := w.sched.Build(ctx, w.cfg.TargetID, changedFiles)
	if w.cfg.OnBuildEnd != nil {
		w.cfg.OnBuildEnd(metrics, err)
	}
}

// ---------------------------------------------------------------------------
// ManualChangeSource — inject changes programmatically (tests / CI webhooks)
// ---------------------------------------------------------------------------

// ManualChangeSource allows external code to push file-change batches.
type ManualChangeSource struct {
	ch     chan []FileRef
	once   sync.Once
	closed chan struct{}
}

// NewManualChangeSource constructs a ManualChangeSource.
func NewManualChangeSource() *ManualChangeSource {
	return &ManualChangeSource{
		ch:     make(chan []FileRef, 64),
		closed: make(chan struct{}),
	}
}

// Push sends a batch of changed files into the source.
// Returns an error if the source has already been closed.
func (s *ManualChangeSource) Push(files ...FileRef) error {
	select {
	case <-s.closed:
		return fmt.Errorf("change source: already closed")
	default:
	}
	select {
	case s.ch <- files:
		return nil
	case <-s.closed:
		return fmt.Errorf("change source: closed during push")
	}
}

// Changes implements ChangeSource.
func (s *ManualChangeSource) Changes() <-chan []FileRef { return s.ch }

// Close implements ChangeSource.
func (s *ManualChangeSource) Close() error {
	s.once.Do(func() {
		close(s.closed)
		close(s.ch)
	})
	return nil
}

// ---------------------------------------------------------------------------
// PollingChangeSource — filesystem polling (backup for envs without inotify)
// ---------------------------------------------------------------------------

// FileSnapshot records a file's content fingerprint at a point in time.
type FileSnapshot struct {
	Path    string
	Hash    [32]byte
	ModTime time.Time
	Size    int64
}

// PollingChangeSource polls a set of files at a fixed interval and emits change
// batches when content (hash) changes. In production, replace with fanotify.
type PollingChangeSource struct {
	interval  time.Duration
	snapshots map[string]FileSnapshot
	fetchFn   func(path string) (FileSnapshot, error)
	changesCh chan []FileRef
	doneCh    chan struct{}
	once      sync.Once
	mu        sync.Mutex
}

// NewPollingChangeSource constructs a PollingChangeSource.
// fetchFn should return a FileSnapshot for the given path (hash via blake3).
func NewPollingChangeSource(
	paths []string,
	fetchFn func(path string) (FileSnapshot, error),
	interval time.Duration,
) *PollingChangeSource {
	s := &PollingChangeSource{
		interval:  interval,
		snapshots: make(map[string]FileSnapshot, len(paths)),
		fetchFn:   fetchFn,
		changesCh: make(chan []FileRef, 16),
		doneCh:    make(chan struct{}),
	}
	for _, p := range paths {
		if snap, err := fetchFn(p); err == nil {
			s.snapshots[p] = snap
		}
	}
	return s
}

// Start begins the polling loop in a background goroutine.
func (s *PollingChangeSource) Start(ctx context.Context) {
	go s.pollLoop(ctx)
}

// Changes implements ChangeSource.
func (s *PollingChangeSource) Changes() <-chan []FileRef { return s.changesCh }

// Close implements ChangeSource.
func (s *PollingChangeSource) Close() error {
	s.once.Do(func() {
		close(s.doneCh)
	})
	return nil
}

// AddPath adds a new path to the watch list at runtime.
func (s *PollingChangeSource) AddPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, err := s.fetchFn(path); err == nil {
		s.snapshots[path] = snap
	}
}

func (s *PollingChangeSource) pollLoop(ctx context.Context) {
	defer close(s.changesCh)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.doneCh:
			return
		case <-ticker.C:
			s.poll()
		}
	}
}

func (s *PollingChangeSource) poll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []FileRef
	for path, prev := range s.snapshots {
		cur, err := s.fetchFn(path)
		if err != nil {
			continue
		}
		if cur.Hash != prev.Hash || cur.Size != prev.Size {
			changed = append(changed, FileRef{Path: path, Hash: cur.Hash, Size: cur.Size, ModTime: cur.ModTime})
			s.snapshots[path] = cur
		}
	}
	if len(changed) > 0 {
		select {
		case s.changesCh <- changed:
		default:
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// dedupFileRefs returns a deduplicated slice; the last FileRef per path wins.
func dedupFileRefs(refs []FileRef) []FileRef {
	seen := make(map[string]FileRef, len(refs))
	for _, r := range refs {
		seen[r.Path] = r
	}
	out := make([]FileRef, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	return out
}

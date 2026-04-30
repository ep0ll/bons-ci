//go:build linux

package fanwatch

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/bons/bons-ci/pkg/fswatch/internal/sys"
)

// ─────────────────────────────────────────────────────────────────────────────
// fanotifyWatcher — Linux implementation of Watcher
// ─────────────────────────────────────────────────────────────────────────────

// fanotifyWatcher implements [Watcher] using Linux fanotify via stdlib syscall.
type fanotifyWatcher struct {
	cfg         watcherConfig
	mu          sync.Mutex
	watching    bool
	closed      bool
	fanotifyFD  int
	stopReadFD  int
	stopWriteFD int
}

// newWatcher is the Linux-specific constructor called by the public NewWatcher.
func newWatcher(opts ...WatcherOption) (Watcher, error) {
	cfg := defaultWatcherConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.mergedDir == "" {
		return nil, fmt.Errorf("fanwatch: WithMergedDir is required")
	}

	fanotifyFD, err := sys.Init()
	if err != nil {
		if isPermissionErr(err) {
			return nil, ErrPermission
		}
		return nil, fmt.Errorf("fanwatch: init: %w", err)
	}

	stopR, stopW, err := sys.StopPipe()
	if err != nil {
		sys.Close(fanotifyFD)
		return nil, fmt.Errorf("fanwatch: stop pipe: %w", err)
	}

	w := &fanotifyWatcher{
		cfg:         cfg,
		fanotifyFD:  fanotifyFD,
		stopReadFD:  stopR,
		stopWriteFD: stopW,
	}

	if err := w.markMergedDir(); err != nil {
		w.closeResources()
		return nil, err
	}
	return w, nil
}

// markMergedDir registers the merged directory with fanotify.
// FAN_EVENT_ON_CHILD and FAN_ONDIR ensure subdirectory events are delivered.
func (w *fanotifyWatcher) markMergedDir() error {
	const fanOnDir = uint64(0x40000000)
	const fanEventOnChild = uint64(0x08000000)

	mask := uint64(w.cfg.mask) | fanOnDir | fanEventOnChild
	if err := sys.MarkMount(w.fanotifyFD, mask, w.cfg.mergedDir); err != nil {
		return fmt.Errorf("fanwatch: mark %q: %w", w.cfg.mergedDir, err)
	}
	return nil
}

// Watch implements [Watcher].
func (w *fanotifyWatcher) Watch(ctx context.Context) (<-chan *RawEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return nil, ErrWatcherClosed
	}
	if w.watching {
		return nil, fmt.Errorf("fanwatch: Watch already active; create a new Watcher to watch again")
	}
	w.watching = true

	out := make(chan *RawEvent, w.cfg.bufSize)
	go w.readLoop(ctx, out)
	return out, nil
}

// readLoop is the event-reading goroutine. It blocks on poll(2) and processes
// events until the context is cancelled or the stop pipe fires.
func (w *fanotifyWatcher) readLoop(ctx context.Context, out chan<- *RawEvent) {
	defer close(out)

	buf := make([]byte, w.cfg.readBufSize)

	for {
		if ctx.Err() != nil {
			return
		}

		readable, err := sys.WaitReadable(w.fanotifyFD, w.stopReadFD)
		if err != nil || !readable {
			return
		}

		records, err := sys.ReadEvents(w.fanotifyFD, buf)
		if err != nil {
			// Non-fatal read error — continue.
			continue
		}

		now := time.Now()
		for _, rec := range records {
			path, pathErr := sys.FDToPath(rec.FD)
			if pathErr != nil {
				// Process exited before path resolution — skip.
				continue
			}

			event := &RawEvent{
				Mask:      EventMask(rec.Mask),
				PID:       rec.PID,
				Path:      path,
				Timestamp: now,
				WatcherID: w.cfg.watcherID,
			}

			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Close implements [Watcher]. Idempotent.
func (w *fanotifyWatcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	sys.SignalStop(w.stopWriteFD)
	w.closeResources()
	return nil
}

// closeResources closes all file descriptors owned by the watcher.
func (w *fanotifyWatcher) closeResources() {
	if w.fanotifyFD >= 0 {
		sys.Close(w.fanotifyFD)
		w.fanotifyFD = -1
	}
	if w.stopWriteFD >= 0 {
		sys.Close(w.stopWriteFD)
		w.stopWriteFD = -1
	}
	if w.stopReadFD >= 0 {
		sys.Close(w.stopReadFD)
		w.stopReadFD = -1
	}
}

// isPermissionErr checks whether an error indicates missing capability.
func isPermissionErr(err error) bool {
	return err != nil && (err == syscall.EPERM ||
		containsStr(err.Error(), "CAP_SYS_ADMIN") ||
		containsStr(err.Error(), "permission denied"))
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

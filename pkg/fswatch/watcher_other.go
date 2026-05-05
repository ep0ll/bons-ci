//go:build !linux

package fswatch

import "context"

// newWatcher returns a stub watcher on non-Linux platforms.
func newWatcher(opts ...WatcherOption) (Watcher, error) {
	return &stubWatcher{}, nil
}

// stubWatcher is a no-op Watcher for non-Linux platforms.
type stubWatcher struct{}

func (s *stubWatcher) Watch(_ context.Context) (<-chan *RawEvent, error) {
	return nil, ErrNotSupported
}

func (s *stubWatcher) Close() error { return nil }

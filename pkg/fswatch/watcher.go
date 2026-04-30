package fanwatch

import "context"

// Watcher observes a merged overlay directory via Linux fanotify and delivers
// raw filesystem events on a channel.
//
// # Lifecycle
//
//  1. Construct with [NewWatcher].
//  2. Call [Watch] to start observation; it returns a receive-only channel.
//  3. Feed the channel to a [Pipeline].
//  4. Cancel the context to stop; the channel is closed after cleanup.
//
// # Concurrency
//
// Watch may only be called once per Watcher. Calling it a second time returns
// an error. A Watcher may not be reused after its context is cancelled.
//
// # Privilege
//
// fanwatch requires CAP_SYS_ADMIN (or equivalent in a privileged container).
// Without it, [NewWatcher] returns [ErrPermission].
type Watcher interface {
	// Watch starts the fanotify observation loop and returns a channel of raw
	// events. The channel is closed when ctx is cancelled or an unrecoverable
	// error occurs.
	Watch(ctx context.Context) (<-chan *RawEvent, error)

	// Close releases all resources held by the watcher. It is idempotent.
	// After Close, Watch must not be called.
	Close() error
}

// NewWatcher constructs the best available [Watcher] for the current platform
// and configuration. On Linux this is a [fanotifyWatcher]; on other platforms
// it returns a stub that returns [ErrNotSupported] from Watch.
func NewWatcher(opts ...WatcherOption) (Watcher, error) {
	return newWatcher(opts...)
}

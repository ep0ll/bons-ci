package fanwatch

import "errors"

// Sentinel errors returned by fanwatch APIs.
var (
	// ErrMountNotFound is returned when no overlay mount matches the requested
	// merged directory path in /proc/self/mountinfo.
	ErrMountNotFound = errors.New("fanwatch: overlay mount not found")

	// ErrWatcherClosed is returned when methods are called on a closed watcher.
	ErrWatcherClosed = errors.New("fanwatch: watcher closed")

	// ErrNotSupported is returned on platforms where fanotify is unavailable.
	ErrNotSupported = errors.New("fanwatch: fanotify not supported on this platform")

	// ErrPermission is returned when the process lacks CAP_SYS_ADMIN.
	ErrPermission = errors.New("fanwatch: insufficient privilege (CAP_SYS_ADMIN required)")

	// ErrQueueOverflow is a non-fatal error delivered when the fanotify kernel
	// queue overflows. Some events were dropped. The watcher continues.
	ErrQueueOverflow = errors.New("fanwatch: fanotify queue overflow — some events dropped")

	// ErrPathEscapes is returned when a resolved path would escape the watched root.
	ErrPathEscapes = errors.New("fanwatch: resolved path escapes watched root")
)

package fsmonitor

import (
	"context"
	"errors"
)

// ErrNotSupported is returned on non-Linux platforms.
var ErrNotSupported = errors.New("fsmonitor: fanotify is only supported on Linux")

// FileStat tracks the operations and the latest checksum of a monitored file.
type FileStat struct {
	Path           string `json:"path"`
	Reads          uint64 `json:"reads"`
	Writes         uint64 `json:"writes"`
	Checksum       string `json:"checksum,omitempty"`        // Full file checksum on close
	AccessChecksum string `json:"access_checksum,omitempty"` // Checksum of only accessed bytes
}

// Stats returns a snapshot of the current monitoring results.
type Stats struct {
	Files         map[string]FileStat `json:"files"`
	QueueOverflow uint64              `json:"queue_overflow"`
	EventsTotal   uint64              `json:"events_total"`
}

// Monitor defining the platform-agnostic interface for file system tracking.
type Monitor interface {
	// Add adds a directory or file to the monitoring watchlist.
	// dirs can be recursive depending on the implementation.
	Add(path string) error

	// Run begins monitoring. It blocks until the context is cancelled 
	// or a fatal error occurs.
	Run(ctx context.Context) error

	// Snapshot returns an instantaneous snapshot of the file operation statistics.
	Snapshot() Stats
}

// Package event provides a lightweight, reactive pub-sub event bus for
// content store lifecycle notifications.
package event

import (
	"context"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// Kind identifies the category of a store lifecycle event.
type Kind string

const (
	// KindWriteStarted fires when a Writer is opened successfully.
	KindWriteStarted Kind = "content.write.started"
	// KindWriteCommitted fires when a write is durably committed.
	KindWriteCommitted Kind = "content.write.committed"
	// KindWriteFailed fires when a write or commit returns an error.
	KindWriteFailed Kind = "content.write.failed"
	// KindReadHit fires when content is found in the store.
	KindReadHit Kind = "content.read.hit"
	// KindReadMiss fires when content is not found in the store.
	KindReadMiss Kind = "content.read.miss"
	// KindDeleted fires when content is deleted from the store.
	KindDeleted Kind = "content.deleted"
)

// Event carries information about a single content store operation.
type Event struct {
	// Kind is the event category.
	Kind Kind
	// Source identifies the store that produced the event (e.g., "local", "registry").
	Source string
	// Digest is the content digest affected by this event, if applicable.
	Digest digest.Digest
	// Ref is the in-progress write reference, if applicable.
	Ref string
	// Err holds the error from a failed operation, or nil on success.
	Err error
	// OccurredAt is the wall-clock time of the event.
	OccurredAt time.Time
}

// Handler is a callback invoked whenever an Event is published to a Bus.
// Handlers must be safe for concurrent use across multiple goroutines.
type Handler func(ctx context.Context, evt Event)

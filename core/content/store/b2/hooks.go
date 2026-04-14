package b2

import (
	"context"
	"time"

	"github.com/opencontainers/go-digest"
)

// ---------------------------------------------------------------------------
// Event Kinds
// ---------------------------------------------------------------------------

// EventKind identifies the type of lifecycle event.
type EventKind string

const (
	EventBlobCommitted EventKind = "blob.committed" // after a successful Writer.Commit
	EventBlobDeleted   EventKind = "blob.deleted"   // after a successful Delete
	EventBlobAccessed  EventKind = "blob.accessed"  // after a ReaderAt or Info call
	EventBlobWalked    EventKind = "blob.walked"    // for each blob visited during Walk
)

// Event represents a lifecycle event emitted by the Store.
type Event struct {
	Kind      EventKind
	Digest    digest.Digest
	Size      int64
	Ref       string
	Labels    map[string]string
	Timestamp time.Time
}

// ---------------------------------------------------------------------------
// Hook — Observer Interface (Open/Closed Principle)
// ---------------------------------------------------------------------------

// Hook receives lifecycle events from the Store.
// Implementations should be non-blocking; long-running work should
// be dispatched to a background goroutine.
type Hook interface {
	OnEvent(ctx context.Context, evt Event)
}

// HookFunc adapts a plain function into a Hook.
type HookFunc func(ctx context.Context, evt Event)

// OnEvent implements Hook.
func (f HookFunc) OnEvent(ctx context.Context, evt Event) { f(ctx, evt) }

// compile-time check
var _ Hook = (HookFunc)(nil)

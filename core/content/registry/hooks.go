package registry

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
	EventBlobFetched  EventKind = "blob.fetched"  // content fetched from remote registry
	EventBlobPushed   EventKind = "blob.pushed"   // content pushed to remote registry
	EventBlobCached   EventKind = "blob.cached"   // content served from local cache
	EventBlobDeleted  EventKind = "blob.deleted"  // content deleted
	EventBlobAccessed EventKind = "blob.accessed" // content metadata accessed (Info)
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

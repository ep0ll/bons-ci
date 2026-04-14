package registry

import (
	"context"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// EventKind identifies the type of a lifecycle event emitted by Store.
type EventKind string

const (
	EventBlobFetched  EventKind = "blob.fetched"  // fetched from remote registry
	EventBlobPushed   EventKind = "blob.pushed"   // pushed to remote registry
	EventBlobCached   EventKind = "blob.cached"   // served from local cache
	EventBlobDeleted  EventKind = "blob.deleted"  // deleted from local store
	EventBlobAccessed EventKind = "blob.accessed" // metadata accessed via Info
)

// Event is an immutable value describing a single lifecycle occurrence.
type Event struct {
	Kind      EventKind
	Digest    digest.Digest
	Size      int64
	Ref       string
	Labels    map[string]string
	Timestamp time.Time
}

// Hook receives lifecycle events from Store.
//
// Implementations MUST be safe for concurrent invocation and MUST NOT block.
// Long-running work should be dispatched to a background goroutine.
type Hook interface {
	OnEvent(ctx context.Context, evt Event)
}

// HookFunc adapts a plain function to the Hook interface.
type HookFunc func(ctx context.Context, evt Event)

// OnEvent implements Hook.
func (f HookFunc) OnEvent(ctx context.Context, evt Event) { f(ctx, evt) }

// compile-time assertion
var _ Hook = (HookFunc)(nil)

package registry

import (
	"context"

	"github.com/opencontainers/go-digest"
)

type EventKind string

const (
	EventBlobFetched          EventKind = "registry.blob.fetched"
	EventBlobCached           EventKind = "registry.blob.cached"
	EventPipelineStarted      EventKind = "registry.pipeline.started"
	EventPipelineCompleted    EventKind = "registry.pipeline.completed"
	EventBlobDeleted          EventKind = "registry.blob.deleted"
	EventBlobAccessed         EventKind = "registry.blob.accessed"
)

// Event describes a lifecycle occurrence within the registry store or ingestion pipeline.
type Event struct {
	Kind   EventKind
	Digest digest.Digest
	Size   int64
	Ref    string // Used for pipeline events
}

// Hook is an observer for registry store events.
type Hook interface {
	OnEvent(ctx context.Context, evt Event)
}

// HookFunc is a functional adapter for Hook.
type HookFunc func(ctx context.Context, evt Event)

// OnEvent implements Hook.
func (f HookFunc) OnEvent(ctx context.Context, evt Event) {
	f(ctx, evt)
}

func emitHook(ctx context.Context, hooks []Hook, evt Event) {
	for _, hook := range hooks {
		hook.OnEvent(ctx, evt)
	}
}

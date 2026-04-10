package registry

import (
	"time"

	"go.opentelemetry.io/otel/trace"
)

// StoreOption configures a Store at construction time.
type StoreOption func(*storeOptions)

type storeOptions struct {
	tracer   trace.TracerProvider
	hooks    []Hook
	cacheTTL time.Duration
}

// WithTracer enables OpenTelemetry tracing on the store.
// When set, the Store can be wrapped in a TracedStore decorator via
// NewTracedStore.
func WithTracer(tp trace.TracerProvider) StoreOption {
	return func(o *storeOptions) {
		o.tracer = tp
	}
}

// WithHooks registers lifecycle hooks that receive events for
// blob fetches, pushes, cache hits, deletes, and metadata accesses.
func WithHooks(hooks ...Hook) StoreOption {
	return func(o *storeOptions) {
		o.hooks = append(o.hooks, hooks...)
	}
}

// WithCacheTTL sets the time-to-live for the in-memory info cache.
// Default is 5 minutes.
func WithCacheTTL(ttl time.Duration) StoreOption {
	return func(o *storeOptions) {
		o.cacheTTL = ttl
	}
}

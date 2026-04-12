package registry

import (
	"time"

	"go.opentelemetry.io/otel/trace"
)

// StoreOption configures a Store at construction time via functional options.
type StoreOption func(*storeOptions)

// storeOptions holds the resolved configuration for a Store.
type storeOptions struct {
	tracer      trace.TracerProvider
	hooks       []Hook
	cacheTTL    time.Duration
	retryMax    int
	workerLimit int
}

// WithTracer attaches an OpenTelemetry TracerProvider.
// Wrap the constructed Store with NewTracedStore to activate per-op spans.
func WithTracer(tp trace.TracerProvider) StoreOption {
	return func(o *storeOptions) { o.tracer = tp }
}

// WithHooks registers one or more lifecycle Hook observers.
// Multiple calls append; duplicates are the caller's responsibility.
func WithHooks(hooks ...Hook) StoreOption {
	return func(o *storeOptions) { o.hooks = append(o.hooks, hooks...) }
}

// WithCacheTTL sets the entry TTL for the sharded InfoCache (default: 5 min).
// A zero or negative value uses the default.
func WithCacheTTL(ttl time.Duration) StoreOption {
	return func(o *storeOptions) { o.cacheTTL = ttl }
}

// WithRetryMax sets the maximum number of attempts for transient remote errors
// (default: 3). Set to 1 to disable retries.
func WithRetryMax(n int) StoreOption {
	return func(o *storeOptions) { o.retryMax = n }
}

// WithWriterWorkerLimit sets the channel depth for async local cache writes
// per contentWriter (default: 32). Higher values absorb burst writes at the
// cost of slightly more memory.
func WithWriterWorkerLimit(n int) StoreOption {
	return func(o *storeOptions) { o.workerLimit = n }
}

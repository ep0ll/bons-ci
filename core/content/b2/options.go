package b2

import (
	"go.opentelemetry.io/otel/trace"
)

// StoreOption configures a Store at construction time.
type StoreOption func(*storeOptions)

type storeOptions struct {
	tracer trace.TracerProvider
	hooks  []Hook
}

// WithTracer enables OpenTelemetry tracing on the store.
// When set, the Store returned by New / NewWithMinio is wrapped in
// a TracedStore decorator that creates spans for every operation.
func WithTracer(tp trace.TracerProvider) StoreOption {
	return func(o *storeOptions) {
		o.tracer = tp
	}
}

// WithHooks registers lifecycle hooks that receive events for
// blob commits, deletes, accesses, and walks.
func WithHooks(hooks ...Hook) StoreOption {
	return func(o *storeOptions) {
		o.hooks = append(o.hooks, hooks...)
	}
}

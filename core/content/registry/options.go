package registry

import (
	"time"

	"github.com/containerd/containerd/v2/core/transfer/registry"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type Config struct {
	Ref          string
	InfoCacheTTL time.Duration
	RegistryOpts []registry.Opt
	Tracer       trace.TracerProvider
	Hooks        []Hook
}

type StoreOption func(*Config)

// WithReference sets the primary image reference being ingested.
func WithReference(ref string) StoreOption {
	return func(c *Config) {
		c.Ref = ref
	}
}

// WithRegistryOpts provides authentication and configuration options for the underlying registry.
func WithRegistryOpts(opts ...registry.Opt) StoreOption {
	return func(c *Config) {
		c.RegistryOpts = opts
	}
}

// WithInfoCacheTTL sets the time-to-live for remote Info queries to prevent rate limits.
func WithInfoCacheTTL(ttl time.Duration) StoreOption {
	return func(c *Config) {
		c.InfoCacheTTL = ttl
	}
}

// WithTracer provides an OpenTelemetry tracer provider for instrumenting the store operations.
func WithTracer(tp trace.TracerProvider) StoreOption {
	return func(c *Config) {
		c.Tracer = tp
	}
}

// WithHooks registers event observers onto the content store lifecycle.
func WithHooks(hooks ...Hook) StoreOption {
	return func(c *Config) {
		c.Hooks = append(c.Hooks, hooks...)
	}
}

func applyOptions(opts []StoreOption) Config {
	cfg := Config{
		InfoCacheTTL: 5 * time.Minute,
		Tracer:       noop.NewTracerProvider(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

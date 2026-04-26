package solve

import "context"

// ─── Lifecycle Hooks ─────────────────────────────────────────────────────────

// Observer is notified during solve vertex lifecycle events.
// Implementations can be used for progress reporting, logging, or debugging.
type Observer interface {
	// PreSolve is called before the sub-graph is marshalled.
	PreSolve(ctx context.Context, cfg Config)
	// PostSolve is called after successful marshalling.
	PostSolve(ctx context.Context, cfg Config, digest string)
	// OnError is called when marshalling fails.
	OnError(ctx context.Context, cfg Config, err error)
}

// Hook is a lifecycle hook that can inspect/modify the solve process.
type Hook interface {
	// BeforeMarshal is called before the sub-graph is serialised.
	// Can be used for validation or injection of additional metadata.
	BeforeMarshal(ctx context.Context, cfg *Config) error
	// AfterMarshal is called after successful serialisation.
	// Receives the serialised definition bytes and can perform side-effects.
	AfterMarshal(ctx context.Context, cfg Config, defBytes []byte) error
}

// WithObserver attaches an observer to the solve config.
func WithObserver(o Observer) Option {
	return func(c *Config) { c.Observer = o }
}

// WithHook attaches a lifecycle hook to the solve config.
func WithHook(h Hook) Option {
	return func(c *Config) { c.Hooks = append(c.Hooks, h) }
}

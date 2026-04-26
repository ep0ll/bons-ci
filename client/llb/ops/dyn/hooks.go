package dyn

import "context"

// ─── Lifecycle Hooks ─────────────────────────────────────────────────────────

// Observer is notified during dyn vertex lifecycle events.
type Observer interface {
	// PreEvaluate is called before the policy exec is marshalled.
	PreEvaluate(ctx context.Context, cfg Config)
	// PostEvaluate is called after successful marshalling.
	PostEvaluate(ctx context.Context, cfg Config, digest string)
	// OnError is called when marshalling or evaluation fails.
	OnError(ctx context.Context, cfg Config, err error)
}

// Hook is a lifecycle hook for the dyn evaluation process.
type Hook interface {
	// BeforeMarshal is called before the exec is serialised.
	BeforeMarshal(ctx context.Context, cfg *Config) error
	// AfterMarshal is called after the exec is serialised.
	AfterMarshal(ctx context.Context, cfg Config, defBytes []byte) error
}

// WithObserver attaches an observer to the dyn config.
func WithObserver(o Observer) Option {
	return func(c *Config) { c.Observer = o }
}

// WithHook attaches a lifecycle hook to the dyn config.
func WithHook(h Hook) Option {
	return func(c *Config) { c.Hooks = append(c.Hooks, h) }
}

// WithTimeout configures evaluation timeout in seconds.
func WithTimeout(seconds int) Option {
	return func(c *Config) { c.TimeoutSecs = seconds }
}

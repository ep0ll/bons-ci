package export

import "context"

// ─── Lifecycle Hooks ─────────────────────────────────────────────────────────

// Observer is notified during export vertex lifecycle events.
type Observer interface {
	// PreExport is called before the export op is marshalled.
	PreExport(ctx context.Context, cfg Config)
	// PostExport is called after successful marshalling.
	PostExport(ctx context.Context, cfg Config, digest string)
	// OnError is called when marshalling fails.
	OnError(ctx context.Context, cfg Config, err error)
}

// Hook is a lifecycle hook for the export process.
type Hook interface {
	// BeforeMarshal is called before serialisation.
	BeforeMarshal(ctx context.Context, cfg *Config) error
	// AfterMarshal is called after serialisation with the result bytes.
	AfterMarshal(ctx context.Context, cfg Config, defBytes []byte) error
}

// AttestationPredicate describes an in-toto attestation to attach.
type AttestationPredicate struct {
	// Type is the predicate type URI (e.g. "https://slsa.dev/provenance/v1").
	Type string
	// Payload is the raw predicate JSON.
	Payload []byte
}

// WithObserver attaches an observer to the export config.
func WithObserver(o Observer) Option {
	return func(c *Config) { c.Observer = o }
}

// WithHook attaches a lifecycle hook to the export config.
func WithHook(h Hook) Option {
	return func(c *Config) { c.Hooks = append(c.Hooks, h) }
}

// WithAttestation attaches an attestation predicate for inclusion in the export.
func WithAttestation(p AttestationPredicate) Option {
	return func(c *Config) { c.Attestations = append(c.Attestations, p) }
}

// WithPlatforms configures multi-platform export (manifest list).
func WithPlatforms(platforms ...string) Option {
	return func(c *Config) { c.Platforms = platforms }
}

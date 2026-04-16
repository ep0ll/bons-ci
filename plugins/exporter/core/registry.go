package core

import (
	"context"
	"fmt"
	"sync"
)

// ─── Registry ──────────────────────────────────────────────────────────────

// Registry is a thread-safe catalogue of registered Exporter implementations.
// The global DefaultRegistry is pre-populated by init() calls in exporter
// sub-packages; callers may also create isolated registries for testing.
//
// Registration is intentionally append-only after the first Resolve call
// to prevent races between registration and resolution in long-lived servers.
type Registry interface {
	// Register adds an exporter. Returns ErrAlreadyRegistered if the type
	// is already present.
	Register(e Exporter) error

	// MustRegister calls Register and panics on error. Intended for init().
	MustRegister(e Exporter)

	// Resolve looks up and resolves the named exporter.
	// Shorthand for Get + Exporter.Resolve.
	Resolve(ctx context.Context, t ExporterType, opts Options) (ExporterInstance, error)

	// Get returns the Exporter for type t, or ErrNotRegistered.
	Get(t ExporterType) (Exporter, error)

	// Types returns a sorted list of all registered exporter type strings.
	Types() []ExporterType
}

// ─── registry (concrete) ───────────────────────────────────────────────────

type registry struct {
	mu        sync.RWMutex
	exporters map[ExporterType]Exporter
	sealed    bool // set to true on first Resolve; prevents new registrations
}

// NewRegistry returns an empty, unsealed Registry.
func NewRegistry() Registry {
	return &registry{
		exporters: make(map[ExporterType]Exporter),
	}
}

// DefaultRegistry is the process-wide registry. Exporter sub-packages call
// DefaultRegistry.MustRegister in their init() functions.
var DefaultRegistry Registry = NewRegistry()

func (r *registry) Register(e Exporter) error {
	if e == nil {
		return fmt.Errorf("registry: cannot register nil exporter")
	}
	t := e.Type()
	if t == "" {
		return fmt.Errorf("registry: exporter has empty Type()")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed {
		return fmt.Errorf("%w: %q (registry is sealed after first Resolve)", ErrAlreadyRegistered, t)
	}
	if _, exists := r.exporters[t]; exists {
		return fmt.Errorf("%w: %q", ErrAlreadyRegistered, t)
	}
	r.exporters[t] = e
	return nil
}

func (r *registry) MustRegister(e Exporter) {
	if err := r.Register(e); err != nil {
		panic("exporter registry: " + err.Error())
	}
}

func (r *registry) Resolve(ctx context.Context, t ExporterType, opts Options) (ExporterInstance, error) {
	exp, err := r.Get(t)
	if err != nil {
		return nil, err
	}
	// Seal on first successful resolution — no more registrations allowed.
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()

	return exp.Resolve(ctx, opts)
}

func (r *registry) Get(t ExporterType) (Exporter, error) {
	r.mu.RLock()
	e, ok := r.exporters[t]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotRegistered, t)
	}
	return e, nil
}

func (r *registry) Types() []ExporterType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]ExporterType, 0, len(r.exporters))
	for t := range r.exporters {
		types = append(types, t)
	}
	// Stable sort for deterministic output.
	sortExporterTypes(types)
	return types
}

// sortExporterTypes sorts in place (insertion sort; N is tiny).
func sortExporterTypes(ts []ExporterType) {
	for i := 1; i < len(ts); i++ {
		key := ts[i]
		j := i - 1
		for j >= 0 && ts[j] > key {
			ts[j+1] = ts[j]
			j--
		}
		ts[j+1] = key
	}
}

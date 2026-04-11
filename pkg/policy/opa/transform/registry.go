// Package transform defines the Transformer interface and a Registry that maps
// (kind, action) keys to ordered transformer chains. This is the mid-level
// extension point; callers register domain transformers without touching the engine.
//
// Design invariants:
//   - Transformers are pure functions: (ctx, input, Decision) → Decision.
//   - The Registry is append-only after construction for goroutine safety.
//   - Specific-action entries always run before wildcard-action entries.
//   - Every Apply call is individually traced via OTEL.
package transform

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
)

// ─── Decision ─────────────────────────────────────────────────────────────────

// Decision is the mutable result object threaded through the transform pipeline.
// It is value-typed (copied between transforms) so each transformer gets a
// clean view and can return a modified copy without aliasing.
type Decision struct {
	Action     string
	Mutated    bool
	Messages   []string
	Updates    map[string]any
	Expansions []map[string]any
}

// Clone returns a deep copy of d, preventing slice/map aliasing between
// successive transformers.
func (d Decision) Clone() Decision {
	out := Decision{
		Action:  d.Action,
		Mutated: d.Mutated,
	}
	if d.Messages != nil {
		out.Messages = make([]string, len(d.Messages))
		copy(out.Messages, d.Messages)
	}
	if d.Updates != nil {
		out.Updates = make(map[string]any, len(d.Updates))
		for k, v := range d.Updates {
			out.Updates[k] = v
		}
	}
	if d.Expansions != nil {
		out.Expansions = make([]map[string]any, len(d.Expansions))
		for i, e := range d.Expansions {
			cp := make(map[string]any, len(e))
			for k, v := range e {
				cp[k] = v
			}
			out.Expansions[i] = cp
		}
	}
	return out
}

// ─── Transformer interface ────────────────────────────────────────────────────

// Transformer applies domain-specific mutations to a Decision.
// Implementations must be safe for concurrent use. Apply receives a cloned
// Decision and should return the (possibly modified) value.
type Transformer interface {
	// Name returns a stable dot-namespaced identifier, e.g. "source.mutate_op".
	Name() string
	// Apply receives the original request input and current decision, and
	// returns a potentially mutated decision or an error.
	Apply(ctx context.Context, input any, dec Decision) (Decision, error)
}

// ─── Key ─────────────────────────────────────────────────────────────────────

// Key selects which transformers fire for a given (kind, action) pair.
// Action=="" is a wildcard that matches any action within the kind.
type Key struct {
	Kind   string // "source", "dag", "matrix", …
	Action string // "ALLOW", "DENY", "CONVERT", "EXPAND", or "" wildcard
}

func (k Key) String() string {
	a := k.Action
	if a == "" {
		a = "*"
	}
	return fmt.Sprintf("%s/%s", k.Kind, a)
}

// ─── Registry ─────────────────────────────────────────────────────────────────

// Registry maps Keys to ordered transformer chains.
// Register is safe to call concurrently with Get; the internal lock ensures
// visibility. Registered transformers are appended; order is preserved.
type Registry struct {
	mu      sync.RWMutex
	entries map[Key][]Transformer

	// telemetry
	tracer    trace.Tracer
	applied   metric.Int64Counter
	appliedMs metric.Float64Histogram
	errors    metric.Int64Counter
}

// NewRegistry creates an empty Registry with OTEL instruments.
func NewRegistry() (*Registry, error) {
	m := polOtel.Meter("transform")
	prefix := polOtel.Namespace + ".transform"

	app, err := m.Int64Counter(prefix+".applied_total",
		metric.WithDescription("Transform Apply invocations"))
	if err != nil {
		return nil, fmt.Errorf("transform: metric applied: %w", err)
	}
	ms, err := m.Float64Histogram(prefix+".duration_ms",
		metric.WithDescription("Transform Apply latency"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.5, 1, 5, 10, 25),
	)
	if err != nil {
		return nil, fmt.Errorf("transform: metric duration: %w", err)
	}
	errs, err := m.Int64Counter(prefix+".errors_total",
		metric.WithDescription("Transform Apply errors"))
	if err != nil {
		return nil, fmt.Errorf("transform: metric errors: %w", err)
	}

	return &Registry{
		entries: make(map[Key][]Transformer),
		tracer:  polOtel.Tracer("transform"),
		applied: app, appliedMs: ms, errors: errs,
	}, nil
}

// Register appends ts to the chain for key. Returns r for chaining.
func (r *Registry) Register(key Key, ts ...Transformer) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[key] = append(r.entries[key], ts...)
	return r
}

// Get returns the transformer chain for key.
// Specific-action entries are returned before wildcard-action entries.
// Returns nil when no transformers are registered.
func (r *Registry) Get(key Key) []Transformer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	specific := r.entries[key]
	wildcard := r.entries[Key{Kind: key.Kind, Action: ""}]

	total := len(specific) + len(wildcard)
	if total == 0 {
		return nil
	}
	out := make([]Transformer, 0, total)
	out = append(out, specific...)
	out = append(out, wildcard...)
	return out
}

// ApplyAll runs the full transformer chain for key against (input, dec).
// It applies transformers sequentially; the output of each becomes the
// input of the next. Failures return the last good decision along with the error.
func (r *Registry) ApplyAll(ctx context.Context, key Key, input any, dec Decision) (Decision, error) {
	ctx, span := r.tracer.Start(ctx, polOtel.Namespace+".transform.apply_all",
		trace.WithAttributes(
			attribute.String("key", key.String()),
		),
	)
	defer span.End()

	transformers := r.Get(key)
	if len(transformers) == 0 {
		return dec, nil
	}

	cur := dec.Clone()
	for _, t := range transformers {
		attrs := metric.WithAttributes(
			attribute.String("transformer", t.Name()),
			attribute.String("key", key.String()),
		)
		start := time.Now()
		next, err := t.Apply(ctx, input, cur)
		elapsed := float64(time.Since(start).Microseconds()) / 1000.0
		r.applied.Add(ctx, 1, attrs)
		r.appliedMs.Record(ctx, elapsed, attrs)

		if err != nil {
			r.errors.Add(ctx, 1, attrs)
			polOtel.RecordError(ctx, err)
			return cur, fmt.Errorf("transform %q: %w", t.Name(), err)
		}
		cur = next
	}
	return cur, nil
}

// ─── Functional helpers ───────────────────────────────────────────────────────

// Func wraps a plain function as a named Transformer.
func Func(name string, fn func(ctx context.Context, input any, dec Decision) (Decision, error)) Transformer {
	return &funcTransformer{name: name, fn: fn}
}

type funcTransformer struct {
	name string
	fn   func(ctx context.Context, input any, dec Decision) (Decision, error)
}

func (f *funcTransformer) Name() string { return f.name }
func (f *funcTransformer) Apply(ctx context.Context, input any, dec Decision) (Decision, error) {
	return f.fn(ctx, input, dec)
}

// Chain composes multiple transformers into one, stopping on the first error.
func Chain(name string, ts ...Transformer) Transformer {
	return Func(name, func(ctx context.Context, input any, dec Decision) (Decision, error) {
		cur := dec
		for _, t := range ts {
			next, err := t.Apply(ctx, input, cur)
			if err != nil {
				return cur, fmt.Errorf("%s → %s: %w", name, t.Name(), err)
			}
			cur = next
		}
		return cur, nil
	})
}

// Guard wraps t so it only fires when pred(dec) returns true.
func Guard(name string, pred func(Decision) bool, t Transformer) Transformer {
	return Func(name, func(ctx context.Context, input any, dec Decision) (Decision, error) {
		if !pred(dec) {
			return dec, nil
		}
		return t.Apply(ctx, input, dec)
	})
}

// ActionGuard is a convenience Guard that fires only when dec.Action == action.
func ActionGuard(action string, t Transformer) Transformer {
	return Guard(t.Name()+".guard", func(d Decision) bool { return d.Action == action }, t)
}

package llb

import "context"

// ─────────────────────────────────────────────────────────────────────────────
// StateTransformer — strategy interface for composable state transformations
// ─────────────────────────────────────────────────────────────────────────────

// StateTransformer is the strategy interface for stateless, composable state
// transformations. Unlike StateOption (which is a function type), Transformer
// is an interface that supports richer implementations (with state, lifecycle,
// validation) while maintaining the same composability.
//
// All implementations must be safe for concurrent use.
type StateTransformer interface {
	// Transform applies a transformation to the given State, returning a new
	// State or an error.
	Transform(ctx context.Context, s State) (State, error)
}

// StateTransformerFunc adapts a plain function to the StateTransformer interface.
type StateTransformerFunc func(ctx context.Context, s State) (State, error)

// Transform implements StateTransformer.
func (f StateTransformerFunc) Transform(ctx context.Context, s State) (State, error) {
	return f(ctx, s)
}

// ─────────────────────────────────────────────────────────────────────────────
// Noop
// ─────────────────────────────────────────────────────────────────────────────

// NoopTransformer returns the state unchanged. Useful as a default or sentinel
// in configuration where a transformer slot must be filled.
type NoopTransformer struct{}

// Transform implements StateTransformer.
func (NoopTransformer) Transform(_ context.Context, s State) (State, error) {
	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Chain / Multi composites
// ─────────────────────────────────────────────────────────────────────────────

// ChainTransformer applies a sequence of transformers in order. Each
// transformer receives the output of the previous one. If any transformer
// returns an error, the chain stops and that error is returned.
type ChainTransformer []StateTransformer

// Transform implements StateTransformer.
func (c ChainTransformer) Transform(ctx context.Context, s State) (State, error) {
	var err error
	for _, t := range c {
		s, err = t.Transform(ctx, s)
		if err != nil {
			return State{}, err
		}
	}
	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Predicate routing
// ─────────────────────────────────────────────────────────────────────────────

// StatePredicate decides whether a StateTransformer should be applied.
type StatePredicate func(s State) bool

// PredicateTransformer wraps a StateTransformer behind a predicate gate. The
// inner transformer is only invoked when the predicate returns true; otherwise
// the state passes through unchanged.
type PredicateTransformer struct {
	Predicate   StatePredicate
	Transformer StateTransformer
}

// Transform implements StateTransformer.
func (p PredicateTransformer) Transform(ctx context.Context, s State) (State, error) {
	if p.Predicate(s) {
		return p.Transformer.Transform(ctx, s)
	}
	return s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Predicate constructors
// ─────────────────────────────────────────────────────────────────────────────

// OnlyScratch matches states with nil output (scratch states).
func OnlyScratch() StatePredicate {
	return func(s State) bool { return s.Output() == nil }
}

// OnlyNonScratch matches states with a non-nil output.
func OnlyNonScratch() StatePredicate {
	return func(s State) bool { return s.Output() != nil }
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience: Apply
// ─────────────────────────────────────────────────────────────────────────────

// Apply applies a sequence of StateTransformers to the given State.
func Apply(ctx context.Context, s State, transformers ...StateTransformer) (State, error) {
	return ChainTransformer(transformers).Transform(ctx, s)
}

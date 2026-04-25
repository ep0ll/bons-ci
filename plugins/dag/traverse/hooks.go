// Package traverse provides a fully hookable traversal engine for DAGs.
//
// Design goals:
//   - Any vertex can be the starting point of a traversal
//   - All traversal control is exposed via hooks; this package has no opinions
//     about what to do when visiting a vertex
//   - Hooks are composable: multiple hooks can be combined into one
//   - No hidden state; each traversal is an independent call
//
// External packages (cache checkers, digest computers, UI renderers, OTEL
// tracers, schedulers) implement their logic entirely in hook functions without
// modifying or subclassing anything in this package.
package traverse

import (
	"context"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// Action is the control signal returned from every hook function.
// It tells the traversal engine what to do next.
type Action int

const (
	// Continue is the default: proceed normally.
	Continue Action = iota

	// Skip tells the engine to skip this vertex's subtree (inputs).
	// PreVisit returning Skip means the vertex itself is still "visited"
	// (PostVisit will NOT be called), but none of its inputs are traversed.
	// This is useful for cache-hit short-circuiting: if a vertex's output
	// is already available, there's no need to walk its dependency tree.
	Skip

	// Stop halts the entire traversal immediately.
	// No further vertices or edges will be visited.
	Stop
)

// Context carries traversal-scoped state through hook calls.
// It is separate from context.Context to avoid polluting the standard
// context with traversal internals.
type Context struct {
	// Depth is the distance from the traversal's start vertex.
	// The start vertex itself has Depth = 0.
	Depth int

	// Path is the sequence of vertices from the start vertex to the current one.
	// Path[0] is always the start vertex; Path[len-1] is the current vertex.
	// The slice is a copy; callers may retain it.
	Path []vertex.Vertex

	// Parent is the vertex whose input edge led to the current vertex.
	// Nil for the start vertex.
	Parent vertex.Vertex
}

// PreVisitFn is called before a vertex's inputs are traversed.
// Returning Skip prevents input traversal for this vertex but continues
// the overall traversal of siblings.
// Returning Stop halts all traversal.
type PreVisitFn func(ctx context.Context, tc Context, v vertex.Vertex) (Action, error)

// PostVisitFn is called after a vertex's inputs have been fully traversed.
// It is not called if PreVisit returned Skip for this vertex.
type PostVisitFn func(ctx context.Context, tc Context, v vertex.Vertex) (Action, error)

// EdgeFn is called for each edge traversal (parent → child/input).
// It is called after PreVisit for the parent but before descending into the child.
// Returning Skip skips the child's subtree without affecting siblings.
type EdgeFn func(ctx context.Context, parent, child vertex.Vertex, index int) (Action, error)

// Hooks is the complete set of optional callbacks for a traversal.
// All fields are optional; nil hooks are ignored.
//
// Hooks are applied in this order for each vertex:
//  1. PreVisit(vertex)
//  2. [if PreVisit returned Continue] for each input:
//     a. OnEdge(vertex, input, index)
//     b. [recurse into input if OnEdge returned Continue]
//  3. PostVisit(vertex)   [not called if PreVisit returned Skip]
//
// Hooks must be safe for the traversal mode in use. DFS is single-threaded by
// default; BFS is also single-threaded. For parallel traversal, hooks must be
// concurrent-safe and the engine must be configured with a concurrency limit.
type Hooks struct {
	// PreVisit is called when a vertex is first encountered, before its inputs
	// are traversed. Ideal for: opening spans, checking cache, emitting logs.
	PreVisit PreVisitFn

	// PostVisit is called after all inputs have been fully processed.
	// Ideal for: closing spans, finalizing digests, emitting completion events.
	PostVisit PostVisitFn

	// OnEdge is called for each directed edge (parent depends on child).
	// Ideal for: recording edge metadata, conditional sub-tree pruning.
	OnEdge EdgeFn
}

// Compose returns a new Hooks that runs a first, then b (chaining their results).
// The combined hook returns the most restrictive Action:
// Stop > Skip > Continue.
//
// This allows multiple independent concerns (e.g. tracing + caching + logging)
// to be combined without knowing about each other.
func (a Hooks) Compose(b Hooks) Hooks {
	return Hooks{
		PreVisit:  composePreVisit(a.PreVisit, b.PreVisit),
		PostVisit: composePostVisit(a.PostVisit, b.PostVisit),
		OnEdge:    composeEdge(a.OnEdge, b.OnEdge),
	}
}

func composePreVisit(a, b PreVisitFn) PreVisitFn {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func(ctx context.Context, tc Context, v vertex.Vertex) (Action, error) {
		act, err := a(ctx, tc, v)
		if err != nil || act == Stop {
			return act, err
		}
		actB, err := b(ctx, tc, v)
		if err != nil {
			return Stop, err
		}
		return mostRestrictive(act, actB), nil
	}
}

func composePostVisit(a, b PostVisitFn) PostVisitFn {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func(ctx context.Context, tc Context, v vertex.Vertex) (Action, error) {
		act, err := a(ctx, tc, v)
		if err != nil || act == Stop {
			return act, err
		}
		actB, err := b(ctx, tc, v)
		if err != nil {
			return Stop, err
		}
		return mostRestrictive(act, actB), nil
	}
}

func composeEdge(a, b EdgeFn) EdgeFn {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func(ctx context.Context, parent, child vertex.Vertex, index int) (Action, error) {
		act, err := a(ctx, parent, child, index)
		if err != nil || act == Stop {
			return act, err
		}
		actB, err := b(ctx, parent, child, index)
		if err != nil {
			return Stop, err
		}
		return mostRestrictive(act, actB), nil
	}
}

// mostRestrictive returns the more restrictive of two Actions.
// Stop > Skip > Continue.
func mostRestrictive(a, b Action) Action {
	if a == Stop || b == Stop {
		return Stop
	}
	if a == Skip || b == Skip {
		return Skip
	}
	return Continue
}

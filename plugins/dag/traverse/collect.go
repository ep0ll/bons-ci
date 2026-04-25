package traverse

import (
	"context"
	"errors"
	"fmt"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── CollectAll ───────────────────────────────────────────────────────────────

// CollectAll returns every vertex reachable from start in DFS post-order
// (inputs before the vertices that depend on them). Diamond dependencies are
// deduplicated — each vertex appears exactly once.
func CollectAll(ctx context.Context, start vertex.Vertex) ([]vertex.Vertex, error) {
	var out []vertex.Vertex
	eng := New(DFS, Hooks{
		PostVisit: func(_ context.Context, _ Context, v vertex.Vertex) (Action, error) {
			out = append(out, v)
			return Continue, nil
		},
	})
	if err := eng.TraverseUpstream(ctx, start); err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}
	return out, nil
}

// CollectAllFromDAG returns all vertices in topological order from the prebuilt DAG.
func CollectAllFromDAG(dag *graph.DAG) []vertex.Vertex {
	return dag.TopologicalOrder()
}

// ─── FindByKind ───────────────────────────────────────────────────────────────

// FindByKind returns all vertices reachable from start whose Kind matches kind.
func FindByKind(ctx context.Context, start vertex.Vertex, kind vertex.Kind) ([]vertex.Vertex, error) {
	return Collect(ctx, start, func(v vertex.Vertex) bool { return v.Kind() == kind })
}

// ─── Collect ─────────────────────────────────────────────────────────────────

// Collect returns all vertices reachable from start that satisfy predicate,
// in DFS post-order.
func Collect(ctx context.Context, start vertex.Vertex, predicate func(vertex.Vertex) bool) ([]vertex.Vertex, error) {
	var out []vertex.Vertex
	eng := New(DFS, Hooks{
		PostVisit: func(_ context.Context, _ Context, v vertex.Vertex) (Action, error) {
			if predicate(v) {
				out = append(out, v)
			}
			return Continue, nil
		},
	})
	if err := eng.TraverseUpstream(ctx, start); err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}
	return out, nil
}

// ─── Find ────────────────────────────────────────────────────────────────────

// Find returns the first vertex reachable from start that satisfies predicate
// (DFS pre-order). Returns nil if no vertex matches.
func Find(ctx context.Context, start vertex.Vertex, predicate func(vertex.Vertex) bool) (vertex.Vertex, error) {
	var found vertex.Vertex
	eng := New(DFS, Hooks{
		PreVisit: func(_ context.Context, _ Context, v vertex.Vertex) (Action, error) {
			if predicate(v) {
				found = v
				return Stop, nil
			}
			return Continue, nil
		},
	})
	if err := eng.TraverseUpstream(ctx, start); err != nil {
		return nil, fmt.Errorf("find: %w", err)
	}
	return found, nil
}

// ─── Count ───────────────────────────────────────────────────────────────────

// Count returns the number of distinct vertices reachable from start.
func Count(ctx context.Context, start vertex.Vertex) (int, error) {
	n := 0
	eng := New(DFS, Hooks{
		PreVisit: func(_ context.Context, _ Context, _ vertex.Vertex) (Action, error) {
			n++
			return Continue, nil
		},
	})
	if err := eng.TraverseUpstream(ctx, start); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// ─── MaxDepth ────────────────────────────────────────────────────────────────

// MaxDepth returns the longest path from start to any leaf (depth of the
// deepest reachable vertex). A single vertex with no inputs has depth 0.
func MaxDepth(ctx context.Context, start vertex.Vertex) (int, error) {
	maxD := 0
	eng := New(DFS, Hooks{
		PreVisit: func(_ context.Context, tc Context, _ vertex.Vertex) (Action, error) {
			if tc.Depth > maxD {
				maxD = tc.Depth
			}
			return Continue, nil
		},
	})
	if err := eng.TraverseUpstream(ctx, start); err != nil {
		return 0, fmt.Errorf("max depth: %w", err)
	}
	return maxD, nil
}

// ─── AllPaths ────────────────────────────────────────────────────────────────

// AllPaths returns every distinct path from start to target (inclusive) found
// by following inputs. Each path is ordered start→…→target.
//
// This is an exhaustive DFS and may be expensive on large graphs.
// Use it for diagnostics and debugging only.
func AllPaths(ctx context.Context, start, target vertex.Vertex) ([][]vertex.Vertex, error) {
	if start == nil || target == nil {
		return nil, nil
	}
	var paths [][]vertex.Vertex
	var currentPath []vertex.Vertex

	var dfs func(v vertex.Vertex) error
	dfs = func(v vertex.Vertex) error {
		currentPath = append(currentPath, v)
		if v.ID() == target.ID() {
			p := make([]vertex.Vertex, len(currentPath))
			copy(p, currentPath)
			paths = append(paths, p)
		} else {
			for _, inp := range v.Inputs() {
				if inp == nil {
					continue
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := dfs(inp); err != nil {
					return err
				}
			}
		}
		currentPath = currentPath[:len(currentPath)-1]
		return nil
	}

	if err := dfs(start); err != nil {
		return nil, fmt.Errorf("all paths: %w", err)
	}
	return paths, nil
}

// ─── WalkFunc ─────────────────────────────────────────────────────────────────

// WalkFunc calls fn for each vertex reachable from start in DFS post-order.
//
// BUG FIX: the previous implementation wrapped fn's errors in a traversal
// frame ("traverse: PostVisit error…"), which broke errors.Is/errors.As
// for caller-defined sentinel errors.
//
// Fix: fn errors are returned unwrapped so that errors.Is works correctly
// for callers using sentinel errors (e.g. io.EOF to signal early termination).
func WalkFunc(ctx context.Context, start vertex.Vertex, fn func(ctx context.Context, v vertex.Vertex) error) error {
	eng := New(DFS, Hooks{
		PostVisit: func(ctx context.Context, _ Context, v vertex.Vertex) (Action, error) {
			err := fn(ctx, v)
			if err != nil {
				// Return the user error directly, unwrapped.
				// The engine will see a non-nil error and propagate it.
				return Stop, err
			}
			return Continue, nil
		},
	})
	return eng.TraverseUpstream(ctx, start)
}

// ─── DigestWalker ────────────────────────────────────────────────────────────

// DigestWalker collects a content digest per vertex using PostVisit hooks.
// It demonstrates the pattern for bottom-up computation (all inputs must be
// computed before the current vertex).
//
// This is a demonstration skeleton — real digest computation would hash the
// vertex's kind, ID, and the digests of its inputs.
type DigestWalker struct {
	Digests map[string]string
}

func NewDigestWalker() *DigestWalker {
	return &DigestWalker{Digests: make(map[string]string)}
}

func (dw *DigestWalker) Hooks() Hooks {
	return Hooks{
		PostVisit: func(_ context.Context, _ Context, v vertex.Vertex) (Action, error) {
			// Skeleton: use the vertex's own ID prefix as a stand-in.
			id := v.ID()
			n := len(id)
			if n > 8 {
				n = 8
			}
			dw.Digests[v.ID()] = "digest:" + id[:n]
			return Continue, nil
		},
	}
}

// ─── TracingHooks ────────────────────────────────────────────────────────────

// SpanEvent is emitted for each traversal event and consumed by tracing adapters.
type SpanEvent struct {
	Type   string // "pre", "post", or "edge"
	Vertex vertex.Vertex
	Parent vertex.Vertex // set for "edge" events
	Depth  int
}

// TracingHooks emits SpanEvents to a channel for every traversal event.
// This lets external OTEL/tracing adapters hook into traversal without
// modifying the engine.
//
// The caller is responsible for closing the channel after traversal completes.
func TracingHooks(events chan<- SpanEvent) Hooks {
	return Hooks{
		PreVisit: func(_ context.Context, tc Context, v vertex.Vertex) (Action, error) {
			events <- SpanEvent{Type: "pre", Vertex: v, Depth: tc.Depth}
			return Continue, nil
		},
		PostVisit: func(_ context.Context, tc Context, v vertex.Vertex) (Action, error) {
			events <- SpanEvent{Type: "post", Vertex: v, Depth: tc.Depth}
			return Continue, nil
		},
		OnEdge: func(_ context.Context, parent, child vertex.Vertex, _ int) (Action, error) {
			events <- SpanEvent{Type: "edge", Vertex: child, Parent: parent}
			return Continue, nil
		},
	}
}

// ─── CachingHooks ────────────────────────────────────────────────────────────

// CacheResult holds the outcome of a cache lookup for one vertex.
type CacheResult struct {
	Hit bool
	Key string
}

// CacheCheckFn is called once per vertex to check whether its output is cached.
// Returning Hit=true causes the engine to skip the entire input subtree of that
// vertex — the primary mechanism for incremental / cached builds.
type CacheCheckFn func(ctx context.Context, v vertex.Vertex) (CacheResult, error)

// CachingHooks builds a Hooks set integrating a cache-check function.
//
// If hits is non-nil, every cache check result is recorded in it regardless
// of whether it was a hit or miss.
func CachingHooks(check CacheCheckFn, hits map[string]CacheResult) Hooks {
	return Hooks{
		PreVisit: func(ctx context.Context, _ Context, v vertex.Vertex) (Action, error) {
			result, err := check(ctx, v)
			if err != nil {
				return Stop, fmt.Errorf("cache check for %q: %w", v.ID(), err)
			}
			if hits != nil {
				hits[v.ID()] = result
			}
			if result.Hit {
				return Skip, nil
			}
			return Continue, nil
		},
	}
}

// ensure errors package import is used
var _ = errors.New

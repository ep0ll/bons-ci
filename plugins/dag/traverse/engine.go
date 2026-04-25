package traverse

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// Mode controls the traversal algorithm.
type Mode int

const (
	// DFS (Depth-First Search) follows inputs as deep as possible before
	// backtracking. This matches how most build systems evaluate dependencies:
	// resolve the deepest input first, work your way back up.
	DFS Mode = iota

	// BFS (Breadth-First Search) visits all vertices at the current depth
	// before going deeper. Useful for level-by-level scheduling.
	BFS
)

// Direction controls which edges are followed during traversal.
type Direction int

const (
	// Upstream follows input edges (v → inputs).
	// This is the primary direction and works without a DAG object.
	Upstream Direction = iota

	// Downstream follows dependent edges (v → vertices that depend on v).
	// Requires a DAG to be provided because raw Vertex objects do not
	// expose their dependents — only their inputs.
	Downstream
)

// Engine executes graph traversals.
//
// It is stateless — all state is local to each Traverse call — so a single
// Engine may be used concurrently for independent traversals.
type Engine struct {
	mode  Mode
	hooks Hooks
}

// New returns a new Engine with the given traversal mode and hooks.
// All hooks are optional; pass Hooks{} for a pure walk with no callbacks.
func New(mode Mode, hooks Hooks) *Engine {
	return &Engine{mode: mode, hooks: hooks}
}

// WithHooks returns a new Engine with the same mode but with additional hooks
// composed on top of the existing ones.
func (e *Engine) WithHooks(extra Hooks) *Engine {
	return &Engine{
		mode:  e.mode,
		hooks: e.hooks.Compose(extra),
	}
}

// TraverseUpstream traverses the input/dependency tree rooted at start.
// It follows Vertex.Inputs() edges (upstream direction).
//
// Every vertex reachable by following inputs from start is visited exactly once,
// regardless of how many vertices depend on it (diamond dependencies are handled
// correctly via a visited set).
//
// The traversal is single-threaded and returns the first error encountered.
func (e *Engine) TraverseUpstream(ctx context.Context, start vertex.Vertex) error {
	if start == nil {
		return nil
	}
	visited := make(map[string]bool)
	var path []vertex.Vertex

	var err error
	switch e.mode {
	case DFS:
		err = e.dfsUpstream(ctx, start, nil, 0, visited, path)
	case BFS:
		err = e.bfsUpstream(ctx, start, visited)
	default:
		return fmt.Errorf("traverse: unknown mode %d", e.mode)
	}
	// Stop is a control signal, not a caller-visible error.
	if err == errStop {
		return nil
	}
	return err
}

// TraverseDownstream traverses the dependent tree from start, following edges
// in the downstream direction (which vertices depend on start).
//
// A DAG is required because raw Vertex objects do not expose their dependents.
func (e *Engine) TraverseDownstream(ctx context.Context, start vertex.Vertex, dag *graph.DAG) error {
	if start == nil {
		return nil
	}
	if dag == nil {
		return fmt.Errorf("traverse: DAG is required for downstream traversal")
	}
	visited := make(map[string]bool)
	var err error
	switch e.mode {
	case DFS:
		err = e.dfsDownstream(ctx, start, nil, 0, visited, dag, []vertex.Vertex{})
	case BFS:
		err = e.bfsDownstream(ctx, start, visited, dag)
	default:
		return fmt.Errorf("traverse: unknown mode %d", e.mode)
	}
	if err == errStop {
		return nil
	}
	return err
}

// ─── DFS upstream ────────────────────────────────────────────────────────────

func (e *Engine) dfsUpstream(
	ctx context.Context,
	v vertex.Vertex,
	parent vertex.Vertex,
	depth int,
	visited map[string]bool,
	path []vertex.Vertex,
) error {
	id := v.ID()
	if visited[id] {
		return nil // already processed (diamond dependency)
	}

	path = append(path, v)
	tc := Context{Depth: depth, Path: copyPath(path), Parent: parent}

	// PreVisit
	if e.hooks.PreVisit != nil {
		act, err := e.hooks.PreVisit(ctx, tc, v)
		if err != nil {
			return fmt.Errorf("traverse: PreVisit error at depth %d (vertex %q): %w", depth, id, err)
		}
		switch act {
		case Stop:
			return errStop
		case Skip:
			visited[id] = true
			return nil
		}
	}

	visited[id] = true

	// Traverse inputs.
	inputs := v.Inputs()
	for i, inp := range inputs {
		if inp == nil {
			continue
		}
		// OnEdge
		if e.hooks.OnEdge != nil {
			act, err := e.hooks.OnEdge(ctx, v, inp, i)
			if err != nil {
				return fmt.Errorf("traverse: OnEdge error (parent %q → child %q): %w", id, inp.ID(), err)
			}
			switch act {
			case Stop:
				return errStop
			case Skip:
				continue // skip this input's subtree
			}
		}

		if err := e.dfsUpstream(ctx, inp, v, depth+1, visited, path); err != nil {
			if err == errStop {
				return errStop
			}
			return err
		}
	}

	// PostVisit
	if e.hooks.PostVisit != nil {
		act, err := e.hooks.PostVisit(ctx, tc, v)
		if err != nil {
			return fmt.Errorf("traverse: PostVisit error at depth %d (vertex %q): %w", depth, id, err)
		}
		if act == Stop {
			return errStop
		}
	}

	return nil
}

// ─── BFS upstream ────────────────────────────────────────────────────────────

type bfsItem struct {
	v      vertex.Vertex
	parent vertex.Vertex
	depth  int
}

func (e *Engine) bfsUpstream(
	ctx context.Context,
	start vertex.Vertex,
	visited map[string]bool,
) error {
	queue := []bfsItem{{v: start, parent: nil, depth: 0}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		id := item.v.ID()
		if visited[id] {
			continue
		}

		tc := Context{Depth: item.depth, Parent: item.parent}

		// PreVisit
		skip := false
		if e.hooks.PreVisit != nil {
			act, err := e.hooks.PreVisit(ctx, tc, item.v)
			if err != nil {
				return fmt.Errorf("traverse: PreVisit error at depth %d: %w", item.depth, err)
			}
			switch act {
			case Stop:
				return nil
			case Skip:
				skip = true
			}
		}

		visited[id] = true

		if !skip {
			for i, inp := range item.v.Inputs() {
				if inp == nil {
					continue
				}
				edgeSkip := false
				if e.hooks.OnEdge != nil {
					act, err := e.hooks.OnEdge(ctx, item.v, inp, i)
					if err != nil {
						return err
					}
					switch act {
					case Stop:
						return nil
					case Skip:
						edgeSkip = true
					}
				}
				if !edgeSkip && !visited[inp.ID()] {
					queue = append(queue, bfsItem{v: inp, parent: item.v, depth: item.depth + 1})
				}
			}

			// PostVisit
			if e.hooks.PostVisit != nil {
				act, err := e.hooks.PostVisit(ctx, tc, item.v)
				if err != nil {
					return err
				}
				if act == Stop {
					return nil
				}
			}
		}
	}
	return nil
}

// ─── DFS downstream ──────────────────────────────────────────────────────────

func (e *Engine) dfsDownstream(
	ctx context.Context,
	v vertex.Vertex,
	parent vertex.Vertex,
	depth int,
	visited map[string]bool,
	dag *graph.DAG,
	path []vertex.Vertex,
) error {
	id := v.ID()
	if visited[id] {
		return nil
	}

	path = append(path, v)
	tc := Context{Depth: depth, Path: copyPath(path), Parent: parent}

	if e.hooks.PreVisit != nil {
		act, err := e.hooks.PreVisit(ctx, tc, v)
		if err != nil {
			return err
		}
		switch act {
		case Stop:
			return errStop
		case Skip:
			visited[id] = true
			return nil
		}
	}

	visited[id] = true

	for i, dep := range dag.Downstream(v) {
		if e.hooks.OnEdge != nil {
			act, err := e.hooks.OnEdge(ctx, v, dep, i)
			if err != nil {
				return err
			}
			switch act {
			case Stop:
				return errStop
			case Skip:
				continue
			}
		}
		if err := e.dfsDownstream(ctx, dep, v, depth+1, visited, dag, path); err != nil {
			return err
		}
	}

	if e.hooks.PostVisit != nil {
		act, err := e.hooks.PostVisit(ctx, tc, v)
		if err != nil {
			return err
		}
		if act == Stop {
			return errStop
		}
	}
	return nil
}

// ─── BFS downstream ──────────────────────────────────────────────────────────

func (e *Engine) bfsDownstream(
	ctx context.Context,
	start vertex.Vertex,
	visited map[string]bool,
	dag *graph.DAG,
) error {
	queue := []bfsItem{{v: start, parent: nil, depth: 0}}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		id := item.v.ID()
		if visited[id] {
			continue
		}
		tc := Context{Depth: item.depth, Parent: item.parent}
		skip := false
		if e.hooks.PreVisit != nil {
			act, err := e.hooks.PreVisit(ctx, tc, item.v)
			if err != nil {
				return err
			}
			switch act {
			case Stop:
				return nil
			case Skip:
				skip = true
			}
		}
		visited[id] = true
		if !skip {
			for i, dep := range dag.Downstream(item.v) {
				if e.hooks.OnEdge != nil {
					act, err := e.hooks.OnEdge(ctx, item.v, dep, i)
					if err != nil {
						return err
					}
					switch act {
					case Stop:
						return nil
					case Skip:
						continue
					}
				}
				if !visited[dep.ID()] {
					queue = append(queue, bfsItem{v: dep, parent: item.v, depth: item.depth + 1})
				}
			}
			if e.hooks.PostVisit != nil {
				act, err := e.hooks.PostVisit(ctx, tc, item.v)
				if err != nil {
					return err
				}
				if act == Stop {
					return nil
				}
			}
		}
	}
	return nil
}

// errStop is a sentinel used internally to propagate Stop without wrapping it
// in an error (avoiding "stop is not an error" confusion at call sites).
var errStop = fmt.Errorf("traverse: stop")

func copyPath(path []vertex.Vertex) []vertex.Vertex {
	out := make([]vertex.Vertex, len(path))
	copy(out, path)
	return out
}

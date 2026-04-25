package graph

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// Builder constructs an immutable DAG.
//
// Usage:
//
//	b := graph.NewBuilder()
//	if err := b.AddTree(ctx, rootVertex); err != nil {
//	    return err
//	}
//	dag, err := b.Build()
//
// AddTree recursively walks a vertex's input tree and registers all reachable
// vertices. This is the most common entry point — callers only need to know
// the root(s) they care about.
//
// Alternatively, use Add to register vertices one at a time (in any order)
// and let Build sort them.
type Builder struct {
	vertices map[string]vertex.Vertex
}

// NewBuilder returns a ready-to-use Builder.
func NewBuilder() *Builder {
	return &Builder{
		vertices: make(map[string]vertex.Vertex),
	}
}

// Has reports whether the builder already contains a vertex with the given ID.
func (b *Builder) Has(id string) bool {
	_, ok := b.vertices[id]
	return ok
}

// Add registers a single vertex.
// Returns an error if a different vertex with the same ID was already added
// (same ID with pointer equality is a no-op and returns nil).
//
// Add is safe to call on a zero-value Builder (i.e. &Builder{} or Builder{}).
// Prefer NewBuilder() for clarity, but the zero value is deliberately valid.
func (b *Builder) Add(v vertex.Vertex) error {
	if v == nil {
		return fmt.Errorf("graph: cannot add nil vertex")
	}
	id := v.ID()
	if id == "" {
		return fmt.Errorf("graph: vertex of kind %q has an empty ID", v.Kind())
	}
	// Lazy init ensures the zero-value Builder is safe to use without NewBuilder().
	if b.vertices == nil {
		b.vertices = make(map[string]vertex.Vertex)
	}
	if existing, ok := b.vertices[id]; ok {
		if existing == v {
			return nil // idempotent
		}
		// Two different objects with the same ID would be a bug in the
		// ID computation of one of them.
		return fmt.Errorf("graph: ID collision — two distinct vertices share ID %q (kinds: %q vs %q)",
			id, existing.Kind(), v.Kind())
	}
	b.vertices[id] = v
	return nil
}

// AddTree recursively walks the input tree rooted at v, registers every
// reachable vertex (depth-first, inputs first), and validates each one.
//
// This is the recommended way to populate the builder: call AddTree once
// per root vertex and let it discover the entire dependency graph automatically.
//
// Cycles are detected during Build; AddTree itself only registers vertices.
func (b *Builder) AddTree(ctx context.Context, v vertex.Vertex) error {
	if v == nil {
		return nil // scratch/empty refs are legal
	}
	if b.Has(v.ID()) {
		return nil // already registered (diamond dependencies are fine)
	}
	// Validate before registering so errors surface early.
	if err := v.Validate(ctx); err != nil {
		name := v.ID()
		if n, ok := v.(vertex.Named); ok {
			name = n.Name()
		}
		return fmt.Errorf("graph: validation failed for vertex %q (%s): %w", name, v.Kind(), err)
	}
	// Walk inputs first (post-order traversal ensures inputs are in the map
	// before the vertex that depends on them).
	for _, inp := range v.Inputs() {
		if err := b.AddTree(ctx, inp); err != nil {
			return err
		}
	}
	return b.Add(v)
}

// Build validates the accumulated vertices, detects cycles, computes the
// topological order, identifies roots, and returns the immutable DAG.
//
// Build does not modify the builder; you may call it multiple times
// (though the result will be identical on subsequent calls if no vertices
// were added between calls).
func (b *Builder) Build() (*DAG, error) {
	if len(b.vertices) == 0 {
		// An empty DAG is valid (represents a scratch-only workflow).
		return &DAG{
			vertices:   make(map[string]vertex.Vertex),
			upstream:   make(map[string][]string),
			downstream: make(map[string][]string),
			order:      nil,
			roots:      nil,
		}, nil
	}

	// Build upstream adjacency from Inputs() — we use IDs, not pointers,
	// so the graph structure is independent of object identity.
	upstream := make(map[string][]string, len(b.vertices))
	for id, v := range b.vertices {
		inputs := v.Inputs()
		if len(inputs) == 0 {
			upstream[id] = nil
			continue
		}
		ids := make([]string, 0, len(inputs))
		for _, inp := range inputs {
			if inp == nil {
				continue // nil input = scratch, not a DAG edge
			}
			inpID := inp.ID()
			if !b.Has(inpID) {
				return nil, fmt.Errorf(
					"graph: vertex %q references input %q which was not registered — call AddTree instead of Add when the full tree is available",
					id, inpID,
				)
			}
			ids = append(ids, inpID)
		}
		upstream[id] = ids
	}

	// Detect cycles before computing topological order.
	if err := detectCycles(b.vertices, upstream); err != nil {
		return nil, err
	}

	// Compute topological order.
	order, err := topoSort(b.vertices, upstream)
	if err != nil {
		return nil, err
	}

	// Build downstream (reverse) adjacency.
	downstream := make(map[string][]string, len(b.vertices))
	for id, inputs := range upstream {
		for _, inp := range inputs {
			downstream[inp] = append(downstream[inp], id)
		}
	}

	// Identify roots: vertices that nothing else depends on.
	var roots []string
	for id := range b.vertices {
		if len(downstream[id]) == 0 {
			roots = append(roots, id)
		}
	}

	// Copy vertices map so the DAG is independent of the builder.
	verts := make(map[string]vertex.Vertex, len(b.vertices))
	for k, v := range b.vertices {
		verts[k] = v
	}

	return &DAG{
		vertices:   verts,
		upstream:   upstream,
		downstream: downstream,
		order:      order,
		roots:      roots,
	}, nil
}

// BuildFromRoot is a convenience function that creates a builder, registers
// the full tree rooted at root, and builds the DAG in one call.
func BuildFromRoot(ctx context.Context, root vertex.Vertex) (*DAG, error) {
	b := NewBuilder()
	if err := b.AddTree(ctx, root); err != nil {
		return nil, err
	}
	return b.Build()
}

// BuildFromRoots is like BuildFromRoot but accepts multiple roots.
// All vertices reachable from any root are included.
func BuildFromRoots(ctx context.Context, roots ...vertex.Vertex) (*DAG, error) {
	b := NewBuilder()
	for _, root := range roots {
		if err := b.AddTree(ctx, root); err != nil {
			return nil, err
		}
	}
	return b.Build()
}

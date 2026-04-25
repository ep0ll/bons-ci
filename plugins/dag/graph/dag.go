// Package graph provides an immutable Directed Acyclic Graph structure and a
// mutable Builder that constructs one.
//
// Responsibilities:
//   - Store vertices and their edges (upstream and downstream)
//   - Provide O(1) vertex lookup by ID
//   - Expose topological ordering for serialization
//   - Remain read-only after construction (safe for concurrent traversal)
//
// This package does NOT perform cycle detection — that is handled by validate.go.
// It does NOT understand operation semantics — that is handled by the ops package.
package graph

import (
	"fmt"
	"sort"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// DAG is an immutable directed acyclic graph of Vertex nodes.
//
// Edges are directed from a vertex to its Inputs (upstream / dependency direction).
// The downstream direction (which vertices depend on this one) is stored in the
// dependents map and is computed once during Build.
//
// DAG is safe for concurrent read access after construction.
type DAG struct {
	// vertices maps vertex ID → Vertex for O(1) lookup.
	vertices map[string]vertex.Vertex

	// upstream maps vertex ID → list of vertex IDs it depends on.
	// This mirrors Vertex.Inputs() but in ID form for graph algorithms.
	upstream map[string][]string

	// downstream maps vertex ID → list of vertex IDs that depend on it.
	// This is the reverse of upstream and enables "dependents" traversal.
	downstream map[string][]string

	// order is a topologically sorted list of vertex IDs (leaves first, roots last).
	// "leaf" here means a vertex with no inputs (e.g. a source op).
	// "root" means a vertex with no dependents — the final output(s).
	order []string

	// roots holds the IDs of vertices that nothing else depends on.
	roots []string
}

// Vertex returns the vertex with the given ID, or nil if not found.
func (d *DAG) Vertex(id string) vertex.Vertex {
	return d.vertices[id]
}

// Has reports whether the graph contains a vertex with the given ID.
func (d *DAG) Has(id string) bool {
	_, ok := d.vertices[id]
	return ok
}

// Vertices returns all vertices in the graph. The order is undefined;
// use TopologicalOrder for a stable sequence suitable for serialization.
func (d *DAG) Vertices() []vertex.Vertex {
	out := make([]vertex.Vertex, 0, len(d.vertices))
	for _, v := range d.vertices {
		out = append(out, v)
	}
	return out
}

// Len returns the number of vertices in the graph.
func (d *DAG) Len() int {
	return len(d.vertices)
}

// Roots returns the vertices that no other vertex depends on.
// In a typical build graph, there is one root representing the final output.
// Multiple roots are valid and represent independent final outputs.
func (d *DAG) Roots() []vertex.Vertex {
	out := make([]vertex.Vertex, 0, len(d.roots))
	for _, id := range d.roots {
		out = append(out, d.vertices[id])
	}
	return out
}

// TopologicalOrder returns all vertices in topological order — inputs (leaves)
// before the vertices that depend on them (roots last).
//
// The order is deterministic given the same graph structure. It is suitable
// for serialization: every vertex is guaranteed to appear after all its inputs,
// so a receiver can reconstruct dependencies purely by index references.
func (d *DAG) TopologicalOrder() []vertex.Vertex {
	out := make([]vertex.Vertex, len(d.order))
	for i, id := range d.order {
		out[i] = d.vertices[id]
	}
	return out
}

// Upstream returns the direct inputs of vertex v (the vertices v depends on).
// Returns nil if v is not in the graph.
func (d *DAG) Upstream(v vertex.Vertex) []vertex.Vertex {
	ids, ok := d.upstream[v.ID()]
	if !ok {
		return nil
	}
	return d.verticesForIDs(ids)
}

// Downstream returns the vertices that directly depend on v.
// Returns nil if v is not in the graph or nothing depends on it.
func (d *DAG) Downstream(v vertex.Vertex) []vertex.Vertex {
	ids, ok := d.downstream[v.ID()]
	if !ok {
		return nil
	}
	return d.verticesForIDs(ids)
}

func (d *DAG) verticesForIDs(ids []string) []vertex.Vertex {
	out := make([]vertex.Vertex, 0, len(ids))
	for _, id := range ids {
		if v, ok := d.vertices[id]; ok {
			out = append(out, v)
		}
	}
	return out
}

// Subgraph returns a new DAG containing only the vertices reachable from start
// by following inputs (upstream direction). The starting vertex is included.
// This is useful for extracting the dependency cone of a specific vertex.
func (d *DAG) Subgraph(start vertex.Vertex) (*DAG, error) {
	if !d.Has(start.ID()) {
		return nil, fmt.Errorf("graph: vertex %q not found in DAG", start.ID())
	}
	b := NewBuilder()
	var walk func(v vertex.Vertex)
	walk = func(v vertex.Vertex) {
		if b.Has(v.ID()) {
			return
		}
		// Add inputs first (topological order)
		for _, inp := range v.Inputs() {
			walk(inp)
		}
		// Builder.Add always succeeds for valid subgraph extractions since the
		// parent graph already validated cycles.
		_ = b.Add(v)
	}
	walk(start)
	return b.Build()
}

// topoSort computes a topological ordering of the vertices using Kahn's algorithm.
// It runs after all vertices are added and is called internally by Builder.Build.
// Returns an error only if a cycle is detected (which should never happen if
// Validate was called first, but this provides a safety net).
func topoSort(
	vertices map[string]vertex.Vertex,
	upstream map[string][]string,
) ([]string, error) {
	// Compute in-degree (number of incoming edges in upstream direction =
	// how many inputs each vertex has).
	inDegree := make(map[string]int, len(vertices))
	for id := range vertices {
		inDegree[id] = len(upstream[id])
	}

	// Queue starts with vertices that have no inputs (leaves/sources).
	queue := make([]string, 0, len(vertices))
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	// Sort for determinism.
	sort.Strings(queue)

	result := make([]string, 0, len(vertices))

	// Build reverse adjacency: for each vertex, who depends on it?
	reverse := make(map[string][]string, len(vertices))
	for id, inputs := range upstream {
		for _, inp := range inputs {
			reverse[inp] = append(reverse[inp], id)
		}
	}

	for len(queue) > 0 {
		// Pop the front (stable sort already applied).
		current := queue[0]
		queue = queue[1:]
		result = append(result, current)

		// For each vertex that depends on current, decrement its in-degree.
		dependents := reverse[current]
		sort.Strings(dependents) // determinism
		for _, dep := range dependents {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				// Insert in sorted order.
				i := sort.SearchStrings(queue, dep)
				queue = append(queue, "")
				copy(queue[i+1:], queue[i:])
				queue[i] = dep
			}
		}
	}

	if len(result) != len(vertices) {
		return nil, fmt.Errorf("graph: cycle detected — topological sort could not complete (%d/%d vertices processed)",
			len(result), len(vertices))
	}
	return result, nil
}

package graph

import (
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── Ancestor / Descendant queries ────────────────────────────────────────────

// IsAncestor reports whether ancestor is an ancestor of descendant in the DAG,
// i.e. whether descendant transitively depends on ancestor.
//
// A vertex is NOT considered an ancestor of itself.
func (d *DAG) IsAncestor(ancestor, descendant vertex.Vertex) bool {
	if ancestor == nil || descendant == nil {
		return false
	}
	if ancestor.ID() == descendant.ID() {
		return false
	}
	// Walk upstream from descendant; if we reach ancestor, it is an ancestor.
	visited := make(map[string]bool)
	var walk func(id string) bool
	walk = func(id string) bool {
		if visited[id] {
			return false
		}
		visited[id] = true
		for _, inputID := range d.upstream[id] {
			if inputID == ancestor.ID() {
				return true
			}
			if walk(inputID) {
				return true
			}
		}
		return false
	}
	return walk(descendant.ID())
}

// IsDescendant reports whether descendant transitively depends on ancestor.
// Equivalent to IsAncestor(ancestor, descendant).
func (d *DAG) IsDescendant(descendant, ancestor vertex.Vertex) bool {
	return d.IsAncestor(ancestor, descendant)
}

// ─── UpstreamCone / DownstreamCone ────────────────────────────────────────────

// UpstreamCone returns all vertices that v transitively depends on,
// i.e. all ancestors of v (not including v itself).
func (d *DAG) UpstreamCone(v vertex.Vertex) []vertex.Vertex {
	if v == nil || !d.Has(v.ID()) {
		return nil
	}
	visited := make(map[string]bool)
	visited[v.ID()] = true // exclude v itself
	var out []vertex.Vertex
	var walk func(id string)
	walk = func(id string) {
		for _, inputID := range d.upstream[id] {
			if !visited[inputID] {
				visited[inputID] = true
				out = append(out, d.vertices[inputID])
				walk(inputID)
			}
		}
	}
	walk(v.ID())
	return out
}

// DownstreamCone returns all vertices that transitively depend on v,
// i.e. all descendants of v (not including v itself).
func (d *DAG) DownstreamCone(v vertex.Vertex) []vertex.Vertex {
	if v == nil || !d.Has(v.ID()) {
		return nil
	}
	visited := make(map[string]bool)
	visited[v.ID()] = true
	var out []vertex.Vertex
	var walk func(id string)
	walk = func(id string) {
		for _, depID := range d.downstream[id] {
			if !visited[depID] {
				visited[depID] = true
				out = append(out, d.vertices[depID])
				walk(depID)
			}
		}
	}
	walk(v.ID())
	return out
}

// ─── Filter ───────────────────────────────────────────────────────────────────

// Filter returns a new DAG containing only the vertices that satisfy predicate,
// along with any edges between them that are preserved by the filter.
//
// Important: if a vertex is removed by the filter, its edges are also removed.
// The result may not be a single connected component.
func (d *DAG) Filter(predicate func(vertex.Vertex) bool) (*DAG, error) {
	b := NewBuilder()
	for _, v := range d.vertices {
		if predicate(v) {
			if err := b.Add(v); err != nil {
				return nil, err
			}
		}
	}

	// Build a filtered upstream map: only keep edges where both endpoints pass.
	filteredUpstream := make(map[string][]string, len(b.vertices))
	for id := range b.vertices {
		for _, inputID := range d.upstream[id] {
			if b.Has(inputID) {
				filteredUpstream[id] = append(filteredUpstream[id], inputID)
			}
		}
	}

	// Detect cycles in the filtered graph (should not happen if the source was acyclic).
	if err := detectCycles(b.vertices, filteredUpstream); err != nil {
		return nil, err
	}

	order, err := topoSort(b.vertices, filteredUpstream)
	if err != nil {
		return nil, err
	}

	downstream := make(map[string][]string)
	for id, inputs := range filteredUpstream {
		for _, inp := range inputs {
			downstream[inp] = append(downstream[inp], id)
		}
	}

	var roots []string
	for id := range b.vertices {
		if len(downstream[id]) == 0 {
			roots = append(roots, id)
		}
	}

	verts := make(map[string]vertex.Vertex, len(b.vertices))
	for k, v := range b.vertices {
		verts[k] = v
	}

	return &DAG{
		vertices:   verts,
		upstream:   filteredUpstream,
		downstream: downstream,
		order:      order,
		roots:      roots,
	}, nil
}

// ─── Walk helpers ─────────────────────────────────────────────────────────────

// VertexByID returns the vertex with the given ID, or nil.
// Identical to Vertex() but follows the more common naming pattern.
func (d *DAG) VertexByID(id string) vertex.Vertex {
	return d.vertices[id]
}

// AllVertices returns all vertices in the graph as a map (ID → Vertex).
// The returned map is a copy; callers may modify it freely.
func (d *DAG) AllVertices() map[string]vertex.Vertex {
	out := make(map[string]vertex.Vertex, len(d.vertices))
	for k, v := range d.vertices {
		out[k] = v
	}
	return out
}

// Leaves returns all vertices with no inputs (the "source" nodes of the DAG —
// those that depend on nothing else).
func (d *DAG) Leaves() []vertex.Vertex {
	var out []vertex.Vertex
	for id, v := range d.vertices {
		if len(d.upstream[id]) == 0 {
			out = append(out, v)
		}
	}
	return out
}

// CommonAncestors returns the set of vertices that are ancestors of BOTH a and b.
// This is useful for understanding shared dependencies between two vertices.
func (d *DAG) CommonAncestors(a, b vertex.Vertex) []vertex.Vertex {
	if a == nil || b == nil {
		return nil
	}

	ancestorsOf := func(start vertex.Vertex) map[string]bool {
		result := make(map[string]bool)
		var walk func(id string)
		walk = func(id string) {
			for _, inputID := range d.upstream[id] {
				if !result[inputID] {
					result[inputID] = true
					walk(inputID)
				}
			}
		}
		walk(start.ID())
		return result
	}

	aAnc := ancestorsOf(a)
	bAnc := ancestorsOf(b)

	var common []vertex.Vertex
	for id := range aAnc {
		if bAnc[id] {
			if v, ok := d.vertices[id]; ok {
				common = append(common, v)
			}
		}
	}
	return common
}

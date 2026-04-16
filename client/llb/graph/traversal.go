package graph

import (
	"context"

	"github.com/bons/bons-ci/client/llb/core"
)

// ─── VisitFunc ────────────────────────────────────────────────────────────────

// VisitFunc is called for each vertex during a traversal.
// Returning false stops the traversal immediately (like filepath.WalkDir).
type VisitFunc func(id core.VertexID, v core.Vertex, depth int) (continueWalk bool)

// ─── Traversal ────────────────────────────────────────────────────────────────

// Traversal provides algorithm choices for walking a Graph.
type Traversal struct {
	g *Graph
}

// NewTraversal wraps g for traversal.
func NewTraversal(g *Graph) *Traversal { return &Traversal{g: g} }

// DFS performs a depth-first pre-order traversal from the given root IDs,
// visiting each reachable vertex once. Roots are visited before their inputs.
func (t *Traversal) DFS(roots []core.VertexID, visit VisitFunc) {
	visited := make(map[core.VertexID]bool)
	for _, root := range roots {
		t.dfs(root, 0, visited, visit)
	}
}

func (t *Traversal) dfs(
	id core.VertexID,
	depth int,
	visited map[core.VertexID]bool,
	visit VisitFunc,
) bool {
	if visited[id] {
		return true
	}
	visited[id] = true

	v, ok := t.g.vertices[id]
	if !ok {
		return true
	}
	if !visit(id, v, depth) {
		return false
	}
	for _, edge := range v.Inputs() {
		// We need the digest of the input vertex; use the pointer if it's
		// already in the vertex map (avoid re-marshaling during traversal).
		for inputID, inputV := range t.g.vertices {
			if inputV == edge.Vertex {
				if !t.dfs(inputID, depth+1, visited, visit) {
					return false
				}
				break
			}
		}
	}
	return true
}

// BFS performs a breadth-first traversal from the given root IDs.
// Roots are visited before their inputs.
func (t *Traversal) BFS(roots []core.VertexID, visit VisitFunc) {
	type entry struct {
		id    core.VertexID
		depth int
	}
	visited := make(map[core.VertexID]bool)
	queue := make([]entry, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, entry{id: r, depth: 0})
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if visited[cur.id] {
			continue
		}
		visited[cur.id] = true

		v, ok := t.g.vertices[cur.id]
		if !ok {
			continue
		}
		if !visit(cur.id, v, cur.depth) {
			return
		}
		for _, edge := range v.Inputs() {
			for inputID, inputV := range t.g.vertices {
				if inputV == edge.Vertex {
					queue = append(queue, entry{id: inputID, depth: cur.depth + 1})
					break
				}
			}
		}
	}
}

// TopologicalOrder returns vertices in topological order (inputs before
// consumers). Useful for deterministic serialisation.
func (t *Traversal) TopologicalOrder(roots []core.VertexID) []core.VertexID {
	result := make([]core.VertexID, 0, len(t.g.vertices))
	visited := make(map[core.VertexID]bool)
	var topo func(id core.VertexID)
	topo = func(id core.VertexID) {
		if visited[id] {
			return
		}
		visited[id] = true
		v, ok := t.g.vertices[id]
		if !ok {
			return
		}
		for _, edge := range v.Inputs() {
			for inputID, inputV := range t.g.vertices {
				if inputV == edge.Vertex {
					topo(inputID)
					break
				}
			}
		}
		result = append(result, id)
	}
	for _, r := range roots {
		topo(r)
	}
	return result
}

// ─── Selection ────────────────────────────────────────────────────────────────

// Selector accumulates vertices matching a predicate.
type Selector struct {
	g *Graph
}

// NewSelector wraps g for selection.
func NewSelector(g *Graph) *Selector { return &Selector{g: g} }

// Predicate is a function that returns true if a vertex should be selected.
type Predicate func(id core.VertexID, v core.Vertex) bool

// ByType returns all vertices of the given type.
func (s *Selector) ByType(vt core.VertexType) []core.VertexID {
	return s.Where(func(_ core.VertexID, v core.Vertex) bool {
		return v.Type() == vt
	})
}

// Where returns all vertex IDs for which predicate returns true.
func (s *Selector) Where(predicate Predicate) []core.VertexID {
	var out []core.VertexID
	for id, v := range s.g.vertices {
		if predicate(id, v) {
			out = append(out, id)
		}
	}
	return out
}

// Subgraph returns a new Graph containing only the vertices reachable from
// roots, applying an optional filter predicate. Excluded vertices are bypassed:
// their consumers point directly at their producers.
func (s *Selector) Subgraph(ctx context.Context, roots []core.VertexID, include Predicate) *Graph {
	vertices := make(map[core.VertexID]core.Vertex)
	reverse := make(map[core.VertexID]map[core.VertexID]struct{})

	tr := NewTraversal(s.g)
	tr.DFS(roots, func(id core.VertexID, v core.Vertex, _ int) bool {
		if include == nil || include(id, v) {
			vertices[id] = v
			for consumerID := range s.g.reverse[id] {
				if _, exists := reverse[id]; !exists {
					reverse[id] = make(map[core.VertexID]struct{})
				}
				reverse[id][consumerID] = struct{}{}
			}
		}
		return true
	})
	return FromVertices(vertices, reverse, roots)
}

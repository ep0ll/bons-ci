package graph

import (
	"context"

	"github.com/bons/bons-ci/client/llb/core"
)

// ─── Traversal ────────────────────────────────────────────────────────────────

// VisitFunc is called for each vertex during traversal.
// Returning false stops the walk.
type VisitFunc func(id core.VertexID, v core.Vertex, depth int) (continueWalk bool)

// Traversal provides algorithm choices for walking a DAG.
type Traversal struct{ d *DAG }

// NewTraversal wraps d for traversal.
func NewTraversal(d *DAG) *Traversal { return &Traversal{d: d} }

// DFS performs depth-first pre-order traversal from roots.
// Each reachable vertex is visited exactly once.
func (t *Traversal) DFS(roots []core.VertexID, visit VisitFunc) {
	visited := make(map[core.VertexID]bool)
	for _, r := range roots {
		t.dfs(r, 0, visited, visit)
	}
}

func (t *Traversal) dfs(id core.VertexID, depth int, visited map[core.VertexID]bool, visit VisitFunc) bool {
	if visited[id] {
		return true
	}
	visited[id] = true
	v, ok := t.d.vertices[id]
	if !ok {
		return true
	}
	if !visit(id, v, depth) {
		return false
	}
	for _, edge := range v.Inputs() {
		for inputID, inputV := range t.d.vertices {
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

// BFS performs breadth-first traversal from roots.
func (t *Traversal) BFS(roots []core.VertexID, visit VisitFunc) {
	type entry struct {
		id    core.VertexID
		depth int
	}
	visited := make(map[core.VertexID]bool)
	queue := make([]entry, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, entry{id: r})
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur.id] {
			continue
		}
		visited[cur.id] = true
		v, ok := t.d.vertices[cur.id]
		if !ok {
			continue
		}
		if !visit(cur.id, v, cur.depth) {
			return
		}
		for _, edge := range v.Inputs() {
			for inputID, inputV := range t.d.vertices {
				if inputV == edge.Vertex {
					queue = append(queue, entry{id: inputID, depth: cur.depth + 1})
					break
				}
			}
		}
	}
}

// TopologicalOrder returns vertices in topological order (inputs before consumers).
func (t *Traversal) TopologicalOrder(roots []core.VertexID) []core.VertexID {
	result := make([]core.VertexID, 0, len(t.d.vertices))
	visited := make(map[core.VertexID]bool)
	var topo func(id core.VertexID)
	topo = func(id core.VertexID) {
		if visited[id] {
			return
		}
		visited[id] = true
		v, ok := t.d.vertices[id]
		if !ok {
			return
		}
		for _, edge := range v.Inputs() {
			for inputID, inputV := range t.d.vertices {
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

// Ancestors returns all vertex IDs that are transitive inputs of id.
func (t *Traversal) Ancestors(ctx context.Context, id core.VertexID, c *core.Constraints) []core.VertexID {
	var result []core.VertexID
	t.DFS([]core.VertexID{id}, func(vid core.VertexID, _ core.Vertex, depth int) bool {
		if depth > 0 {
			result = append(result, vid)
		}
		return true
	})
	return result
}

// Descendants returns all vertex IDs that are transitive consumers of id.
func (t *Traversal) Descendants(id core.VertexID) []core.VertexID {
	result := make([]core.VertexID, 0)
	visited := make(map[core.VertexID]bool)
	var visit func(id core.VertexID)
	visit = func(current core.VertexID) {
		if visited[current] {
			return
		}
		visited[current] = true
		for consID := range t.d.reverse[current] {
			result = append(result, consID)
			visit(consID)
		}
	}
	visit(id)
	return result
}

// ─── Selector ─────────────────────────────────────────────────────────────────

// Predicate is a vertex filter function.
type Predicate func(id core.VertexID, v core.Vertex) bool

// Selector accumulates vertices matching a predicate.
type Selector struct{ d *DAG }

// NewSelector wraps d for selection.
func NewSelector(d *DAG) *Selector { return &Selector{d: d} }

// ByType returns all vertex IDs of the given type.
func (s *Selector) ByType(vt core.VertexType) []core.VertexID {
	return s.Where(func(_ core.VertexID, v core.Vertex) bool {
		return v.Type() == vt
	})
}

// ByLabel returns all vertex IDs whose labels contain all entries in selector.
func (s *Selector) ByLabel(selector core.Labels) []core.VertexID {
	s.d.mu.RLock()
	defer s.d.mu.RUnlock()
	var out []core.VertexID
	for id, lbl := range s.d.labels {
		if lbl.Match(selector) {
			out = append(out, id)
		}
	}
	return out
}

// Where returns all vertex IDs for which predicate returns true.
func (s *Selector) Where(predicate Predicate) []core.VertexID {
	s.d.mu.RLock()
	defer s.d.mu.RUnlock()
	var out []core.VertexID
	for id, v := range s.d.vertices {
		if predicate(id, v) {
			out = append(out, id)
		}
	}
	return out
}

// Roots returns the DAG's root vertex IDs.
func (s *Selector) Roots() []core.VertexID { return s.d.Roots() }

// Leaves returns vertex IDs that have no inputs (source ops).
func (s *Selector) Leaves() []core.VertexID {
	s.d.mu.RLock()
	defer s.d.mu.RUnlock()
	var out []core.VertexID
	for id, v := range s.d.vertices {
		if len(v.Inputs()) == 0 {
			out = append(out, id)
		}
	}
	return out
}

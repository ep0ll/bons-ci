package reactdag

import (
	"fmt"
	"sync"
)

// DAG is a directed acyclic graph of Vertex nodes.
// Vertices are added and linked during a construction phase; after Seal()
// the graph is read-only for concurrent build execution.
type DAG struct {
	mu       sync.RWMutex
	vertices map[string]*Vertex
	sealed   bool
}

// NewDAG constructs an empty DAG.
func NewDAG() *DAG {
	return &DAG{vertices: make(map[string]*Vertex)}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// AddVertex registers a vertex in the DAG.
func (d *DAG) AddVertex(v *Vertex) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("dag: add vertex: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sealed {
		return fmt.Errorf("dag: sealed; cannot add vertex %q", v.ID())
	}
	if _, exists := d.vertices[v.ID()]; exists {
		return fmt.Errorf("dag: vertex %q already registered", v.ID())
	}
	d.vertices[v.ID()] = v
	return nil
}

// LinkVertices adds a directed edge from parent → child, meaning child depends
// on parent. Call AddVertex for both endpoints first.
func (d *DAG) LinkVertices(parentID, childID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sealed {
		return fmt.Errorf("dag: sealed; cannot link %q → %q", parentID, childID)
	}
	parent, child, err := d.mustGetPair(parentID, childID)
	if err != nil {
		return err
	}
	if d.edgeExists(parent, child) {
		return nil // idempotent
	}
	parent.children = append(parent.children, child)
	child.parents = append(child.parents, parent)
	return nil
}

// AddFileDependency declares fine-grained file dependency: childID reads only
// the listed paths from parentID's output. This must be called after LinkVertices.
func (d *DAG) AddFileDependency(childID, parentID string, paths []string) error {
	d.mu.RLock()
	_, childOK := d.vertices[childID]
	_, parentOK := d.vertices[parentID]
	d.mu.RUnlock()
	if !childOK || !parentOK {
		return fmt.Errorf("dag: file dependency: vertex not found (%s or %s)", childID, parentID)
	}
	d.vertices[childID].AddFileDependency(parentID, paths)
	return nil
}

// Seal validates and freezes the graph. After Seal, no structural changes are
// allowed. Always call Seal before starting a build.
func (d *DAG) Seal() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sealed {
		return nil
	}
	if err := d.detectCycle(); err != nil {
		return err
	}
	d.sealed = true
	return nil
}

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

// Vertex retrieves a vertex by ID.
func (d *DAG) Vertex(id string) (*Vertex, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, ok := d.vertices[id]
	return v, ok
}

// All returns all registered vertices in an unspecified order.
func (d *DAG) All() []*Vertex {
	d.mu.RLock()
	defer d.mu.RUnlock()
	vs := make([]*Vertex, 0, len(d.vertices))
	for _, v := range d.vertices {
		vs = append(vs, v)
	}
	return vs
}

// Roots returns vertices that have no parents (entry-points of the graph).
func (d *DAG) Roots() []*Vertex {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var roots []*Vertex
	for _, v := range d.vertices {
		if len(v.parents) == 0 {
			roots = append(roots, v)
		}
	}
	return roots
}

// ---------------------------------------------------------------------------
// Graph algorithms
// ---------------------------------------------------------------------------

// TopologicalSort returns vertices in Kahn-order (parents before children).
// Returns an error if the graph contains a cycle (should not happen post-Seal).
func (d *DAG) TopologicalSort() ([]*Vertex, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return kahnSort(d.vertices)
}

// TopologicalSortFrom returns vertices reachable from startID in topo order.
func (d *DAG) TopologicalSortFrom(startID string) ([]*Vertex, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	root, ok := d.vertices[startID]
	if !ok {
		return nil, fmt.Errorf("dag: vertex %q not found", startID)
	}
	reachable := collectAncestors(root)
	reachable[startID] = root
	return kahnSort(reachable)
}

// Ancestors returns all transitive ancestors of the given vertex (its dependencies).
func (d *DAG) Ancestors(id string) ([]*Vertex, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, ok := d.vertices[id]
	if !ok {
		return nil, fmt.Errorf("dag: vertex %q not found", id)
	}
	m := collectAncestors(v)
	vs := make([]*Vertex, 0, len(m))
	for _, anc := range m {
		vs = append(vs, anc)
	}
	return vs, nil
}

// Descendants returns all transitive descendants (vertices that depend on id).
func (d *DAG) Descendants(id string) ([]*Vertex, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, ok := d.vertices[id]
	if !ok {
		return nil, fmt.Errorf("dag: vertex %q not found", id)
	}
	m := collectDescendants(v)
	vs := make([]*Vertex, 0, len(m))
	for _, desc := range m {
		vs = append(vs, desc)
	}
	return vs, nil
}

// IsAncestor reports whether ancestorID is a transitive ancestor of targetID.
func (d *DAG) IsAncestor(ancestorID, targetID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	target, ok := d.vertices[targetID]
	if !ok {
		return false
	}
	ancs := collectAncestors(target)
	_, found := ancs[ancestorID]
	return found
}

// AncestorDepth returns the minimum graph distance from targetID up to ancestorID,
// or -1 if ancestorID is not an ancestor. Useful for finding the "level" of dependency.
func (d *DAG) AncestorDepth(ancestorID, targetID string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	target, ok := d.vertices[targetID]
	if !ok {
		return -1
	}
	return bfsDepth(target, ancestorID)
}

// CriticalPath returns the IDs of vertices forming the longest dependency chain
// ending at targetID, measured by vertex count.
func (d *DAG) CriticalPath(targetID string) ([]string, error) {
	sorted, err := d.TopologicalSortFrom(targetID)
	if err != nil {
		return nil, err
	}
	return computeCriticalPath(sorted, targetID), nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (d *DAG) mustGetPair(parentID, childID string) (*Vertex, *Vertex, error) {
	parent, ok := d.vertices[parentID]
	if !ok {
		return nil, nil, fmt.Errorf("dag: parent vertex %q not found", parentID)
	}
	child, ok := d.vertices[childID]
	if !ok {
		return nil, nil, fmt.Errorf("dag: child vertex %q not found", childID)
	}
	return parent, child, nil
}

func (d *DAG) edgeExists(parent, child *Vertex) bool {
	for _, c := range parent.children {
		if c.id == child.id {
			return true
		}
	}
	return false
}

// detectCycle uses DFS with three-color marking (white=0, gray=1, black=2).
func (d *DAG) detectCycle() error {
	color := make(map[string]uint8, len(d.vertices))
	var dfs func(v *Vertex) bool
	dfs = func(v *Vertex) bool {
		color[v.id] = 1 // gray: in progress
		for _, child := range v.children {
			if color[child.id] == 1 {
				return true // back-edge → cycle
			}
			if color[child.id] == 0 && dfs(child) {
				return true
			}
		}
		color[v.id] = 2 // black: done
		return false
	}
	for _, v := range d.vertices {
		if color[v.id] == 0 {
			if dfs(v) {
				return fmt.Errorf("dag: cycle detected involving vertex %q", v.id)
			}
		}
	}
	return nil
}

// kahnSort implements Kahn's BFS topological sort over a vertex map.
func kahnSort(vertices map[string]*Vertex) ([]*Vertex, error) {
	inDeg := make(map[string]int, len(vertices))
	for id, v := range vertices {
		if _, seen := inDeg[id]; !seen {
			inDeg[id] = 0
		}
		for _, child := range v.children {
			if _, ok := vertices[child.id]; ok {
				inDeg[child.id]++
			}
		}
	}

	queue := make([]*Vertex, 0, len(vertices))
	for id, deg := range inDeg {
		if deg == 0 {
			queue = append(queue, vertices[id])
		}
	}

	result := make([]*Vertex, 0, len(vertices))
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		result = append(result, v)
		for _, child := range v.children {
			if _, ok := vertices[child.id]; !ok {
				continue
			}
			inDeg[child.id]--
			if inDeg[child.id] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if len(result) != len(vertices) {
		return nil, fmt.Errorf("dag: topological sort failed (cycle?)")
	}
	return result, nil
}

// collectAncestors performs BFS upward (toward roots) from v.
func collectAncestors(v *Vertex) map[string]*Vertex {
	visited := make(map[string]*Vertex)
	queue := v.parents
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, seen := visited[cur.id]; seen {
			continue
		}
		visited[cur.id] = cur
		queue = append(queue, cur.parents...)
	}
	return visited
}

// collectDescendants performs BFS downward (toward leaves) from v.
func collectDescendants(v *Vertex) map[string]*Vertex {
	visited := make(map[string]*Vertex)
	queue := v.children
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, seen := visited[cur.id]; seen {
			continue
		}
		visited[cur.id] = cur
		queue = append(queue, cur.children...)
	}
	return visited
}

// bfsDepth returns the minimum BFS distance from v upward to the vertex with targetID.
func bfsDepth(v *Vertex, targetID string) int {
	type entry struct {
		vertex *Vertex
		depth  int
	}
	queue := []entry{{v, 0}}
	visited := map[string]bool{v.id: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, p := range cur.vertex.parents {
			if p.id == targetID {
				return cur.depth + 1
			}
			if !visited[p.id] {
				visited[p.id] = true
				queue = append(queue, entry{p, cur.depth + 1})
			}
		}
	}
	return -1
}

// computeCriticalPath uses DP on the topologically sorted slice to find the
// longest path (by vertex count) ending at targetID.
func computeCriticalPath(sorted []*Vertex, targetID string) []string {
	// dist[id] = max vertices in any path from a root to id
	dist := make(map[string]int, len(sorted))
	prev := make(map[string]string, len(sorted))

	for _, v := range sorted {
		dist[v.id] = 1
		for _, p := range v.parents {
			if d := dist[p.id] + 1; d > dist[v.id] {
				dist[v.id] = d
				prev[v.id] = p.id
			}
		}
	}

	// Trace back from target
	path := []string{}
	cur := targetID
	for cur != "" {
		path = append([]string{cur}, path...)
		cur = prev[cur]
	}
	return path
}

package solver

import (
	"sort"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// Graph represents the reachable subgraph from a set of leaf edges.
// It provides topological analysis, depth computation, and critical-path
// identification for the scheduler.
type Graph struct {
	mu sync.RWMutex

	// vertices maps digest → Vertex for all reachable vertices.
	vertices map[digest.Digest]Vertex

	// children maps parent digest → set of child digests (forward edges).
	children map[digest.Digest]map[digest.Digest]struct{}

	// parents maps child digest → set of parent digests (reverse edges).
	parents map[digest.Digest]map[digest.Digest]struct{}

	// depth caches computed depth for each vertex.
	depth map[digest.Digest]int

	// leaves stores the original leaf edges that seeded this graph.
	leaves []Edge
}

// BuildGraph constructs the reachable subgraph by walking upward from the
// given leaf edges. It deduplicates vertices by digest and computes the
// parent/child adjacency maps.
func BuildGraph(leaves []Edge) *Graph {
	g := &Graph{
		vertices: make(map[digest.Digest]Vertex),
		children: make(map[digest.Digest]map[digest.Digest]struct{}),
		parents:  make(map[digest.Digest]map[digest.Digest]struct{}),
		depth:    make(map[digest.Digest]int),
		leaves:   leaves,
	}

	visited := make(map[digest.Digest]struct{})
	for _, leaf := range leaves {
		g.walk(leaf.Vertex, visited)
	}
	g.computeDepths()
	return g
}

// walk recursively discovers all vertices reachable from v by following
// inputs upward.
func (g *Graph) walk(v Vertex, visited map[digest.Digest]struct{}) {
	dgst := v.Digest()
	if _, ok := visited[dgst]; ok {
		return
	}
	visited[dgst] = struct{}{}
	g.vertices[dgst] = v

	for _, inp := range v.Inputs() {
		parent := inp.Vertex
		parentDgst := parent.Digest()

		// child → parent (upward)
		if g.parents[dgst] == nil {
			g.parents[dgst] = make(map[digest.Digest]struct{})
		}
		g.parents[dgst][parentDgst] = struct{}{}

		// parent → child (downward)
		if g.children[parentDgst] == nil {
			g.children[parentDgst] = make(map[digest.Digest]struct{})
		}
		g.children[parentDgst][dgst] = struct{}{}

		g.walk(parent, visited)
	}
}

// computeDepths calculates the depth (longest path from any root) for each
// vertex. Roots (vertices with no inputs) have depth 0.
func (g *Graph) computeDepths() {
	memo := make(map[digest.Digest]int, len(g.vertices))
	for dgst := range g.vertices {
		g.depth[dgst] = g.depthOf(dgst, memo)
	}
}

func (g *Graph) depthOf(dgst digest.Digest, memo map[digest.Digest]int) int {
	if d, ok := memo[dgst]; ok {
		return d
	}
	v := g.vertices[dgst]
	if v == nil || len(v.Inputs()) == 0 {
		memo[dgst] = 0
		return 0
	}
	maxParent := 0
	for _, inp := range v.Inputs() {
		pd := g.depthOf(inp.Vertex.Digest(), memo)
		if pd+1 > maxParent {
			maxParent = pd + 1
		}
	}
	memo[dgst] = maxParent
	return maxParent
}

// Depth returns the dependency distance from the deepest root for the
// given vertex. Returns -1 if the vertex is not in the graph.
func (g *Graph) Depth(v Vertex) int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	d, ok := g.depth[v.Digest()]
	if !ok {
		return -1
	}
	return d
}

// MaxDepth returns the maximum depth across all vertices in the graph.
func (g *Graph) MaxDepth() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	max := 0
	for _, d := range g.depth {
		if d > max {
			max = d
		}
	}
	return max
}

// Vertex returns the vertex with the given digest, or nil.
func (g *Graph) Vertex(dgst digest.Digest) Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.vertices[dgst]
}

// Vertices returns all vertices in the graph.
func (g *Graph) Vertices() []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Vertex, 0, len(g.vertices))
	for _, v := range g.vertices {
		out = append(out, v)
	}
	return out
}

// Leaves returns the original leaf edges.
func (g *Graph) Leaves() []Edge {
	return g.leaves
}

// Roots returns vertices with no inputs (depth 0).
func (g *Graph) Roots() []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var roots []Vertex
	for dgst, v := range g.vertices {
		if g.depth[dgst] == 0 {
			roots = append(roots, v)
		}
	}
	return roots
}

// ParentsOf returns the parent vertices (inputs) of the given vertex.
func (g *Graph) ParentsOf(v Vertex) []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	pSet := g.parents[v.Digest()]
	out := make([]Vertex, 0, len(pSet))
	for p := range pSet {
		if pv, ok := g.vertices[p]; ok {
			out = append(out, pv)
		}
	}
	return out
}

// ChildrenOf returns the child vertices (consumers) of the given vertex.
func (g *Graph) ChildrenOf(v Vertex) []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cSet := g.children[v.Digest()]
	out := make([]Vertex, 0, len(cSet))
	for c := range cSet {
		if cv, ok := g.vertices[c]; ok {
			out = append(out, cv)
		}
	}
	return out
}

// TopologicalOrder returns vertices sorted by depth ascending (roots first).
// Within the same depth, vertices are sorted by digest for determinism.
func (g *Graph) TopologicalOrder() []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Vertex, 0, len(g.vertices))
	for _, v := range g.vertices {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		di := g.depth[out[i].Digest()]
		dj := g.depth[out[j].Digest()]
		if di != dj {
			return di < dj
		}
		return out[i].Digest() < out[j].Digest()
	})
	return out
}

// CriticalPath returns the longest dependency chain from the given leaf
// edge back to a root. This is the bottleneck path that determines
// minimum solve latency.
func (g *Graph) CriticalPath(leaf Edge) []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.criticalPathFrom(leaf.Vertex)
}

func (g *Graph) criticalPathFrom(v Vertex) []Vertex {
	if v == nil {
		return nil
	}
	path := []Vertex{v}
	inputs := v.Inputs()
	if len(inputs) == 0 {
		return path
	}

	// Follow the deepest parent at each step.
	var deepest Vertex
	deepestDepth := -1
	for _, inp := range inputs {
		d := g.depth[inp.Vertex.Digest()]
		if d > deepestDepth {
			deepestDepth = d
			deepest = inp.Vertex
		}
	}
	if deepest != nil {
		path = append(path, g.criticalPathFrom(deepest)...)
	}
	return path
}

// Size returns the number of vertices in the graph.
func (g *Graph) Size() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.vertices)
}

// Contains checks if a vertex is in the graph.
func (g *Graph) Contains(v Vertex) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.vertices[v.Digest()]
	return ok
}

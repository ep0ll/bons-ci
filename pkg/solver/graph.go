package solver

import (
	"sort"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// Graph represents the reachable subgraph starting from a set of leaf edges,
// traversed upward through inputs. It provides topological analysis, depth
// computation, and critical-path identification for the scheduler.
//
// Graph is built once and then read-only; all exported methods are safe for
// concurrent use after BuildGraph returns.
type Graph struct {
	mu sync.RWMutex

	// vertices maps digest → Vertex for every reachable vertex.
	vertices map[digest.Digest]Vertex

	// children maps parent digest → set of child digests (forward / downward).
	children map[digest.Digest]map[digest.Digest]struct{}

	// parents maps child digest → set of parent digests (reverse / upward).
	parents map[digest.Digest]map[digest.Digest]struct{}

	// depth caches the computed dependency distance from any root for each vertex.
	depth map[digest.Digest]int

	// leaves stores the original leaf edges that seeded the graph.
	leaves []Edge
}

// BuildGraph constructs the reachable subgraph by walking upward from the given
// leaf edges. It deduplicates vertices by digest and computes depth and
// adjacency maps in one pass.
//
// Complexity: O(V + E) where V = vertices, E = edges.
func BuildGraph(leaves []Edge) *Graph {
	g := &Graph{
		vertices: make(map[digest.Digest]Vertex),
		children: make(map[digest.Digest]map[digest.Digest]struct{}),
		parents:  make(map[digest.Digest]map[digest.Digest]struct{}),
		depth:    make(map[digest.Digest]int),
		leaves:   leaves,
	}
	visited := make(map[digest.Digest]struct{}, len(leaves)*4)
	for _, leaf := range leaves {
		if leaf.Vertex != nil {
			g.walk(leaf.Vertex, visited)
		}
	}
	g.computeDepths()
	return g
}

// walk recursively discovers all ancestors of v by following Inputs() upward.
func (g *Graph) walk(v Vertex, visited map[digest.Digest]struct{}) {
	dgst := v.Digest()
	if _, seen := visited[dgst]; seen {
		return
	}
	visited[dgst] = struct{}{}
	g.vertices[dgst] = v

	for _, inp := range v.Inputs() {
		if inp.Vertex == nil {
			continue
		}
		parent := inp.Vertex
		parentDgst := parent.Digest()

		// child → parent (upward link, used for execution ordering)
		if g.parents[dgst] == nil {
			g.parents[dgst] = make(map[digest.Digest]struct{})
		}
		g.parents[dgst][parentDgst] = struct{}{}

		// parent → child (downward link, used to unblock children on completion)
		if g.children[parentDgst] == nil {
			g.children[parentDgst] = make(map[digest.Digest]struct{})
		}
		g.children[parentDgst][dgst] = struct{}{}

		g.walk(parent, visited)
	}
}

// computeDepths computes depth for every vertex. Depth is defined as the
// length of the longest path from any root (depth-0) vertex to this vertex.
// Roots (no inputs) have depth 0. This mirrors BuildKit's concept of a vertex's
// "distance" from the sources.
func (g *Graph) computeDepths() {
	memo := make(map[digest.Digest]int, len(g.vertices))
	for dgst := range g.vertices {
		g.depth[dgst] = g.depthOf(dgst, memo)
	}
}

// depthOf computes depth recursively with memoisation.
func (g *Graph) depthOf(dgst digest.Digest, memo map[digest.Digest]int) int {
	if d, ok := memo[dgst]; ok {
		return d
	}
	v := g.vertices[dgst]
	if v == nil || len(v.Inputs()) == 0 {
		memo[dgst] = 0
		return 0
	}
	max := 0
	for _, inp := range v.Inputs() {
		if inp.Vertex == nil {
			continue
		}
		if d := g.depthOf(inp.Vertex.Digest(), memo) + 1; d > max {
			max = d
		}
	}
	memo[dgst] = max
	return max
}

// ─── Read-only accessors (safe after BuildGraph) ──────────────────────────────

// Depth returns the dependency distance from the deepest root for v.
// Returns -1 if v is not in the graph.
func (g *Graph) Depth(v Vertex) int {
	g.mu.RLock()
	d, ok := g.depth[v.Digest()]
	g.mu.RUnlock()
	if !ok {
		return -1
	}
	return d
}

// DepthByDigest returns depth by raw digest, -1 if absent.
func (g *Graph) DepthByDigest(dgst digest.Digest) int {
	g.mu.RLock()
	d, ok := g.depth[dgst]
	g.mu.RUnlock()
	if !ok {
		return -1
	}
	return d
}

// MaxDepth returns the maximum depth across all vertices.
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

// VertexByDigest returns the Vertex for the given digest, or nil.
// (Renamed from Vertex() to avoid shadowing the Vertex interface name.)
func (g *Graph) VertexByDigest(dgst digest.Digest) Vertex {
	g.mu.RLock()
	v := g.vertices[dgst]
	g.mu.RUnlock()
	return v
}

// Vertices returns all vertices in the graph as a slice.
func (g *Graph) Vertices() []Vertex {
	g.mu.RLock()
	out := make([]Vertex, 0, len(g.vertices))
	for _, v := range g.vertices {
		out = append(out, v)
	}
	g.mu.RUnlock()
	return out
}

// Leaves returns the original leaf edges that seeded this graph.
func (g *Graph) Leaves() []Edge { return g.leaves }

// Roots returns every vertex with depth 0 (no inputs / source vertices).
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

// ParentsOf returns the direct parent (input) vertices of v.
func (g *Graph) ParentsOf(v Vertex) []Vertex {
	g.mu.RLock()
	pSet := g.parents[v.Digest()]
	out := make([]Vertex, 0, len(pSet))
	for p := range pSet {
		if pv, ok := g.vertices[p]; ok {
			out = append(out, pv)
		}
	}
	g.mu.RUnlock()
	return out
}

// ChildrenOf returns the direct child (consumer) vertices of v — those that
// list v as an input. Used by the scheduler to unblock dependants on completion.
func (g *Graph) ChildrenOf(v Vertex) []Vertex {
	g.mu.RLock()
	cSet := g.children[v.Digest()]
	out := make([]Vertex, 0, len(cSet))
	for c := range cSet {
		if cv, ok := g.vertices[c]; ok {
			out = append(out, cv)
		}
	}
	g.mu.RUnlock()
	return out
}

// InDegree returns the number of parent vertices for v (i.e. len(v.Inputs())).
// A vertex with InDegree 0 is a root.
func (g *Graph) InDegree(v Vertex) int {
	g.mu.RLock()
	n := len(g.parents[v.Digest()])
	g.mu.RUnlock()
	return n
}

// OutDegree returns the number of children of v.
func (g *Graph) OutDegree(v Vertex) int {
	g.mu.RLock()
	n := len(g.children[v.Digest()])
	g.mu.RUnlock()
	return n
}

// TopologicalOrder returns all vertices sorted by depth ascending (roots
// first). Within the same depth, vertices are sorted by digest for
// determinism. This order is used for the initial cache-probe sweep.
func (g *Graph) TopologicalOrder() []Vertex {
	g.mu.RLock()
	out := make([]Vertex, 0, len(g.vertices))
	for _, v := range g.vertices {
		out = append(out, v)
	}
	g.mu.RUnlock()
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

// CriticalPath returns the longest dependency chain from the given leaf back to
// a root, inclusive. The first element is the leaf; the last is the deepest
// root ancestor. This is the bottleneck path that determines minimum latency.
func (g *Graph) CriticalPath(leaf Edge) []Vertex {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if leaf.Vertex == nil {
		return nil
	}
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
	var deepest Vertex
	deepestDepth := -1
	for _, inp := range inputs {
		if inp.Vertex == nil {
			continue
		}
		if d := g.depth[inp.Vertex.Digest()]; d > deepestDepth {
			deepestDepth = d
			deepest = inp.Vertex
		}
	}
	if deepest != nil {
		path = append(path, g.criticalPathFrom(deepest)...)
	}
	return path
}

// Size returns the total number of vertices in the graph.
func (g *Graph) Size() int {
	g.mu.RLock()
	n := len(g.vertices)
	g.mu.RUnlock()
	return n
}

// Contains reports whether v is part of this graph.
func (g *Graph) Contains(v Vertex) bool {
	g.mu.RLock()
	_, ok := g.vertices[v.Digest()]
	g.mu.RUnlock()
	return ok
}

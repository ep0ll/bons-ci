// Package graph implements an immutable directed acyclic graph (DAG) of build
// vertices together with an event-driven mutation layer.
//
// Immutability contract
// ─────────────────────
// A *Graph value is immutable after construction. Every mutation method
// (Replace, Reparent, …) returns a *new* Graph. The old Graph is unaffected
// and may be retained for rollback/history purposes.
//
// Reactive contract
// ─────────────────
// When a mutation changes the content-address (digest) of a vertex, all
// vertices that transitively depend on it are also re-digested. A
// DigestChangeEvent is emitted on the Graph's event bus for each affected
// vertex so that subscribers (e.g., a caching layer) can react.
package graph

import (
	"context"
	"fmt"
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/reactive"
)

// ─── Graph ───────────────────────────────────────────────────────────────────

// Graph is an immutable snapshot of a build DAG.
//
// Internal representation
//
//	vertices   – map from VertexID → Vertex (all known vertices)
//	reverse    – map from VertexID → set<VertexID> (consumers of each vertex)
//	roots      – IDs of vertices with no consumers (terminal outputs)
type Graph struct {
	vertices map[core.VertexID]core.Vertex
	// reverse maps a vertex to the set of vertices that list it as an input.
	reverse map[core.VertexID]map[core.VertexID]struct{}
	roots   []core.VertexID

	// bus is shared between a graph and all graphs derived from it via mutation,
	// so subscribers set up before a mutation continue to receive events.
	bus *reactive.EventBus[reactive.GraphEvent]

	mu sync.RWMutex // guards vertices, reverse, roots during Build only
}

// New builds a Graph by walking the DAG reachable from root.
// Returns ErrCyclicGraph if a cycle is detected.
func New(ctx context.Context, root core.Vertex, c *core.Constraints) (*Graph, error) {
	g := &Graph{
		vertices: make(map[core.VertexID]core.Vertex),
		reverse:  make(map[core.VertexID]map[core.VertexID]struct{}),
		bus:      reactive.NewEventBus[reactive.GraphEvent](),
	}
	if err := g.walk(ctx, root, c, make(map[core.VertexID]bool)); err != nil {
		return nil, err
	}
	mv, err := root.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	g.roots = []core.VertexID{mv.Digest}
	return g, nil
}

// FromVertices builds a Graph from an explicit vertex map (e.g., after
// deserialising a pb.Definition). roots must be a subset of keys in vertices.
func FromVertices(
	vertices map[core.VertexID]core.Vertex,
	reverse map[core.VertexID]map[core.VertexID]struct{},
	roots []core.VertexID,
) *Graph {
	return &Graph{
		vertices: vertices,
		reverse:  reverse,
		roots:    roots,
		bus:      reactive.NewEventBus[reactive.GraphEvent](),
	}
}

// ─── Read operations ─────────────────────────────────────────────────────────

// Vertex returns the vertex with the given ID, or (nil, ErrVertexNotFound).
func (g *Graph) Vertex(id core.VertexID) (core.Vertex, error) {
	g.mu.RLock()
	v, ok := g.vertices[id]
	g.mu.RUnlock()
	if !ok {
		return nil, &core.VertexNotFoundError{ID: id}
	}
	return v, nil
}

// Roots returns the IDs of root (terminal) vertices – those with no consumers.
func (g *Graph) Roots() []core.VertexID {
	return g.roots
}

// Consumers returns the IDs of all vertices that directly consume vertex id.
func (g *Graph) Consumers(id core.VertexID) []core.VertexID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cons, ok := g.reverse[id]
	if !ok {
		return nil
	}
	out := make([]core.VertexID, 0, len(cons))
	for cid := range cons {
		out = append(out, cid)
	}
	return out
}

// Size returns the total number of vertices in the graph.
func (g *Graph) Size() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.vertices)
}

// Subscribe registers a handler for graph-change events.
func (g *Graph) Subscribe(handler func(reactive.GraphEvent)) reactive.Subscription {
	return g.bus.Subscribe(handler)
}

// ─── Traversal ───────────────────────────────────────────────────────────────

// walk performs a depth-first pre-order traversal starting at v, populating
// g.vertices and g.reverse. It detects cycles using a coloured DFS.
//
// colour: 0 = white (unvisited), 1 = grey (in stack), 2 = black (done).
func (g *Graph) walk(
	ctx context.Context,
	v core.Vertex,
	c *core.Constraints,
	inStack map[core.VertexID]bool,
) error {
	mv, err := v.Marshal(ctx, c)
	if err != nil {
		return fmt.Errorf("graph.walk marshal: %w", err)
	}
	id := mv.Digest

	// Cycle detection.
	if inStack[id] {
		return &core.CyclicGraphError{Cycle: []core.VertexID{id}}
	}
	// Already processed.
	if _, exists := g.vertices[id]; exists {
		return nil
	}

	inStack[id] = true
	g.vertices[id] = v

	for _, edge := range v.Inputs() {
		child := edge.Vertex
		cmv, err := child.Marshal(ctx, c)
		if err != nil {
			return fmt.Errorf("graph.walk marshal child: %w", err)
		}
		cid := cmv.Digest

		// Record reverse edge: child → parent.
		if g.reverse[cid] == nil {
			g.reverse[cid] = make(map[core.VertexID]struct{})
		}
		g.reverse[cid][id] = struct{}{}

		if err := g.walk(ctx, child, c, inStack); err != nil {
			return err
		}
	}
	delete(inStack, id)
	return nil
}

// ─── Clone helpers ────────────────────────────────────────────────────────────

// clone produces a shallow copy of the graph suitable for mutation.
// The bus is shared so existing subscribers remain active.
func (g *Graph) clone() *Graph {
	g.mu.RLock()
	defer g.mu.RUnlock()

	verts := make(map[core.VertexID]core.Vertex, len(g.vertices))
	for k, v := range g.vertices {
		verts[k] = v
	}
	rev := make(map[core.VertexID]map[core.VertexID]struct{}, len(g.reverse))
	for k, consumers := range g.reverse {
		c2 := make(map[core.VertexID]struct{}, len(consumers))
		for cid := range consumers {
			c2[cid] = struct{}{}
		}
		rev[k] = c2
	}
	roots := make([]core.VertexID, len(g.roots))
	copy(roots, g.roots)

	return &Graph{
		vertices: verts,
		reverse:  rev,
		roots:    roots,
		bus:      g.bus, // shared
	}
}

// emit fires a GraphEvent on the shared bus.
func (g *Graph) emit(e reactive.GraphEvent) {
	g.bus.Publish(e)
}

// ─── Digest propagation ───────────────────────────────────────────────────────

// propagateDigestChange replaces oldID with newVertex throughout g, then
// recursively re-digests all consumers and emits DigestChanged events.
//
// ctx and c are needed to re-marshal affected vertices.
func (g *Graph) propagateDigestChange(
	ctx context.Context,
	oldID core.VertexID,
	newVertex core.Vertex,
	c *core.Constraints,
) error {
	newMV, err := newVertex.Marshal(ctx, c)
	if err != nil {
		return fmt.Errorf("propagateDigestChange: marshal new vertex: %w", err)
	}
	newID := newMV.Digest

	// Update vertex map.
	delete(g.vertices, oldID)
	g.vertices[newID] = newVertex

	// Emit change event.
	g.emit(reactive.GraphEvent{
		Kind:       reactive.EventKindDigestChanged,
		AffectedID: newID,
		PreviousID: oldID,
	})

	// Update roots if needed.
	for i, r := range g.roots {
		if r == oldID {
			g.roots[i] = newID
		}
	}

	// Re-wire reverse adjacency.
	consumers := g.reverse[oldID]
	delete(g.reverse, oldID)
	if g.reverse[newID] == nil {
		g.reverse[newID] = make(map[core.VertexID]struct{})
	}
	for cid := range consumers {
		g.reverse[newID][cid] = struct{}{}
	}

	// Recursively re-digest consumers.
	for consumerID := range consumers {
		consumerVertex, ok := g.vertices[consumerID]
		if !ok {
			continue
		}
		mut, ok := consumerVertex.(core.MutatingVertex)
		if !ok {
			continue
		}
		// Build the updated input list for the consumer.
		newInputs := make([]core.Edge, len(consumerVertex.Inputs()))
		for i, edge := range consumerVertex.Inputs() {
			eMV, err := edge.Vertex.Marshal(ctx, c)
			if err != nil {
				return err
			}
			if eMV.Digest == oldID {
				newInputs[i] = core.Edge{Vertex: newVertex, Index: edge.Index}
			} else {
				newInputs[i] = edge
			}
		}
		updatedConsumer, err := mut.WithInputs(newInputs)
		if err != nil {
			return fmt.Errorf("propagateDigestChange: rewire consumer: %w", err)
		}
		if err := g.propagateDigestChange(ctx, consumerID, updatedConsumer, c); err != nil {
			return err
		}
	}
	return nil
}

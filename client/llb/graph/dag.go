// Package graph implements a content-addressable directed acyclic graph (DAG)
// of build vertices together with reactive mutation, traversal, merge, and
// prune operations.
//
// Content-addressability contract
// ────────────────────────────────
// Every vertex is stored under its deterministic digest. Changing any field on
// a vertex (directly or via WithInputs) changes its digest; the DAG's mutation
// methods automatically cascade the new digest to every consumer vertex and
// emit DigestChanged events on the shared event bus.
//
// Immutability contract
// ─────────────────────
// A *DAG is immutable after construction. Every mutation returns a *new* DAG.
// Old DAG values are unaffected and may be retained as rollback checkpoints.
package graph

import (
	"context"
	"fmt"
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/reactive"
	digest "github.com/opencontainers/go-digest"
)

// ─── DAG ──────────────────────────────────────────────────────────────────────

// DAG is an immutable snapshot of a build graph.
//
// Internal representation:
//
//	vertices  – digest → Vertex
//	reverse   – digest → set of consumer digests
//	labels    – digest → Labels (for selector/gate operations)
//	roots     – digests of vertices with no consumers
type DAG struct {
	mu       sync.RWMutex
	vertices map[core.VertexID]core.Vertex
	reverse  map[core.VertexID]map[core.VertexID]struct{}
	labels   map[core.VertexID]core.Labels
	roots    []core.VertexID

	// bus is shared between a DAG and all DAGs derived from it via mutation,
	// so subscribers installed before a mutation keep receiving events.
	bus *reactive.EventBus[reactive.GraphEvent]
}

// New constructs a DAG by walking the graph reachable from root.
// Returns ErrCyclicGraph if a cycle is detected.
func New(ctx context.Context, root core.Vertex, c *core.Constraints) (*DAG, error) {
	d := &DAG{
		vertices: make(map[core.VertexID]core.Vertex),
		reverse:  make(map[core.VertexID]map[core.VertexID]struct{}),
		labels:   make(map[core.VertexID]core.Labels),
		bus:      reactive.NewEventBus[reactive.GraphEvent](),
	}
	if err := d.walk(ctx, root, c, make(map[core.VertexID]bool)); err != nil {
		return nil, err
	}
	mv, err := root.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("dag.New: marshal root: %w", err)
	}
	d.roots = []core.VertexID{mv.Digest}
	return d, nil
}

// FromVertices constructs a DAG from explicit maps (e.g., after deserialisation).
func FromVertices(
	vertices map[core.VertexID]core.Vertex,
	reverse map[core.VertexID]map[core.VertexID]struct{},
	labels map[core.VertexID]core.Labels,
	roots []core.VertexID,
) *DAG {
	if labels == nil {
		labels = make(map[core.VertexID]core.Labels)
	}
	return &DAG{
		vertices: vertices,
		reverse:  reverse,
		labels:   labels,
		roots:    roots,
		bus:      reactive.NewEventBus[reactive.GraphEvent](),
	}
}

// ─── Read operations ──────────────────────────────────────────────────────────

// Vertex returns the vertex with the given ID, or (nil, ErrVertexNotFound).
func (d *DAG) Vertex(id core.VertexID) (core.Vertex, error) {
	d.mu.RLock()
	v, ok := d.vertices[id]
	d.mu.RUnlock()
	if !ok {
		return nil, &core.VertexNotFoundError{ID: id}
	}
	return v, nil
}

// Roots returns the IDs of root (terminal) vertices.
func (d *DAG) Roots() []core.VertexID { return d.roots }

// Consumers returns the IDs of all vertices that directly consume vertex id.
func (d *DAG) Consumers(id core.VertexID) []core.VertexID {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cons := d.reverse[id]
	out := make([]core.VertexID, 0, len(cons))
	for cid := range cons {
		out = append(out, cid)
	}
	return out
}

// Size returns the total number of vertices.
func (d *DAG) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.vertices)
}

// Labels returns labels attached to the given vertex.
func (d *DAG) Labels(id core.VertexID) core.Labels {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.labels[id]
}

// AllVertices returns all vertex IDs in the graph (unordered).
func (d *DAG) AllVertices() []core.VertexID {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ids := make([]core.VertexID, 0, len(d.vertices))
	for id := range d.vertices {
		ids = append(ids, id)
	}
	return ids
}

// Subscribe registers a handler for graph change events.
func (d *DAG) Subscribe(handler func(reactive.GraphEvent)) reactive.Subscription {
	return d.bus.Subscribe(handler)
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// walk performs a depth-first traversal, populating d.vertices and d.reverse.
func (d *DAG) walk(
	ctx context.Context,
	v core.Vertex,
	c *core.Constraints,
	inStack map[core.VertexID]bool,
) error {
	mv, err := v.Marshal(ctx, c)
	if err != nil {
		return fmt.Errorf("dag walk marshal: %w", err)
	}
	id := mv.Digest

	if inStack[id] {
		return &core.CyclicGraphError{Cycle: []core.VertexID{id}}
	}
	if _, exists := d.vertices[id]; exists {
		return nil
	}
	inStack[id] = true
	d.vertices[id] = v

	for _, edge := range v.Inputs() {
		child := edge.Vertex
		cmv, err := child.Marshal(ctx, c)
		if err != nil {
			return fmt.Errorf("dag walk child marshal: %w", err)
		}
		cid := cmv.Digest
		if d.reverse[cid] == nil {
			d.reverse[cid] = make(map[core.VertexID]struct{})
		}
		d.reverse[cid][id] = struct{}{}
		if err := d.walk(ctx, child, c, inStack); err != nil {
			return err
		}
	}
	delete(inStack, id)
	return nil
}

// clone produces a copy of the DAG, sharing the event bus.
func (d *DAG) clone() *DAG {
	d.mu.RLock()
	defer d.mu.RUnlock()

	verts := make(map[core.VertexID]core.Vertex, len(d.vertices))
	for k, v := range d.vertices {
		verts[k] = v
	}
	rev := make(map[core.VertexID]map[core.VertexID]struct{}, len(d.reverse))
	for k, cs := range d.reverse {
		c2 := make(map[core.VertexID]struct{}, len(cs))
		for cid := range cs {
			c2[cid] = struct{}{}
		}
		rev[k] = c2
	}
	lbls := make(map[core.VertexID]core.Labels, len(d.labels))
	for k, l := range d.labels {
		lbls[k] = l
	}
	roots := make([]core.VertexID, len(d.roots))
	copy(roots, d.roots)

	return &DAG{
		vertices: verts,
		reverse:  rev,
		labels:   lbls,
		roots:    roots,
		bus:      d.bus,
	}
}

func (d *DAG) emit(e reactive.GraphEvent) { d.bus.Publish(e) }

// ─── Content-address cascade ──────────────────────────────────────────────────

// propagateDigest replaces oldID with newVertex, re-digests all consumers
// recursively, and emits DigestChanged events.
func (d *DAG) propagateDigest(
	ctx context.Context,
	oldID core.VertexID,
	newVertex core.Vertex,
	c *core.Constraints,
) error {
	newMV, err := newVertex.Marshal(ctx, c)
	if err != nil {
		return fmt.Errorf("propagateDigest: marshal: %w", err)
	}
	newID := newMV.Digest

	// Update vertex map.
	delete(d.vertices, oldID)
	d.vertices[newID] = newVertex

	// Transfer labels.
	if lbl, ok := d.labels[oldID]; ok {
		delete(d.labels, oldID)
		d.labels[newID] = lbl
	}

	// Update roots.
	for i, r := range d.roots {
		if r == oldID {
			d.roots[i] = newID
		}
	}

	// Rewire reverse adjacency.
	consumers := d.reverse[oldID]
	delete(d.reverse, oldID)
	if consumers != nil {
		if d.reverse[newID] == nil {
			d.reverse[newID] = make(map[core.VertexID]struct{})
		}
		for cid := range consumers {
			d.reverse[newID][cid] = struct{}{}
		}
	}

	d.emit(reactive.GraphEvent{
		Kind:       reactive.EventKindDigestChanged,
		AffectedID: newID,
		PreviousID: oldID,
	})

	// Cascade to consumers.
	for consID := range consumers {
		consVtx, ok := d.vertices[consID]
		if !ok {
			continue
		}
		mut, ok := consVtx.(core.MutatingVertex)
		if !ok {
			continue
		}
		// Rebuild input list with the updated vertex substituted in.
		newInputs := make([]core.Edge, len(consVtx.Inputs()))
		for i, edge := range consVtx.Inputs() {
			emv, err := edge.Vertex.Marshal(ctx, c)
			if err != nil {
				return err
			}
			if emv.Digest == oldID {
				newInputs[i] = core.Edge{Vertex: newVertex, Index: edge.Index}
			} else {
				newInputs[i] = edge
			}
		}
		updatedCons, err := mut.WithInputs(newInputs)
		if err != nil {
			return fmt.Errorf("propagateDigest: rewire consumer %q: %w", consID, err)
		}
		if err := d.propagateDigest(ctx, consID, updatedCons, c); err != nil {
			return err
		}
	}
	return nil
}

// SetLabels attaches labels to a vertex, returning a new DAG.
func (d *DAG) SetLabels(id core.VertexID, labels core.Labels) (*DAG, error) {
	d.mu.RLock()
	_, ok := d.vertices[id]
	d.mu.RUnlock()
	if !ok {
		return nil, &core.VertexNotFoundError{ID: id}
	}
	nd := d.clone()
	nd.labels[id] = labels
	return nd, nil
}

// ─── Reachability ─────────────────────────────────────────────────────────────

// reachable returns the set of all vertex IDs reachable from roots.
func (d *DAG) reachable(ctx context.Context, roots []core.VertexID, c *core.Constraints) map[core.VertexID]struct{} {
	reached := make(map[core.VertexID]struct{})
	var visit func(id core.VertexID)
	visit = func(id core.VertexID) {
		if _, ok := reached[id]; ok {
			return
		}
		reached[id] = struct{}{}
		v, ok := d.vertices[id]
		if !ok {
			return
		}
		for _, edge := range v.Inputs() {
			emv, err := edge.Vertex.Marshal(ctx, c)
			if err != nil {
				continue
			}
			visit(emv.Digest)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	return reached
}

// vertexDigest returns the digest of v under c, or "" on error.
func vertexDigest(ctx context.Context, v core.Vertex, c *core.Constraints) digest.Digest {
	if v == nil {
		return ""
	}
	mv, err := v.Marshal(ctx, c)
	if err != nil {
		return ""
	}
	return mv.Digest
}

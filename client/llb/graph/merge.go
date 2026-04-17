package graph

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/reactive"
)

// ─── MergeDAGs ───────────────────────────────────────────────────────────────

// MergeDAGs combines two or more DAGs into one, deduplicating shared
// sub-graphs by content address.
//
// The merged DAG's roots are the union of all source DAGs' roots.
// The shared event bus comes from the first DAG so existing subscribers
// continue to receive events.
func MergeDAGs(ctx context.Context, c *core.Constraints, dags ...*DAG) (*DAG, error) {
	if len(dags) == 0 {
		return nil, fmt.Errorf("MergeDAGs: at least one DAG required")
	}
	if len(dags) == 1 {
		return dags[0], nil
	}

	merged := dags[0].clone()
	added := 0

	for _, src := range dags[1:] {
		src.mu.RLock()
		for id, v := range src.vertices {
			if _, exists := merged.vertices[id]; !exists {
				merged.vertices[id] = v
				added++
			}
		}
		// Merge reverse adjacency.
		for id, consumers := range src.reverse {
			if merged.reverse[id] == nil {
				merged.reverse[id] = make(map[core.VertexID]struct{})
			}
			for cid := range consumers {
				merged.reverse[id][cid] = struct{}{}
			}
		}
		// Merge labels.
		for id, lbl := range src.labels {
			if _, exists := merged.labels[id]; !exists {
				merged.labels[id] = lbl
			}
		}
		// Union roots.
		for _, r := range src.roots {
			if !containsID(merged.roots, r) {
				merged.roots = append(merged.roots, r)
			}
		}
		src.mu.RUnlock()
	}

	merged.emit(reactive.GraphEvent{
		Kind:  reactive.EventKindDAGMerged,
		Count: added,
	})
	return merged, nil
}

// ─── DiffDAGs ────────────────────────────────────────────────────────────────

// DiffResult describes the structural difference between two DAGs.
type DiffResult struct {
	// Added contains vertex IDs present in B but not in A.
	Added []core.VertexID
	// Removed contains vertex IDs present in A but not in B.
	Removed []core.VertexID
	// Changed contains vertex IDs present in both but with different content.
	// (Impossible in a content-addressed DAG – same ID = same content.)
	// This field is always empty but kept for API completeness.
	Changed []core.VertexID
}

// DiffDAGs computes the structural difference between DAG a (before) and b (after).
func DiffDAGs(_ context.Context, a, b *DAG) DiffResult {
	a.mu.RLock()
	b.mu.RLock()
	defer a.mu.RUnlock()
	defer b.mu.RUnlock()

	var result DiffResult
	for id := range b.vertices {
		if _, ok := a.vertices[id]; !ok {
			result.Added = append(result.Added, id)
		}
	}
	for id := range a.vertices {
		if _, ok := b.vertices[id]; !ok {
			result.Removed = append(result.Removed, id)
		}
	}
	return result
}

// ─── SubDAG ───────────────────────────────────────────────────────────────────

// SubDAG extracts the sub-graph reachable from the given roots into a new DAG,
// optionally filtering vertices with an include predicate.
func SubDAG(
	ctx context.Context,
	d *DAG,
	c *core.Constraints,
	roots []core.VertexID,
	include func(id core.VertexID, v core.Vertex) bool,
) *DAG {
	reachable := d.reachable(ctx, roots, c)

	nd := &DAG{
		vertices: make(map[core.VertexID]core.Vertex),
		reverse:  make(map[core.VertexID]map[core.VertexID]struct{}),
		labels:   make(map[core.VertexID]core.Labels),
		roots:    roots,
		bus:      reactive.NewEventBus[reactive.GraphEvent](),
	}

	for id := range reachable {
		v := d.vertices[id]
		if include != nil && !include(id, v) {
			continue
		}
		nd.vertices[id] = v
		if lbl, ok := d.labels[id]; ok {
			nd.labels[id] = lbl
		}
		if consumers, ok := d.reverse[id]; ok {
			nd.reverse[id] = make(map[core.VertexID]struct{}, len(consumers))
			for cid := range consumers {
				if _, inReach := reachable[cid]; inReach {
					nd.reverse[id][cid] = struct{}{}
				}
			}
		}
	}
	return nd
}

// ─── Clone ────────────────────────────────────────────────────────────────────

// CloneDAG creates an independent copy of d with its own event bus.
func CloneDAG(d *DAG) *DAG {
	nd := d.clone()
	nd.bus = reactive.NewEventBus[reactive.GraphEvent]()
	return nd
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func containsID(ids []core.VertexID, id core.VertexID) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

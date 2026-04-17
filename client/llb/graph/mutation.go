package graph

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/reactive"
)

// ─── Mutator ─────────────────────────────────────────────────────────────────

// Mutator performs structural transformations on a DAG. Every method returns
// a new *DAG; the receiver is never modified.
type Mutator struct{ d *DAG }

// NewMutator wraps d for mutation.
func NewMutator(d *DAG) *Mutator { return &Mutator{d: d} }

// ─── Replace ──────────────────────────────────────────────────────────────────

// Replace substitutes every occurrence of targetID with replacement, then
// re-digests all affected descendants.
//
// Compatibility:
//   - replacement must expose at least as many output slots as targetID's
//     consumers require.
//   - replacement.Validate() must succeed.
func (m *Mutator) Replace(
	ctx context.Context,
	targetID core.VertexID,
	replacement core.Vertex,
	c *core.Constraints,
) (*DAG, error) {
	if _, err := m.d.Vertex(targetID); err != nil {
		return nil, fmt.Errorf("Replace: %w", err)
	}
	if err := replacement.Validate(ctx, c); err != nil {
		return nil, fmt.Errorf("Replace: validate replacement: %w", err)
	}

	// Output-slot compatibility check.
	maxIdx := m.maxConsumedOutputIndex(targetID)
	if maxIdx >= len(replacement.Outputs()) {
		return nil, &core.IncompatibleInputsError{
			VertexType: replacement.Type(),
			Got:        len(replacement.Outputs()),
			Want:       fmt.Sprintf("at least %d", maxIdx+1),
			Detail:     "replacement has fewer output slots than consumers require",
		}
	}

	nd := m.d.clone()
	if err := nd.propagateDigest(ctx, targetID, replacement, c); err != nil {
		return nil, fmt.Errorf("Replace: propagate: %w", err)
	}
	nd.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexReplaced, AffectedID: targetID})
	return nd, nil
}

// ─── Reparent ─────────────────────────────────────────────────────────────────

// Reparent changes the input edges of targetID to newInputs, re-digests the
// vertex, and propagates the change to all consumers.
//
// Every edge in newInputs must reference a vertex present in the DAG.
func (m *Mutator) Reparent(
	ctx context.Context,
	targetID core.VertexID,
	newInputs []core.Edge,
	c *core.Constraints,
) (*DAG, error) {
	target, err := m.d.Vertex(targetID)
	if err != nil {
		return nil, fmt.Errorf("Reparent: %w", err)
	}

	// Verify all new input vertices exist in the DAG.
	for i, edge := range newInputs {
		emv, err := edge.Vertex.Marshal(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("Reparent: marshal input[%d]: %w", i, err)
		}
		if _, err := m.d.Vertex(emv.Digest); err != nil {
			return nil, fmt.Errorf("Reparent: input[%d] not in DAG: %w", i, err)
		}
	}

	mut, ok := target.(core.MutatingVertex)
	if !ok {
		return nil, fmt.Errorf("Reparent: vertex type %q does not implement MutatingVertex", target.Type())
	}
	updated, err := mut.WithInputs(newInputs)
	if err != nil {
		return nil, fmt.Errorf("Reparent: %w", err)
	}

	nd := m.d.clone()
	if err := nd.propagateDigest(ctx, targetID, updated, c); err != nil {
		return nil, fmt.Errorf("Reparent: propagate: %w", err)
	}
	nd.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexReparented, AffectedID: targetID})
	return nd, nil
}

// ─── InsertBefore ────────────────────────────────────────────────────────────

// InsertBefore inserts newVertex between targetID's current producers and
// targetID itself:
//
//	producers → targetID   becomes   producers → newVertex → targetID
//
// newVertex receives the same inputs targetID currently has.
func (m *Mutator) InsertBefore(
	ctx context.Context,
	targetID core.VertexID,
	newVertex core.Vertex,
	c *core.Constraints,
) (*DAG, error) {
	target, err := m.d.Vertex(targetID)
	if err != nil {
		return nil, fmt.Errorf("InsertBefore: %w", err)
	}

	newMV, err := newVertex.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("InsertBefore: marshal new vertex: %w", err)
	}
	newID := newMV.Digest

	// Re-wire targetID's inputs to point at newVertex's outputs.
	originalInputs := target.Inputs()
	newTargetInputs := make([]core.Edge, len(originalInputs))
	for i := range originalInputs {
		newTargetInputs[i] = core.Edge{Vertex: newVertex, Index: i}
	}

	mut, ok := target.(core.MutatingVertex)
	if !ok {
		return nil, fmt.Errorf("InsertBefore: target type %q does not implement MutatingVertex", target.Type())
	}
	updatedTarget, err := mut.WithInputs(newTargetInputs)
	if err != nil {
		return nil, fmt.Errorf("InsertBefore: rewire target: %w", err)
	}

	nd := m.d.clone()
	// Register the new vertex.
	nd.vertices[newID] = newVertex
	// Build reverse edges from newVertex's own inputs.
	for _, edge := range originalInputs {
		emv, err := edge.Vertex.Marshal(ctx, c)
		if err != nil {
			return nil, err
		}
		if nd.reverse[emv.Digest] == nil {
			nd.reverse[emv.Digest] = make(map[core.VertexID]struct{})
		}
		nd.reverse[emv.Digest][newID] = struct{}{}
	}

	if err := nd.propagateDigest(ctx, targetID, updatedTarget, c); err != nil {
		return nil, fmt.Errorf("InsertBefore: propagate: %w", err)
	}
	nd.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded, AffectedID: newID})
	return nd, nil
}

// ─── Bypass ───────────────────────────────────────────────────────────────────

// Bypass removes targetID and connects its consumers directly to its single
// producer. Fails if the vertex has ≠1 input (ambiguous bypass).
func (m *Mutator) Bypass(
	ctx context.Context,
	targetID core.VertexID,
	c *core.Constraints,
) (*DAG, error) {
	target, err := m.d.Vertex(targetID)
	if err != nil {
		return nil, fmt.Errorf("Bypass: %w", err)
	}
	inputs := target.Inputs()
	if len(inputs) != 1 {
		return nil, fmt.Errorf("Bypass: vertex must have exactly 1 input (has %d)", len(inputs))
	}

	upstream := inputs[0].Vertex
	nd := m.d.clone()

	for consID := range nd.reverse[targetID] {
		cons, ok := nd.vertices[consID]
		if !ok {
			continue
		}
		mut, ok := cons.(core.MutatingVertex)
		if !ok {
			return nil, fmt.Errorf("Bypass: consumer %q does not implement MutatingVertex", consID)
		}
		newConsInputs := make([]core.Edge, len(cons.Inputs()))
		for i, edge := range cons.Inputs() {
			emv, err := edge.Vertex.Marshal(ctx, c)
			if err != nil {
				return nil, err
			}
			if emv.Digest == targetID {
				newConsInputs[i] = core.Edge{Vertex: upstream, Index: edge.Index}
			} else {
				newConsInputs[i] = edge
			}
		}
		updated, err := mut.WithInputs(newConsInputs)
		if err != nil {
			return nil, fmt.Errorf("Bypass: rewire consumer: %w", err)
		}
		if err := nd.propagateDigest(ctx, consID, updated, c); err != nil {
			return nil, fmt.Errorf("Bypass: propagate: %w", err)
		}
	}

	// Remove the bypassed vertex.
	delete(nd.vertices, targetID)
	delete(nd.reverse, targetID)

	nd.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexRemoved, AffectedID: targetID})
	return nd, nil
}

// ─── Append ───────────────────────────────────────────────────────────────────

// Append chains newVertex after afterID (uses afterID as its sole input) and
// adds it as a new root in the DAG.
func (m *Mutator) Append(
	ctx context.Context,
	afterID core.VertexID,
	newVertex core.Vertex,
	c *core.Constraints,
) (*DAG, error) {
	if _, err := m.d.Vertex(afterID); err != nil {
		return nil, fmt.Errorf("Append: %w", err)
	}
	if err := newVertex.Validate(ctx, c); err != nil {
		return nil, fmt.Errorf("Append: %w", err)
	}
	newMV, err := newVertex.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("Append: marshal: %w", err)
	}
	newID := newMV.Digest

	nd := m.d.clone()
	nd.vertices[newID] = newVertex
	if nd.reverse[afterID] == nil {
		nd.reverse[afterID] = make(map[core.VertexID]struct{})
	}
	nd.reverse[afterID][newID] = struct{}{}
	nd.roots = append(nd.roots, newID)

	nd.emit(reactive.GraphEvent{Kind: reactive.EventKindVertexAdded, AffectedID: newID})
	return nd, nil
}

// ─── Prune ────────────────────────────────────────────────────────────────────

// Prune removes all vertices not reachable from the given root IDs (or from
// the DAG's existing roots if rootIDs is empty). Returns the pruned DAG and
// the count of removed vertices.
func (m *Mutator) Prune(
	ctx context.Context,
	c *core.Constraints,
	rootIDs ...core.VertexID,
) (*DAG, int, error) {
	roots := rootIDs
	if len(roots) == 0 {
		roots = m.d.Roots()
	}

	reachable := m.d.reachable(ctx, roots, c)
	removed := 0

	nd := m.d.clone()
	for id := range nd.vertices {
		if _, ok := reachable[id]; !ok {
			delete(nd.vertices, id)
			delete(nd.reverse, id)
			delete(nd.labels, id)
			removed++
		}
	}
	// Re-filter roots.
	newRoots := make([]core.VertexID, 0, len(roots))
	for _, r := range roots {
		if _, ok := nd.vertices[r]; ok {
			newRoots = append(newRoots, r)
		}
	}
	nd.roots = newRoots

	nd.emit(reactive.GraphEvent{
		Kind:  reactive.EventKindDAGPruned,
		Count: removed,
	})
	return nd, removed, nil
}

// ─── UpdateField ─────────────────────────────────────────────────────────────

// UpdateField replaces a vertex with a new one built by applying fn to its
// current value, then propagates the digest change.
// fn receives the current vertex and must return a new, valid vertex.
// This is a convenience wrapper around Replace.
func (m *Mutator) UpdateField(
	ctx context.Context,
	targetID core.VertexID,
	fn func(ctx context.Context, current core.Vertex) (core.Vertex, error),
	c *core.Constraints,
) (*DAG, error) {
	current, err := m.d.Vertex(targetID)
	if err != nil {
		return nil, fmt.Errorf("UpdateField: %w", err)
	}
	replacement, err := fn(ctx, current)
	if err != nil {
		return nil, fmt.Errorf("UpdateField: %w", err)
	}
	return m.Replace(ctx, targetID, replacement, c)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (m *Mutator) maxConsumedOutputIndex(targetID core.VertexID) int {
	max := -1
	for consID := range m.d.reverse[targetID] {
		cons, ok := m.d.vertices[consID]
		if !ok {
			continue
		}
		for _, edge := range cons.Inputs() {
			if edge.Index > max {
				max = edge.Index
			}
		}
	}
	return max
}

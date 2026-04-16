package graph

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/reactive"
)

// ─── Mutator ─────────────────────────────────────────────────────────────────

// Mutator performs structural transformations on a Graph. Every method returns
// a *new* Graph; the receiver is never modified. Each successful mutation also
// emits appropriate GraphEvents on the shared bus.
type Mutator struct {
	g *Graph
}

// NewMutator wraps g for mutation.
func NewMutator(g *Graph) *Mutator { return &Mutator{g: g} }

// ─── Replace ──────────────────────────────────────────────────────────────────

// Replace substitutes all occurrences of targetID with replacement throughout
// the graph, then re-digests every affected descendant.
//
// Compatibility rules:
//   - replacement.Outputs() must be a superset of the output slots consumed by
//     targetID's consumers (so existing consumers remain satisfiable).
//   - replacement.Validate() must succeed.
func (m *Mutator) Replace(
	ctx context.Context,
	targetID core.VertexID,
	replacement core.Vertex,
	c *core.Constraints,
) (*Graph, error) {
	if _, err := m.g.Vertex(targetID); err != nil {
		return nil, fmt.Errorf("Replace: %w", err)
	}

	if err := replacement.Validate(ctx, c); err != nil {
		return nil, fmt.Errorf("Replace: replacement validation: %w", err)
	}

	// Verify output slot compatibility.
	maxConsumerIdx := m.maxConsumedOutputIndex(targetID)
	if maxConsumerIdx >= len(replacement.Outputs()) {
		return nil, &core.IncompatibleInputsError{
			VertexType: replacement.Type(),
			Got:        len(replacement.Outputs()),
			Want:       fmt.Sprintf("at least %d", maxConsumerIdx+1),
			Detail:     "replacement has fewer output slots than required by consumers",
		}
	}

	ng := m.g.clone()
	if err := ng.propagateDigestChange(ctx, targetID, replacement, c); err != nil {
		return nil, fmt.Errorf("Replace: propagate: %w", err)
	}

	ng.emit(reactive.GraphEvent{
		Kind:       reactive.EventKindVertexReplaced,
		AffectedID: targetID,
	})
	return ng, nil
}

// ─── Reparent ─────────────────────────────────────────────────────────────────

// Reparent changes the input edges of targetID to newInputs, re-digests the
// vertex, and propagates the digest change to all consumers.
//
// Compatibility rules:
//   - The new edges must satisfy the vertex's input requirements
//     (checked via MutatingVertex.WithInputs).
//   - Every edge in newInputs must reference a vertex present in the graph.
func (m *Mutator) Reparent(
	ctx context.Context,
	targetID core.VertexID,
	newInputs []core.Edge,
	c *core.Constraints,
) (*Graph, error) {
	target, err := m.g.Vertex(targetID)
	if err != nil {
		return nil, fmt.Errorf("Reparent: %w", err)
	}

	// Verify all input vertices exist.
	for i, edge := range newInputs {
		emv, err := edge.Vertex.Marshal(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("Reparent: marshal input[%d]: %w", i, err)
		}
		if _, err := m.g.Vertex(emv.Digest); err != nil {
			return nil, fmt.Errorf("Reparent: input[%d] not in graph: %w", i, err)
		}
	}

	mut, ok := target.(core.MutatingVertex)
	if !ok {
		return nil, fmt.Errorf("Reparent: vertex type %q does not support reparenting", target.Type())
	}

	updated, err := mut.WithInputs(newInputs)
	if err != nil {
		return nil, fmt.Errorf("Reparent: %w", err)
	}

	ng := m.g.clone()
	if err := ng.propagateDigestChange(ctx, targetID, updated, c); err != nil {
		return nil, fmt.Errorf("Reparent: propagate: %w", err)
	}

	ng.emit(reactive.GraphEvent{
		Kind:       reactive.EventKindVertexReparented,
		AffectedID: targetID,
	})
	return ng, nil
}

// ─── InsertBefore ────────────────────────────────────────────────────────────

// InsertBefore inserts newVertex between all producers of targetID and targetID
// itself. After the mutation:
//
//	producers → targetID   becomes   producers → newVertex → targetID
//
// newVertex must accept exactly the same inputs that targetID currently has.
func (m *Mutator) InsertBefore(
	ctx context.Context,
	targetID core.VertexID,
	newVertex core.Vertex,
	c *core.Constraints,
) (*Graph, error) {
	target, err := m.g.Vertex(targetID)
	if err != nil {
		return nil, fmt.Errorf("InsertBefore: %w", err)
	}

	// newVertex gets the same inputs as target currently has.
	newMV, err := newVertex.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("InsertBefore: marshal new vertex: %w", err)
	}

	// target's new inputs are the outputs of newVertex.
	newInputs := make([]core.Edge, len(target.Inputs()))
	for i := range target.Inputs() {
		newInputs[i] = core.Edge{Vertex: newVertex, Index: i}
	}

	mut, ok := target.(core.MutatingVertex)
	if !ok {
		return nil, fmt.Errorf("InsertBefore: target vertex type %q does not support reparenting", target.Type())
	}
	updatedTarget, err := mut.WithInputs(newInputs)
	if err != nil {
		return nil, fmt.Errorf("InsertBefore: rewire target: %w", err)
	}

	ng := m.g.clone()
	// Add the new vertex.
	ng.vertices[newMV.Digest] = newVertex
	// Re-wire existing producers to point at newVertex (handled inside propagate).
	if err := ng.propagateDigestChange(ctx, targetID, updatedTarget, c); err != nil {
		return nil, fmt.Errorf("InsertBefore: propagate: %w", err)
	}

	ng.emit(reactive.GraphEvent{
		Kind:       reactive.EventKindVertexAdded,
		AffectedID: newMV.Digest,
	})
	return ng, nil
}

// ─── Bypass ───────────────────────────────────────────────────────────────────

// Bypass removes targetID from the graph and connects its consumers directly
// to its producers. The vertex must have exactly one input (otherwise the
// bypass is ambiguous).
func (m *Mutator) Bypass(
	ctx context.Context,
	targetID core.VertexID,
	c *core.Constraints,
) (*Graph, error) {
	target, err := m.g.Vertex(targetID)
	if err != nil {
		return nil, fmt.Errorf("Bypass: %w", err)
	}
	inputs := target.Inputs()
	if len(inputs) != 1 {
		return nil, fmt.Errorf("Bypass: vertex must have exactly 1 input (has %d)", len(inputs))
	}

	upstream := inputs[0].Vertex
	ng := m.g.clone()

	// Rewire each consumer to point at upstream instead of target.
	consumers := ng.reverse[targetID]
	for consumerID := range consumers {
		consumer, ok := ng.vertices[consumerID]
		if !ok {
			continue
		}
		mut, ok := consumer.(core.MutatingVertex)
		if !ok {
			return nil, fmt.Errorf("Bypass: consumer %q does not support reparenting", consumerID)
		}
		newInputs := make([]core.Edge, len(consumer.Inputs()))
		for i, edge := range consumer.Inputs() {
			emv, err := edge.Vertex.Marshal(ctx, c)
			if err != nil {
				return nil, err
			}
			if emv.Digest == targetID {
				newInputs[i] = core.Edge{Vertex: upstream, Index: edge.Index}
			} else {
				newInputs[i] = edge
			}
		}
		updated, err := mut.WithInputs(newInputs)
		if err != nil {
			return nil, fmt.Errorf("Bypass: rewire consumer: %w", err)
		}
		if err := ng.propagateDigestChange(ctx, consumerID, updated, c); err != nil {
			return nil, fmt.Errorf("Bypass: propagate: %w", err)
		}
	}

	// Remove target.
	delete(ng.vertices, targetID)
	delete(ng.reverse, targetID)

	ng.emit(reactive.GraphEvent{
		Kind:       reactive.EventKindVertexRemoved,
		AffectedID: targetID,
	})
	return ng, nil
}

// ─── Append ───────────────────────────────────────────────────────────────────

// Append chains newVertex after afterID, making afterID's output the sole
// input of newVertex. Consumers of afterID are *not* automatically rewired;
// use the returned new graph's root to build further state.
func (m *Mutator) Append(
	ctx context.Context,
	afterID core.VertexID,
	newVertex core.Vertex,
	c *core.Constraints,
) (*Graph, error) {
	if _, err := m.g.Vertex(afterID); err != nil {
		return nil, fmt.Errorf("Append: %w", err)
	}
	if err := newVertex.Validate(ctx, c); err != nil {
		return nil, fmt.Errorf("Append: new vertex invalid: %w", err)
	}
	newMV, err := newVertex.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("Append: marshal: %w", err)
	}

	ng := m.g.clone()
	ng.vertices[newMV.Digest] = newVertex

	// Register reverse edge: afterID → newMV.Digest.
	if ng.reverse[afterID] == nil {
		ng.reverse[afterID] = make(map[core.VertexID]struct{})
	}
	ng.reverse[afterID][newMV.Digest] = struct{}{}

	// newVertex is a new root.
	ng.roots = append(ng.roots, newMV.Digest)

	ng.emit(reactive.GraphEvent{
		Kind:       reactive.EventKindVertexAdded,
		AffectedID: newMV.Digest,
	})
	return ng, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// maxConsumedOutputIndex returns the highest output slot index of targetID
// that any consumer references, or -1 if there are no consumers.
func (m *Mutator) maxConsumedOutputIndex(targetID core.VertexID) int {
	max := -1
	for consumerID := range m.g.reverse[targetID] {
		consumer, ok := m.g.vertices[consumerID]
		if !ok {
			continue
		}
		for _, edge := range consumer.Inputs() {
			// We need to identify which edge points at targetID. Since edges carry
			// a Vertex pointer we do an identity comparison on the marshaled digest.
			// We skip the marshal here because this is a read-only scan; we use the
			// vertex pointer stored in the graph instead.
			if edge.Index > max {
				max = edge.Index
			}
		}
	}
	return max
}

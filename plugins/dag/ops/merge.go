package ops

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── MergeOp ──────────────────────────────────────────────────────────────────

// MergeOp overlays multiple filesystem layers into a single result.
// Inputs are applied in order — the last input takes precedence for conflicting paths.
// A merge of N scratch inputs collapses to scratch; a merge of one non-scratch
// input collapses to that input (handled at the State layer, not here).
type MergeOp struct {
	id          string
	inputs      []vertex.Vertex
	refs        []vertex.Ref
	constraints Constraints
}

var _ vertex.Vertex = (*MergeOp)(nil)
var _ vertex.Named = (*MergeOp)(nil)

// NewMergeOp creates a MergeOp from the given input refs.
// refs must contain at least 2 non-scratch entries for the op to be valid.
func NewMergeOp(refs []vertex.Ref, c Constraints) *MergeOp {
	// Collect distinct non-scratch input vertices.
	var inputs []vertex.Vertex
	seen := make(map[string]bool)
	for _, ref := range refs {
		if ref.IsZero() {
			continue
		}
		id := ref.Vertex.ID()
		if !seen[id] {
			seen[id] = true
			inputs = append(inputs, ref.Vertex)
		}
	}

	m := &MergeOp{
		inputs:      inputs,
		refs:        refs,
		constraints: c,
	}
	m.id = m.computeID()
	return m
}

func (m *MergeOp) computeID() string {
	type refID struct {
		VertexID string `json:"vertex_id"`
		Index    int    `json:"index"`
	}
	rIDs := make([]refID, 0, len(m.refs))
	for _, r := range m.refs {
		if r.IsZero() {
			rIDs = append(rIDs, refID{})
		} else {
			rIDs = append(rIDs, refID{VertexID: r.Vertex.ID(), Index: r.Index})
		}
	}
	return idOf(struct {
		Kind     string    `json:"kind"`
		Inputs   []refID   `json:"inputs"`
		Platform *Platform `json:"platform,omitempty"`
	}{
		Kind:     string(vertex.KindMerge),
		Inputs:   rIDs,
		Platform: m.constraints.Platform,
	})
}

func (m *MergeOp) ID() string               { return m.id }
func (m *MergeOp) Kind() vertex.Kind        { return vertex.KindMerge }
func (m *MergeOp) Inputs() []vertex.Vertex  { return m.inputs }
func (m *MergeOp) Refs() []vertex.Ref       { return m.refs }
func (m *MergeOp) Constraints() Constraints { return m.constraints }
func (m *MergeOp) Name() string             { return fmt.Sprintf("merge(%d inputs)", len(m.refs)) }

func (m *MergeOp) Validate(ctx context.Context) error {
	if len(m.refs) < 2 {
		return fmt.Errorf("merge: at least 2 inputs are required, got %d", len(m.refs))
	}
	return nil
}

// Ref returns a reference to the merged output.
func (m *MergeOp) Ref() vertex.Ref { return vertex.Ref{Vertex: m, Index: 0} }

// Merge creates a MergeOp from the given refs using the functional-options pattern.
// Scratch (zero) refs are filtered out. If only one non-scratch ref remains,
// it is returned directly without constructing a MergeOp.
func Merge(refs []vertex.Ref, c Constraints) vertex.Ref {
	var nonScratch []vertex.Ref
	for _, r := range refs {
		if !r.IsZero() {
			nonScratch = append(nonScratch, r)
		}
	}
	switch len(nonScratch) {
	case 0:
		return vertex.Ref{} // scratch
	case 1:
		return nonScratch[0] // passthrough
	default:
		return NewMergeOp(nonScratch, c).Ref()
	}
}

// ─── DiffOp ───────────────────────────────────────────────────────────────────

// DiffOp computes the layer delta between a lower and upper filesystem.
// The result contains only the files that are new or modified in upper
// relative to lower. This is the inverse of Merge and is typically used
// with Merge to implement incremental, non-overlapping build steps:
//
//	base := ops.Image("alpine").Ref()
//	built := ops.Image("alpine").Exec(...)... // some mutation
//	delta := ops.Diff(base, built.RootRef(), c)
//	final := ops.Merge([]vertex.Ref{base, delta}, c)
type DiffOp struct {
	id          string
	lower       vertex.Ref
	upper       vertex.Ref
	inputs      []vertex.Vertex
	constraints Constraints
}

var _ vertex.Vertex = (*DiffOp)(nil)
var _ vertex.Named = (*DiffOp)(nil)

// NewDiffOp creates a DiffOp between lower and upper filesystems.
func NewDiffOp(lower, upper vertex.Ref, c Constraints) *DiffOp {
	d := &DiffOp{
		lower:       lower,
		upper:       upper,
		constraints: c,
	}

	// Build input list from distinct non-scratch refs.
	seen := make(map[string]bool)
	for _, ref := range []vertex.Ref{lower, upper} {
		if !ref.IsZero() {
			id := ref.Vertex.ID()
			if !seen[id] {
				seen[id] = true
				d.inputs = append(d.inputs, ref.Vertex)
			}
		}
	}

	d.id = d.computeID()
	return d
}

func (d *DiffOp) computeID() string {
	lowerID, upperID := "", ""
	if !d.lower.IsZero() {
		lowerID = fmt.Sprintf("%s:%d", d.lower.Vertex.ID(), d.lower.Index)
	}
	if !d.upper.IsZero() {
		upperID = fmt.Sprintf("%s:%d", d.upper.Vertex.ID(), d.upper.Index)
	}
	return idOf(struct {
		Kind     string    `json:"kind"`
		Lower    string    `json:"lower,omitempty"`
		Upper    string    `json:"upper,omitempty"`
		Platform *Platform `json:"platform,omitempty"`
	}{
		Kind:     string(vertex.KindDiff),
		Lower:    lowerID,
		Upper:    upperID,
		Platform: d.constraints.Platform,
	})
}

func (d *DiffOp) ID() string               { return d.id }
func (d *DiffOp) Kind() vertex.Kind        { return vertex.KindDiff }
func (d *DiffOp) Inputs() []vertex.Vertex  { return d.inputs }
func (d *DiffOp) Lower() vertex.Ref        { return d.lower }
func (d *DiffOp) Upper() vertex.Ref        { return d.upper }
func (d *DiffOp) Constraints() Constraints { return d.constraints }
func (d *DiffOp) Name() string             { return "diff" }

func (d *DiffOp) Validate(ctx context.Context) error {
	// Both lower and upper may be scratch (diff of scratch and scratch = scratch).
	// The only invalid case is if upper is missing but lower is set — that would
	// mean "remove everything from lower", which is better expressed as Scratch().
	return nil
}

// Ref returns a reference to the diff output.
func (d *DiffOp) Ref() vertex.Ref { return vertex.Ref{Vertex: d, Index: 0} }

// Diff is a convenience constructor that handles the scratch-passthrough cases:
//   - Diff(scratch, scratch) → scratch
//   - Diff(scratch, upper) → upper (diff from empty = full upper)
func Diff(lower, upper vertex.Ref, c Constraints) vertex.Ref {
	if lower.IsZero() && upper.IsZero() {
		return vertex.Ref{}
	}
	if lower.IsZero() {
		return upper
	}
	return NewDiffOp(lower, upper, c).Ref()
}

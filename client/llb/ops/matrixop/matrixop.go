// Package matrixop implements the MatrixOp vertex — a Cartesian product
// expansion of operations across multiple axes (e.g. Go versions × OS targets).
// Each combination produces an independent state; results are merged.
package matrixop

import (
	"context"
	"sort"
	"strings"

	"github.com/bons/bons-ci/client/llb"
	"github.com/bons/bons-ci/client/llb/ops/mergeop"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// Axis
// ─────────────────────────────────────────────────────────────────────────────

// Axis represents a single dimension of the matrix with named values.
type Axis struct {
	Name   string
	Values []string
}

// ─────────────────────────────────────────────────────────────────────────────
// MatrixOp
// ─────────────────────────────────────────────────────────────────────────────

// MatrixOp expands a template function across the Cartesian product of N axes.
// For each cell in the matrix, the MatrixFn builds a State. All resulting
// states are merged into a single output via MergeOp.
//
// Example: 2 Go versions × 3 OS targets = 6 parallel states.
type MatrixOp struct {
	cache       llb.MarshalCache
	axes        []Axis
	fn          MatrixFn
	constraints llb.Constraints
	output      llb.Output

	// expanded stores the lazily-computed merged vertex.
	expanded *mergeExpansion
}

// mergeExpansion caches the expanded matrix result.
type mergeExpansion struct {
	cells  []map[string]string
	states []llb.State
}

// MatrixFn builds a State for each cell in the matrix. The map keys are axis
// names and values are the current axis value for this cell.
type MatrixFn func(cell map[string]string) llb.State

var _ llb.Vertex = (*MatrixOp)(nil)

// NewMatrixOp creates a MatrixOp.
func NewMatrixOp(axes []Axis, fn MatrixFn, c llb.Constraints) *MatrixOp {
	op := &MatrixOp{
		axes:        axes,
		fn:          fn,
		constraints: c,
	}
	op.output = llb.NewOutput(op)
	return op
}

// expand performs the Cartesian product and builds all cell states.
func (m *MatrixOp) expand() *mergeExpansion {
	if m.expanded != nil {
		return m.expanded
	}

	cells := cartesianProduct(m.axes)
	states := make([]llb.State, 0, len(cells))
	for _, cell := range cells {
		states = append(states, m.fn(cell))
	}

	m.expanded = &mergeExpansion{cells: cells, states: states}
	return m.expanded
}

// Validate checks the matrix op.
func (m *MatrixOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if len(m.axes) == 0 {
		return errors.New("matrix op requires at least one axis")
	}
	for _, a := range m.axes {
		if len(a.Values) == 0 {
			return errors.Errorf("matrix axis %q must have at least one value", a.Name)
		}
	}
	if m.fn == nil {
		return errors.New("matrix op requires a MatrixFn")
	}
	return nil
}

// Marshal expands the matrix, merges all cell states, and marshals the merged
// result. The matrix metadata is embedded as custom attributes.
func (m *MatrixOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := m.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := m.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	exp := m.expand()

	if len(exp.states) == 0 {
		return "", nil, nil, nil, errors.New("matrix expansion produced no states")
	}

	// Single cell — just marshal the state directly.
	if len(exp.states) == 1 {
		st := exp.states[0]
		if st.Output() == nil {
			return "", nil, nil, nil, errors.New("matrix cell produced scratch state")
		}
		v := st.Output().Vertex(ctx, constraints)
		return v.Marshal(ctx, constraints)
	}

	// Multiple cells — merge them.
	merged := mergeop.Merge(exp.states)
	v := merged.Output().Vertex(ctx, constraints)
	return v.Marshal(ctx, constraints)
}

// Output returns the merged output.
func (m *MatrixOp) Output() llb.Output { return m.output }

// Inputs returns all expanded cell outputs.
func (m *MatrixOp) Inputs() []llb.Output {
	exp := m.expand()
	var inputs []llb.Output
	for _, st := range exp.states {
		if st.Output() != nil {
			inputs = append(inputs, st.Output())
		}
	}
	return inputs
}

// ─────────────────────────────────────────────────────────────────────────────
// Cartesian product
// ─────────────────────────────────────────────────────────────────────────────

// cartesianProduct generates all combinations of axis values. Result order is
// deterministic: axes are sorted by name, values within each axis maintain
// insertion order.
func cartesianProduct(axes []Axis) []map[string]string {
	if len(axes) == 0 {
		return nil
	}

	// Sort axes by name for determinism.
	sorted := make([]Axis, len(axes))
	copy(sorted, axes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	// Start with one empty cell.
	cells := []map[string]string{{}}

	for _, axis := range sorted {
		var expanded []map[string]string
		for _, cell := range cells {
			for _, val := range axis.Values {
				newCell := make(map[string]string, len(cell)+1)
				for k, v := range cell {
					newCell[k] = v
				}
				newCell[axis.Name] = val
				expanded = append(expanded, newCell)
			}
		}
		cells = expanded
	}

	return cells
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructors
// ─────────────────────────────────────────────────────────────────────────────

// Matrix creates a state by expanding the template function across all
// combinations of the given axes.
func Matrix(axes []Axis, fn MatrixFn, opts ...llb.ConstraintsOpt) llb.State {
	var c llb.Constraints
	for _, o := range opts {
		o.SetConstraintsOption(&c)
	}
	op := NewMatrixOp(axes, fn, c)
	return llb.NewState(op.Output())
}

// CellKey generates a deterministic key string for a matrix cell. Useful for
// tagging cell outputs.
func CellKey(cell map[string]string) string {
	keys := make([]string, 0, len(cell))
	for k := range cell {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+cell[k])
	}
	return strings.Join(parts, ",")
}

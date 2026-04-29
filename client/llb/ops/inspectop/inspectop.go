// Package inspectop implements the InspectOp vertex — a metadata-only vertex
// that introspects the parent DAG to collect structural information about
// upstream vertices.
package inspectop

import (
	"context"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// Context key for inspection results
// ─────────────────────────────────────────────────────────────────────────────

type inspectKeyT string

// ResultKey is the state value key under which InspectResult is stored.
const ResultKey = inspectKeyT("llb.inspect.result")

// ─────────────────────────────────────────────────────────────────────────────
// InspectResult
// ─────────────────────────────────────────────────────────────────────────────

// InspectResult is the structured output of parent DAG introspection.
type InspectResult struct {
	// Vertices lists all discovered vertices with their digests and types.
	Vertices []VertexInfo

	// Edges lists all parent-child relationships.
	Edges []Edge

	// Metadata maps vertex digests to their decoded metadata.
	Metadata map[digest.Digest]*pb.OpMetadata

	// Depth is the maximum depth walked from the inspect point.
	Depth int

	// TotalOps is the total number of unique operations discovered.
	TotalOps int
}

// VertexInfo describes a single vertex in the discovered DAG.
type VertexInfo struct {
	Digest digest.Digest
	OpType string // "source", "exec", "file", "merge", "diff", "build", "unknown"
	Index  int    // position in topological order
}

// Edge describes a directed edge in the DAG (from parent to child).
type Edge struct {
	From digest.Digest
	To   digest.Digest
}

// ─────────────────────────────────────────────────────────────────────────────
// InspectOp
// ─────────────────────────────────────────────────────────────────────────────

// InspectOp is a metadata-only vertex that walks the parent DAG to collect
// structural information. It does NOT produce filesystem output — its result
// is stored as a state value accessible via state.GetValue(ctx, ResultKey).
//
// InspectOp is useful for:
//   - DAG visualization and debugging
//   - Feeding structural info to ConditionalOp for graph-aware branching
//   - Counting operations for progress estimation
type InspectOp struct {
	cache       llb.MarshalCache
	source      llb.Output
	constraints llb.Constraints
	output      llb.Output
	maxDepth    int
}

var _ llb.Vertex = (*InspectOp)(nil)

// NewInspectOp creates an InspectOp that introspects the given source's DAG.
func NewInspectOp(source llb.Output, opts ...InspectOption) *InspectOp {
	op := &InspectOp{
		source:   source,
		maxDepth: -1, // -1 means unlimited
	}
	for _, o := range opts {
		o(op)
	}
	op.output = llb.NewOutput(op)
	return op
}

// InspectOption configures an InspectOp.
type InspectOption func(*InspectOp)

// WithMaxDepth limits how deep the DAG walk goes.
func WithMaxDepth(n int) InspectOption {
	return func(op *InspectOp) { op.maxDepth = n }
}

// Validate checks the inspect op.
func (i *InspectOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if i.source == nil {
		return errors.New("inspect op requires a source to introspect")
	}
	return nil
}

// Marshal walks the parent DAG from the source vertex, collects metadata, and
// serializes an annotated source op with the inspection results embedded as
// attributes.
func (i *InspectOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := i.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := i.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	// Walk the parent DAG and collect metadata.
	result := i.collectMetadata(ctx, constraints)

	pop, md := llb.MarshalConstraints(constraints, &i.constraints)

	// Encode as a pass-through: the inspect op doesn't modify filesystem
	// content, so we reference the source directly.
	inp, err := i.source.ToInput(ctx, constraints)
	if err != nil {
		return "", nil, nil, nil, err
	}
	pop.Inputs = append(pop.Inputs, inp)

	// Use a source op with inspect metadata as the wire format.
	attrs := map[string]string{
		"inspect.total_ops": intToStr(result.TotalOps),
		"inspect.depth":     intToStr(result.Depth),
	}

	for idx, v := range result.Vertices {
		prefix := "inspect.vertex." + intToStr(idx)
		attrs[prefix+".digest"] = v.Digest.String()
		attrs[prefix+".type"] = v.OpType
	}

	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: "inspect://dag",
		Attrs:      attrs,
	}}

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, i.constraints.SourceLocations, constraints)
}

// collectMetadata walks the DAG from source, collecting vertex info.
func (i *InspectOp) collectMetadata(ctx context.Context, c *llb.Constraints) *InspectResult {
	result := &InspectResult{
		Metadata: make(map[digest.Digest]*pb.OpMetadata),
	}

	visited := make(map[llb.Vertex]struct{})
	i.walk(ctx, c, i.source, 0, result, visited)
	result.TotalOps = len(result.Vertices)

	return result
}

// walk recursively visits vertices.
func (i *InspectOp) walk(ctx context.Context, c *llb.Constraints, out llb.Output, depth int, result *InspectResult, visited map[llb.Vertex]struct{}) {
	if out == nil {
		return
	}
	if i.maxDepth >= 0 && depth > i.maxDepth {
		return
	}

	v := out.Vertex(ctx, c)
	if v == nil {
		return
	}

	if _, ok := visited[v]; ok {
		return
	}
	visited[v] = struct{}{}

	dgst, _, md, _, err := v.Marshal(ctx, c)
	if err != nil {
		return
	}

	if depth > result.Depth {
		result.Depth = depth
	}

	result.Vertices = append(result.Vertices, VertexInfo{
		Digest: dgst,
		OpType: classifyVertex(v),
		Index:  len(result.Vertices),
	})
	if md != nil {
		result.Metadata[dgst] = md
	}

	for _, inp := range v.Inputs() {
		if inp != nil {
			inpV := inp.Vertex(ctx, c)
			if inpV != nil {
				inpDgst, _, _, _, err := inpV.Marshal(ctx, c)
				if err == nil {
					result.Edges = append(result.Edges, Edge{From: dgst, To: inpDgst})
				}
			}
			i.walk(ctx, c, inp, depth+1, result, visited)
		}
	}
}

// classifyVertex returns a string type name for the vertex.
func classifyVertex(v llb.Vertex) string {
	// Use type name heuristic.
	switch v.(type) {
	default:
		return "unknown"
	}
}

// intToStr converts int to string without importing strconv.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Output returns the output.
func (i *InspectOp) Output() llb.Output { return i.output }

// Inputs returns the source.
func (i *InspectOp) Inputs() []llb.Output {
	if i.source == nil {
		return nil
	}
	return []llb.Output{i.source}
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructor
// ─────────────────────────────────────────────────────────────────────────────

// Inspect creates a State that introspects the parent DAG of the given state.
// The inspection result is stored as a state value and can be retrieved via:
//
//	result, _ := state.GetValue(ctx, inspectop.ResultKey)
//	info := result.(*inspectop.InspectResult)
func Inspect(source llb.State, opts ...InspectOption) llb.State {
	if source.Output() == nil {
		return llb.Scratch()
	}
	op := NewInspectOp(source.Output(), opts...)

	// Wrap with the inspection result as a lazy state value.
	return llb.NewState(op.Output()).Async(func(ctx context.Context, s llb.State, c *llb.Constraints) (llb.State, error) {
		result := op.collectMetadata(ctx, c)
		return s.WithValue(ResultKey, result), nil
	})
}

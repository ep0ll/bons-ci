// Package definition provides the DefinitionOp vertex, which reconstructs a
// vertex graph from a marshalled pb.Definition. This enables composing sub-builds
// received over the wire and is equivalent to BuildKit's DefinitionOp.
package definition

import (
	"context"
	"fmt"
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/protobuf/proto"
)

// ─── DefinitionOp ────────────────────────────────────────────────────────────

// DefinitionOp implements core.Vertex using a marshalled pb.Definition.
// It allows the entire vertex graph to be reconstructed from wire-format bytes,
// enabling composition of sub-builds and remote definitions.
type DefinitionOp struct {
	mu        sync.Mutex
	ops       map[digest.Digest]*pb.Op
	defs      map[digest.Digest][]byte
	metas     map[digest.Digest]*pb.OpMetadata
	sources   map[digest.Digest][]*core.SourceLocation
	platforms map[digest.Digest]*ocispecs.Platform
	dgst      digest.Digest
	index     pb.OutputIndex

	// inputCache is shared among sub-DefinitionOps to avoid re-parsing.
	inputCache sync.Map // map[string][]*DefinitionOp
}

// NewDefinitionOp constructs a DefinitionOp from a marshalled pb.Definition.
func NewDefinitionOp(def *pb.Definition) (*DefinitionOp, error) {
	if def == nil || len(def.Def) == 0 {
		return nil, fmt.Errorf("definition: nil or empty definition")
	}

	ops := make(map[digest.Digest]*pb.Op, len(def.Def))
	defs := make(map[digest.Digest][]byte, len(def.Def))
	plats := make(map[digest.Digest]*ocispecs.Platform, len(def.Def))

	var last digest.Digest
	for _, dt := range def.Def {
		var op pb.Op
		if err := proto.Unmarshal(dt, &op); err != nil {
			return nil, fmt.Errorf("definition: unmarshal op: %w", err)
		}
		dgst := digest.FromBytes(dt)
		ops[dgst] = &op
		defs[dgst] = dt
		last = dgst

		if op.Platform != nil {
			spec := op.Platform.Spec()
			plats[dgst] = &spec
		}
	}

	// Resolve the terminal op's input to get the real head.
	var idx pb.OutputIndex
	dgst := last
	if dgst != "" {
		termOp := ops[dgst]
		if len(termOp.Inputs) > 0 {
			idx = pb.OutputIndex(termOp.Inputs[0].Index)
			dgst = digest.Digest(termOp.Inputs[0].Digest)
		}
	}

	metas := make(map[digest.Digest]*pb.OpMetadata, len(def.Metadata))
	for k, v := range def.Metadata {
		metas[digest.Digest(k)] = v
	}

	srcs := make(map[digest.Digest][]*core.SourceLocation)
	// Source location parsing would go here if needed.

	return &DefinitionOp{
		ops:       ops,
		defs:      defs,
		metas:     metas,
		sources:   srcs,
		platforms: plats,
		dgst:      dgst,
		index:     idx,
	}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (d *DefinitionOp) Type() core.VertexType { return core.VertexTypeSource }
func (d *DefinitionOp) Inputs() []core.Edge   { return nil } // Inputs resolved via Output chain
func (d *DefinitionOp) Outputs() []core.OutputSlot {
	if d.dgst == "" {
		return nil
	}
	return []core.OutputSlot{{Index: int(d.index), Description: "definition output"}}
}

func (d *DefinitionOp) Validate(_ context.Context, _ *core.Constraints) error {
	if d.dgst == "" {
		return nil // scratch
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.ops) == 0 || len(d.defs) == 0 {
		return fmt.Errorf("definition: invalid op with no ops (%d defs)", len(d.defs))
	}
	if _, ok := d.ops[d.dgst]; !ok {
		return fmt.Errorf("definition: unknown op digest %q", d.dgst)
	}
	if _, ok := d.defs[d.dgst]; !ok {
		return fmt.Errorf("definition: unknown def digest %q", d.dgst)
	}
	if d.index < 0 {
		return fmt.Errorf("definition: invalid index %d", d.index)
	}
	return nil
}

func (d *DefinitionOp) Marshal(_ context.Context, _ *core.Constraints) (*core.MarshaledVertex, error) {
	if d.dgst == "" {
		return nil, fmt.Errorf("definition: cannot marshal empty definition")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	dt, ok := d.defs[d.dgst]
	if !ok {
		return nil, fmt.Errorf("definition: missing bytes for %q", d.dgst)
	}

	var md *pb.OpMetadata
	if m, ok := d.metas[d.dgst]; ok {
		md = m
	} else {
		md = &pb.OpMetadata{}
	}

	return &core.MarshaledVertex{
		Digest:   core.VertexID(d.dgst),
		Bytes:    dt,
		Metadata: md,
	}, nil
}

func (d *DefinitionOp) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	return d, nil // Definitions are self-contained
}

// ─── core.Output ─────────────────────────────────────────────────────────────

// Output returns an Output suitable for composing into the graph.
func (d *DefinitionOp) Output() core.Output {
	if d.dgst == "" {
		return nil
	}
	return &definitionOutput{d: d}
}

type definitionOutput struct {
	d *DefinitionOp
}

func (o *definitionOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex {
	return o.d
}

func (o *definitionOutput) ToInput(_ context.Context, _ *core.Constraints) (*pb.Input, error) {
	return &pb.Input{
		Digest: string(o.d.dgst),
		Index:  int64(o.d.index),
	}, nil
}

// ─── Sub-graph reconstruction ────────────────────────────────────────────────

// SubVertex returns the DefinitionOp for a specific digest within the definition,
// enabling full graph traversal. This uses a shared cache for deduplication.
func (d *DefinitionOp) SubVertex(dgst digest.Digest, idx pb.OutputIndex) *DefinitionOp {
	key := dgst.String() + fmt.Sprintf(":%d", idx)
	if v, ok := d.inputCache.Load(key); ok {
		return v.(*DefinitionOp)
	}

	sub := &DefinitionOp{
		ops:        d.ops,
		defs:       d.defs,
		metas:      d.metas,
		platforms:  d.platforms,
		sources:    d.sources,
		dgst:       dgst,
		index:      idx,
		inputCache: sync.Map{},
	}
	// Copy parent cache reference
	d.inputCache.Range(func(k, v any) bool {
		sub.inputCache.Store(k, v)
		return true
	})

	d.inputCache.Store(key, sub)
	return sub
}

// AllOps returns all parsed ops in the definition (for inspection/debugging).
func (d *DefinitionOp) AllOps() map[digest.Digest]*pb.Op {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[digest.Digest]*pb.Op, len(d.ops))
	for k, v := range d.ops {
		out[k] = v
	}
	return out
}

// Digest returns the head vertex digest.
func (d *DefinitionOp) Digest() digest.Digest { return d.dgst }

// Index returns the output index.
func (d *DefinitionOp) Index() pb.OutputIndex { return d.index }

var (
	_ core.Vertex = (*DefinitionOp)(nil)
	_ core.Output = (*definitionOutput)(nil)
)

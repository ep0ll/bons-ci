// Package definitionop implements the DefinitionOp vertex for reconstructing
// an LLB graph from a marshalled protobuf Definition.
package definitionop

import (
	"context"
	"sync"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"
)

// ─────────────────────────────────────────────────────────────────────────────
// DefinitionOp
// ─────────────────────────────────────────────────────────────────────────────

// DefinitionOp implements llb.Vertex using a pre-marshalled definition. This
// allows reconstructing an LLB state graph from a wire-format definition
// received from another process or session.
type DefinitionOp struct {
	mu         sync.Mutex
	ops        map[digest.Digest]*pb.Op
	defs       map[digest.Digest][]byte
	metas      map[digest.Digest]*pb.OpMetadata
	sources    map[digest.Digest][]*llb.SourceLocation
	platforms  map[digest.Digest]*ocispecs.Platform
	inputCache sync.Map // digest.Digest → []*DefinitionOp
	dgst       digest.Digest
	index      pb.OutputIndex
}

var _ llb.Vertex = (*DefinitionOp)(nil)

// NewDefinitionOp reconstructs a DefinitionOp from a marshalled pb.Definition.
func NewDefinitionOp(def *pb.Definition) (*DefinitionOp, error) {
	if def.IsNil() {
		return nil, errors.New("invalid nil input definition")
	}

	ops := make(map[digest.Digest]*pb.Op)
	defs := make(map[digest.Digest][]byte)
	platforms := make(map[digest.Digest]*ocispecs.Platform)

	var dgst digest.Digest
	for _, dt := range def.Def {
		var op pb.Op
		if err := proto.Unmarshal(dt, &op); err != nil {
			return nil, errors.Wrap(err, "failed to parse llb proto op")
		}
		dgst = digest.FromBytes(dt)
		ops[dgst] = &op
		defs[dgst] = dt

		var platform *ocispecs.Platform
		if op.Platform != nil {
			spec := op.Platform.Spec()
			platform = &spec
		}
		platforms[dgst] = platform
	}

	srcs := make(map[digest.Digest][]*llb.SourceLocation)
	if def.Source != nil {
		sourceMaps := make([]*llb.SourceMap, len(def.Source.Infos))
		for i, info := range def.Source.Infos {
			var st *llb.State
			sdef := info.Definition
			if sdef != nil {
				op, err := NewDefinitionOp(sdef)
				if err != nil {
					return nil, err
				}
				state := llb.NewState(op)
				st = &state
			}
			sourceMaps[i] = llb.NewSourceMap(st, info.Filename, info.Language, info.Data)
		}

		for dgstStr, locs := range def.Source.Locations {
			for _, loc := range locs.Locations {
				if loc.SourceIndex < 0 || int(loc.SourceIndex) >= len(sourceMaps) {
					return nil, errors.Errorf("invalid source map index %d", loc.SourceIndex)
				}
				srcs[digest.Digest(dgstStr)] = append(srcs[digest.Digest(dgstStr)], &llb.SourceLocation{
					SourceMap: sourceMaps[int(loc.SourceIndex)],
					Ranges:    loc.Ranges,
				})
			}
		}
	}

	var index pb.OutputIndex
	if dgst != "" {
		index = pb.OutputIndex(ops[dgst].Inputs[0].Index)
		dgst = digest.Digest(ops[dgst].Inputs[0].Digest)
	}

	metas := make(map[digest.Digest]*pb.OpMetadata, len(def.Metadata))
	for k, v := range def.Metadata {
		metas[digest.Digest(k)] = v
	}

	return &DefinitionOp{
		ops:       ops,
		defs:      defs,
		metas:     metas,
		sources:   srcs,
		platforms: platforms,
		dgst:      dgst,
		index:     index,
	}, nil
}

// ToInput converts to a pb.Input.
func (d *DefinitionOp) ToInput(ctx context.Context, c *llb.Constraints) (*pb.Input, error) {
	return d.Output().ToInput(ctx, c)
}

// Vertex returns itself.
func (d *DefinitionOp) Vertex(context.Context, *llb.Constraints) llb.Vertex { return d }

// Validate checks for a valid internal state.
func (d *DefinitionOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if d.dgst == "" {
		return nil // scratch
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.ops) == 0 || len(d.defs) == 0 || len(d.metas) == 0 {
		return errors.Errorf("invalid definition op: ops=%d defs=%d metas=%d", len(d.ops), len(d.defs), len(d.metas))
	}

	if _, ok := d.ops[d.dgst]; !ok {
		return errors.Errorf("definition op references unknown op %q", d.dgst)
	}
	if _, ok := d.defs[d.dgst]; !ok {
		return errors.Errorf("definition op references unknown def %q", d.dgst)
	}
	if _, ok := d.metas[d.dgst]; !ok {
		return errors.Errorf("definition op references unknown metadata %q", d.dgst)
	}
	if d.index < 0 {
		return errors.New("definition op has invalid output index")
	}

	return nil
}

// Marshal returns the pre-serialized bytes for this op's digest.
func (d *DefinitionOp) Marshal(ctx context.Context, c *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	if d.dgst == "" {
		return "", nil, nil, nil, errors.New("cannot marshal empty definition op")
	}
	if err := d.Validate(ctx, c); err != nil {
		return "", nil, nil, nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	return d.dgst, d.defs[d.dgst], d.metas[d.dgst], d.sources[d.dgst], nil
}

// Output returns the output for the current digest/index pair.
func (d *DefinitionOp) Output() llb.Output {
	if d.dgst == "" {
		return nil
	}

	d.mu.Lock()
	platform := d.platforms[d.dgst]
	d.mu.Unlock()

	return llb.NewOutputWithIndex(d, platform, func() (pb.OutputIndex, error) {
		return d.index, nil
	})
}

// Inputs reconstructs the inputs by looking up each input digest/index in the
// shared definition maps. Results are cached for identity dedup.
func (d *DefinitionOp) Inputs() []llb.Output {
	if d.dgst == "" {
		return nil
	}

	d.mu.Lock()
	op := d.ops[d.dgst]
	platform := d.platforms[d.dgst]
	d.mu.Unlock()

	var inputs []llb.Output
	for _, input := range op.Inputs {
		vtx := d.loadOrCreateInput(input, platform)
		inputs = append(inputs, vtx.Output())
	}
	return inputs
}

// loadOrCreateInput returns a cached or new DefinitionOp for the given input.
func (d *DefinitionOp) loadOrCreateInput(input *pb.Input, platform *ocispecs.Platform) *DefinitionOp {
	dgst := digest.Digest(input.Digest)
	key := dgst.String()

	if cached, ok := d.inputCache.Load(key); ok {
		arr := cached.([]*DefinitionOp)
		if int(input.Index) < len(arr) && arr[input.Index] != nil {
			return arr[input.Index]
		}
	}

	vtx := &DefinitionOp{
		ops:       d.ops,
		defs:      d.defs,
		metas:     d.metas,
		platforms: d.platforms,
		sources:   d.sources,
		dgst:      dgst,
		index:     pb.OutputIndex(input.Index),
	}

	// Grow the cached slice if needed.
	var arr []*DefinitionOp
	if existing, ok := d.inputCache.Load(key); ok {
		arr = existing.([]*DefinitionOp)
	}
	diff := int(input.Index) - len(arr)
	if diff >= 0 {
		arr = append(arr, make([]*DefinitionOp, diff+1)...)
	}
	arr[input.Index] = vtx
	d.inputCache.Store(key, arr)

	return vtx
}

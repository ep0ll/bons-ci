package marshal

import (
	"context"
	"fmt"
	"slices"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/apicaps"
	digest "github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/proto"
)

// ─── Definition ───────────────────────────────────────────────────────────────

// Definition is the serialised form of a build graph, ready for transmission
// to a BuildKit daemon.
type Definition struct {
	// Def holds the serialised pb.Op bytes for each vertex, in topological order.
	Def [][]byte
	// Metadata maps each vertex digest to its per-vertex metadata.
	Metadata map[digest.Digest]core.OpMetadata
	// Source carries source-map information for debugging.
	Source *pb.Source
}

// ToPB converts to the protobuf wire type.
func (d *Definition) ToPB() *pb.Definition {
	metas := make(map[string]*pb.OpMetadata, len(d.Metadata))
	for dgst, m := range d.Metadata {
		metas[string(dgst)] = m.ToPB()
	}
	return &pb.Definition{
		Def:      d.Def,
		Source:   d.Source,
		Metadata: metas,
	}
}

// ─── Serializer ───────────────────────────────────────────────────────────────

// Serializer marshals an llbx Vertex graph into a Definition. It maintains a
// vertex cache to avoid re-serialising shared sub-graphs.
type Serializer struct {
	digestCache map[core.Vertex]digest.Digest
	bytesCache  map[digest.Digest][]byte
}

// NewSerializer returns a Serializer ready to use.
func NewSerializer() *Serializer {
	return &Serializer{
		digestCache: make(map[core.Vertex]digest.Digest),
		bytesCache:  make(map[digest.Digest][]byte),
	}
}

// Serialize converts the graph rooted at root into a Definition.
func (s *Serializer) Serialize(
	ctx context.Context,
	root core.Vertex,
	c *core.Constraints,
) (*Definition, error) {
	def := &Definition{
		Metadata: make(map[digest.Digest]core.OpMetadata),
	}
	smc := newSourceMapCollector()
	seenDigests := make(map[digest.Digest]struct{})
	seenVertices := make(map[core.Vertex]struct{})

	if err := s.walk(ctx, root, def, smc, seenDigests, seenVertices, c); err != nil {
		return nil, err
	}

	// Append the terminal pointer op.
	rootMV, err := root.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("serialize: marshal root: %w", err)
	}
	pointer := &pb.Op{Inputs: []*pb.Input{{
		Digest: string(rootMV.Digest),
		Index:  0,
	}}}
	pointerBytes, err := pointer.MarshalVT()
	if err != nil {
		return nil, fmt.Errorf("serialize: marshal pointer op: %w", err)
	}
	def.Def = append(def.Def, pointerBytes)

	// Build terminal metadata with capabilities summary.
	pointerDigest := digest.FromBytes(pointerBytes)
	termMD := core.OpMetadata{
		Caps: map[apicaps.CapID]bool{
			pb.CapConstraints: true,
			pb.CapPlatform:    true,
		},
	}
	for _, m := range def.Metadata {
		if m.IgnoreCache {
			termMD.Caps[pb.CapMetaIgnoreCache] = true
		}
		if m.Description != nil {
			termMD.Caps[pb.CapMetaDescription] = true
		}
		if m.ExportCache != nil {
			termMD.Caps[pb.CapMetaExportCache] = true
		}
	}
	def.Metadata[pointerDigest] = termMD

	src, err := smc.marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("serialize: source maps: %w", err)
	}
	def.Source = src

	return def, nil
}

func (s *Serializer) walk(
	ctx context.Context,
	v core.Vertex,
	def *Definition,
	smc *sourceMapCollector,
	seenDigests map[digest.Digest]struct{},
	seenVertices map[core.Vertex]struct{},
	c *core.Constraints,
) error {
	if _, ok := seenVertices[v]; ok {
		return nil
	}
	// Recurse into inputs first (topological order).
	for _, edge := range v.Inputs() {
		if err := s.walk(ctx, edge.Vertex, def, smc, seenDigests, seenVertices, c); err != nil {
			return err
		}
	}

	mv, err := v.Marshal(ctx, c)
	if err != nil {
		return fmt.Errorf("serialize walk: %w", err)
	}
	seenVertices[v] = struct{}{}

	if mv.Metadata != nil {
		existing := def.Metadata[mv.Digest]
		def.Metadata[mv.Digest] = existing.MergeWith(core.OpMetadataFromPB(mv.Metadata))
	}
	smc.add(mv.Digest, mv.SourceLocations)

	if _, ok := seenDigests[mv.Digest]; ok {
		return nil
	}
	def.Def = append(def.Def, mv.Bytes)
	seenDigests[mv.Digest] = struct{}{}
	return nil
}

// ─── MarshalConstraints ───────────────────────────────────────────────────────

// MarshalConstraints merges base and override constraints into a pb.Op shell
// and OpMetadata, mirroring BuildKit's own helper of the same name.
func MarshalConstraints(base, override *core.Constraints) (*pb.Op, *pb.OpMetadata) {
	c := base.Merge(override)

	if c.Platform == nil {
		p := platforms.Normalize(platforms.DefaultSpec())
		c.Platform = &p
	}

	opPlatform := &pb.Platform{
		OS:           c.Platform.OS,
		Architecture: c.Platform.Architecture,
		Variant:      c.Platform.Variant,
		OSVersion:    c.Platform.OSVersion,
		OSFeatures:   slices.Clone(c.Platform.OSFeatures),
	}

	return &pb.Op{
		Platform: opPlatform,
		Constraints: &pb.WorkerConstraints{
			Filter: c.WorkerConstraints,
		},
	}, c.Metadata.ToPB()
}

// DeterministicMarshal serialises a proto.Message with deterministic field
// ordering, ensuring stable digests across multiple runs.
func DeterministicMarshal[M proto.Message](m M) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

// ─── sourceMapCollector ───────────────────────────────────────────────────────

type sourceMapCollector struct {
	maps      []*core.SourceMap
	index     map[*core.SourceMap]int
	locations map[digest.Digest][]*core.SourceLocation
}

func newSourceMapCollector() *sourceMapCollector {
	return &sourceMapCollector{
		index:     make(map[*core.SourceMap]int),
		locations: make(map[digest.Digest][]*core.SourceLocation),
	}
}

func (c *sourceMapCollector) add(dgst digest.Digest, locs []*core.SourceLocation) {
	for _, l := range locs {
		if _, ok := c.index[l.SourceMap]; !ok {
			idx := len(c.maps)
			c.maps = append(c.maps, l.SourceMap)
			c.index[l.SourceMap] = idx
		}
	}
	c.locations[dgst] = append(c.locations[dgst], locs...)
}

func (c *sourceMapCollector) marshal(_ context.Context, _ *core.Constraints) (*pb.Source, error) {
	src := &pb.Source{
		Locations: make(map[string]*pb.Locations),
	}
	for _, sm := range c.maps {
		src.Infos = append(src.Infos, &pb.SourceInfo{
			Filename: sm.Filename,
			Language: sm.Language,
			Data:     sm.Data,
		})
	}
	for dgst, locs := range c.locations {
		pbLocs := &pb.Locations{}
		for _, loc := range locs {
			pbLocs.Locations = append(pbLocs.Locations, &pb.Location{
				SourceIndex: int32(c.index[loc.SourceMap]),
				Ranges:      loc.Ranges,
			})
		}
		src.Locations[dgst.String()] = pbLocs
	}
	return src, nil
}

// Package marshal handles conversion of the vertex graph into BuildKit's
// wire format (pb.Definition) with per-Constraints caching.
package marshal

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/apicaps"
	digest "github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/proto"
)

// ─── Cache ────────────────────────────────────────────────────────────────────

// Cache is a thread-safe, per-Constraints result store for marshaled vertices.
type Cache struct {
	mu      sync.Mutex
	entries map[*core.Constraints]*cacheEntry
}

type cacheEntry struct {
	digest  digest.Digest
	bytes   []byte
	meta    *pb.OpMetadata
	sources []*core.SourceLocation
}

// Acquire locks the cache and returns a CacheHandle for use within one
// Marshal call.
func (c *Cache) Acquire() *CacheHandle {
	c.mu.Lock()
	return &CacheHandle{c: c}
}

// Invalidate discards all cached results.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// CacheHandle provides scoped access to a locked Cache.
type CacheHandle struct{ c *Cache }

// Release unlocks the cache.
func (h *CacheHandle) Release() { h.c.mu.Unlock() }

// Load retrieves a cached result. Returns cerrdefs.ErrNotFound if absent.
func (h *CacheHandle) Load(constraints *core.Constraints) (
	digest.Digest, []byte, *pb.OpMetadata, []*core.SourceLocation, error,
) {
	if h.c.entries == nil {
		return "", nil, nil, nil, cerrdefs.ErrNotFound
	}
	e, ok := h.c.entries[constraints]
	if !ok {
		return "", nil, nil, nil, cerrdefs.ErrNotFound
	}
	return e.digest, e.bytes, e.meta, e.sources, nil
}

// Store persists a result and returns the same values for chaining.
func (h *CacheHandle) Store(
	bytes []byte,
	meta *pb.OpMetadata,
	sources []*core.SourceLocation,
	constraints *core.Constraints,
) (digest.Digest, []byte, *pb.OpMetadata, []*core.SourceLocation, error) {
	dgst := digest.FromBytes(bytes)
	if h.c.entries == nil {
		h.c.entries = make(map[*core.Constraints]*cacheEntry)
	}
	h.c.entries[constraints] = &cacheEntry{
		digest:  dgst,
		bytes:   bytes,
		meta:    meta,
		sources: sources,
	}
	return dgst, bytes, meta, sources, nil
}

// ─── Definition ───────────────────────────────────────────────────────────────

// Definition is the serialised form of a build graph ready for transmission.
type Definition struct {
	Def      [][]byte
	Metadata map[digest.Digest]core.OpMetadata
	Source   *pb.Source
}

// ToPB converts to the protobuf Definition.
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

// Serializer converts a graph rooted at a core.Output into a Definition.
type Serializer struct{}

// NewSerializer returns a ready-to-use Serializer.
func NewSerializer() *Serializer { return &Serializer{} }

// Serialize converts the graph rooted at root into a wire-format Definition.
func (s *Serializer) Serialize(
	ctx context.Context,
	root core.Output,
	c *core.Constraints,
) (*Definition, error) {
	if root == nil {
		return &Definition{Metadata: make(map[digest.Digest]core.OpMetadata)}, nil
	}
	rootVtx := root.Vertex(ctx, c)
	if rootVtx == nil {
		return &Definition{Metadata: make(map[digest.Digest]core.OpMetadata)}, nil
	}

	def := &Definition{Metadata: make(map[digest.Digest]core.OpMetadata)}
	smc := &sourceMapCollector{
		index:     make(map[*core.SourceMap]int),
		locations: make(map[digest.Digest][]*core.SourceLocation),
	}
	seenDigests := make(map[digest.Digest]struct{})
	seenVertices := make(map[core.Vertex]struct{})

	if err := walk(ctx, rootVtx, def, smc, seenDigests, seenVertices, c); err != nil {
		return nil, err
	}

	// Append terminal pointer op.
	rootMV, err := rootVtx.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("serialize root: %w", err)
	}
	pointer := &pb.Op{Inputs: []*pb.Input{{
		Digest: string(rootMV.Digest),
		Index:  0,
	}}}
	pointerBytes, err := pointer.MarshalVT()
	if err != nil {
		return nil, fmt.Errorf("serialize pointer op: %w", err)
	}
	def.Def = append(def.Def, pointerBytes)

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

	src, err := smc.marshal()
	if err != nil {
		return nil, fmt.Errorf("serialize source maps: %w", err)
	}
	def.Source = src
	return def, nil
}

func walk(
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
	for _, edge := range v.Inputs() {
		if err := walk(ctx, edge.Vertex, def, smc, seenDigests, seenVertices, c); err != nil {
			return err
		}
	}
	mv, err := v.Marshal(ctx, c)
	if err != nil {
		return fmt.Errorf("walk marshal: %w", err)
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
// and pb.OpMetadata. Mirrors BuildKit's own helper of the same name.
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
		Platform:    opPlatform,
		Constraints: &pb.WorkerConstraints{Filter: c.WorkerConstraints},
	}, c.Metadata.ToPB()
}

// DeterministicMarshal serialises a proto.Message with stable field ordering.
func DeterministicMarshal[M proto.Message](m M) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

// ─── sourceMapCollector ───────────────────────────────────────────────────────

type sourceMapCollector struct {
	maps      []*core.SourceMap
	index     map[*core.SourceMap]int
	locations map[digest.Digest][]*core.SourceLocation
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

func (c *sourceMapCollector) marshal() (*pb.Source, error) {
	src := &pb.Source{Locations: make(map[string]*pb.Locations)}
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

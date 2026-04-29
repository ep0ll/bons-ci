package llb

import (
	"bytes"
	"context"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// SourceMap
// ─────────────────────────────────────────────────────────────────────────────

// SourceMap maps a source file/location to an LLB state or definition.
// Source maps carry debugging context so that build errors can be traced back
// to the originating source line (e.g. a Dockerfile instruction).
type SourceMap struct {
	State      *State
	Definition *Definition
	Filename   string
	Language   string
	Data       []byte
}

// NewSourceMap creates a SourceMap referencing a source file with optional
// backing state.
func NewSourceMap(st *State, filename, lang string, data []byte) *SourceMap {
	return &SourceMap{
		State:    st,
		Filename: filename,
		Language: lang,
		Data:     data,
	}
}

// Location returns a ConstraintsOpt that attaches this source map at the
// given ranges to a vertex's constraints.
func (sm *SourceMap) Location(ranges []*pb.Range) ConstraintsOpt {
	return constraintsOptFunc(func(c *Constraints) {
		if sm == nil {
			return
		}
		c.SourceLocations = append(c.SourceLocations, &SourceLocation{
			SourceMap: sm,
			Ranges:    ranges,
		})
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// SourceLocation
// ─────────────────────────────────────────────────────────────────────────────

// SourceLocation pairs a SourceMap with specific line/column ranges.
type SourceLocation struct {
	SourceMap *SourceMap
	Ranges    []*pb.Range
}

// ─────────────────────────────────────────────────────────────────────────────
// sourceMapCollector
// ─────────────────────────────────────────────────────────────────────────────

// sourceMapCollector deduplicates and marshals source map data across a
// complete LLB definition. Each unique SourceMap is stored once; locations
// reference the deduplicated index.
type sourceMapCollector struct {
	maps      []*SourceMap
	index     map[*SourceMap]int
	locations map[digest.Digest][]*SourceLocation
}

// newSourceMapCollector creates an empty collector.
func newSourceMapCollector() *sourceMapCollector {
	return &sourceMapCollector{
		index:     make(map[*SourceMap]int),
		locations: make(map[digest.Digest][]*SourceLocation),
	}
}

// Add registers source locations for a given vertex digest.
func (smc *sourceMapCollector) Add(dgst digest.Digest, locs []*SourceLocation) {
	for _, loc := range locs {
		idx, ok := smc.index[loc.SourceMap]
		if !ok {
			idx = -1
			// Slow equality check for structural dedup.
			for i, m := range smc.maps {
				if equalSourceMap(m, loc.SourceMap) {
					idx = i
					break
				}
			}
			if idx == -1 {
				idx = len(smc.maps)
				smc.maps = append(smc.maps, loc.SourceMap)
			}
		}
		smc.index[loc.SourceMap] = idx
	}
	smc.locations[dgst] = append(smc.locations[dgst], locs...)
}

// Marshal serializes all collected source maps into a pb.Source for wire
// transmission.
func (smc *sourceMapCollector) Marshal(ctx context.Context, co ...ConstraintsOpt) (*pb.Source, error) {
	s := &pb.Source{
		Locations: make(map[string]*pb.Locations),
	}
	for _, m := range smc.maps {
		def := m.Definition
		if def == nil && m.State != nil {
			var err error
			def, err = m.State.Marshal(ctx, co...)
			if err != nil {
				return nil, err
			}
			m.Definition = def
		}

		info := &pb.SourceInfo{
			Data:     m.Data,
			Filename: m.Filename,
			Language: m.Language,
		}
		if def != nil {
			info.Definition = def.ToPB()
		}
		s.Infos = append(s.Infos, info)
	}

	for dgst, locs := range smc.locations {
		pbLocs, ok := s.Locations[dgst.String()]
		if !ok {
			pbLocs = &pb.Locations{}
		}
		for _, loc := range locs {
			pbLocs.Locations = append(pbLocs.Locations, &pb.Location{
				SourceIndex: int32(smc.index[loc.SourceMap]),
				Ranges:      loc.Ranges,
			})
		}
		s.Locations[dgst.String()] = pbLocs
	}

	return s, nil
}

// equalSourceMap performs structural equality on two SourceMap values.
func equalSourceMap(a, b *SourceMap) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Filename != b.Filename || a.Language != b.Language {
		return false
	}
	if !bytes.Equal(a.Data, b.Data) {
		return false
	}
	if a.Definition != nil && b.Definition != nil {
		if len(a.Definition.Def) != len(b.Definition.Def) {
			return false
		}
		if len(a.Definition.Def) > 0 {
			lastA := a.Definition.Def[len(a.Definition.Def)-1]
			lastB := b.Definition.Def[len(b.Definition.Def)-1]
			if !bytes.Equal(lastA, lastB) {
				return false
			}
		}
	}
	return true
}

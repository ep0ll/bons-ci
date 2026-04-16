package core

import (
	"maps"
	"slices"

	"github.com/moby/buildkit/util/apicaps"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/moby/buildkit/solver/pb"
	"github.com/containerd/platforms"
)

// ─── Constraints ─────────────────────────────────────────────────────────────

// Constraints carry build-time context that flows through the graph but is not
// part of any individual vertex's identity. They influence marshalling (e.g.,
// which platform a source op targets) but are provided externally at marshal
// time rather than baked into the vertex.
type Constraints struct {
	// Platform specifies the target OS/arch. Defaults to the host platform.
	Platform *v1.Platform

	// WorkerConstraints are label-selector filters for choosing a BuildKit
	// worker (e.g., "label:gpu=true").
	WorkerConstraints []string

	// Metadata carries per-vertex LLB metadata (cache hints, descriptions).
	Metadata OpMetadata

	// LocalUniqueID is a per-build identifier for local directory sources,
	// preventing cross-build cache collisions.
	LocalUniqueID string

	// Caps is the capability set advertised by the connected BuildKit daemon.
	// Nil means "no capability negotiation"; the marshaler applies safe defaults.
	Caps *apicaps.CapSet

	// SourceLocations are source file positions to attach to this vertex.
	SourceLocations []*SourceLocation
}

// DefaultConstraints returns constraints initialised with the host platform.
func DefaultConstraints() *Constraints {
	p := platforms.Normalize(platforms.DefaultSpec())
	return &Constraints{
		Platform: &p,
	}
}

// Clone returns a deep copy that shares no mutable state with the receiver.
func (c *Constraints) Clone() *Constraints {
	if c == nil {
		return DefaultConstraints()
	}
	cloned := *c
	cloned.WorkerConstraints = slices.Clone(c.WorkerConstraints)
	cloned.Metadata = c.Metadata.Clone()
	if c.Platform != nil {
		p := *c.Platform
		if c.Platform.OSFeatures != nil {
			p.OSFeatures = slices.Clone(c.Platform.OSFeatures)
		}
		cloned.Platform = &p
	}
	cloned.SourceLocations = slices.Clone(c.SourceLocations)
	return &cloned
}

// Merge applies overrides on top of the receiver, returning a new Constraints.
// Fields in override that are non-zero/non-nil replace those in the receiver.
func (c *Constraints) Merge(override *Constraints) *Constraints {
	if override == nil {
		return c.Clone()
	}
	merged := c.Clone()
	if override.Platform != nil {
		p := *override.Platform
		merged.Platform = &p
	}
	merged.WorkerConstraints = append(merged.WorkerConstraints, override.WorkerConstraints...)
	merged.Metadata = merged.Metadata.MergeWith(override.Metadata)
	if override.LocalUniqueID != "" {
		merged.LocalUniqueID = override.LocalUniqueID
	}
	if override.Caps != nil {
		merged.Caps = override.Caps
	}
	merged.SourceLocations = append(merged.SourceLocations, override.SourceLocations...)
	return merged
}

// AddCap registers a capability requirement in the metadata.
func (c *Constraints) AddCap(id apicaps.CapID) {
	if c.Metadata.Caps == nil {
		c.Metadata.Caps = make(map[apicaps.CapID]bool)
	}
	c.Metadata.Caps[id] = true
}

// ─── OpMetadata ───────────────────────────────────────────────────────────────

// OpMetadata is the human-friendly counterpart of pb.OpMetadata.
type OpMetadata struct {
	IgnoreCache   bool
	Description   map[string]string
	ExportCache   *pb.ExportCache
	Caps          map[apicaps.CapID]bool
	ProgressGroup *pb.ProgressGroup
}

// Clone returns a deep copy.
func (m OpMetadata) Clone() OpMetadata {
	cloned := m
	if m.Description != nil {
		cloned.Description = maps.Clone(m.Description)
	}
	if m.Caps != nil {
		cloned.Caps = maps.Clone(m.Caps)
	}
	return cloned
}

// MergeWith applies m2 on top of m, returning a new merged OpMetadata.
func (m OpMetadata) MergeWith(m2 OpMetadata) OpMetadata {
	merged := m.Clone()
	if m2.IgnoreCache {
		merged.IgnoreCache = true
	}
	if len(m2.Description) > 0 {
		if merged.Description == nil {
			merged.Description = make(map[string]string)
		}
		maps.Copy(merged.Description, m2.Description)
	}
	if m2.ExportCache != nil {
		merged.ExportCache = m2.ExportCache
	}
	for k := range m2.Caps {
		if merged.Caps == nil {
			merged.Caps = make(map[apicaps.CapID]bool)
		}
		merged.Caps[k] = true
	}
	if m2.ProgressGroup != nil {
		merged.ProgressGroup = m2.ProgressGroup
	}
	return merged
}

// ToPB converts the OpMetadata to its protobuf representation.
func (m OpMetadata) ToPB() *pb.OpMetadata {
	caps := make(map[string]bool, len(m.Caps))
	for k, v := range m.Caps {
		caps[string(k)] = v
	}
	return &pb.OpMetadata{
		IgnoreCache:   m.IgnoreCache,
		Description:   m.Description,
		ExportCache:   m.ExportCache,
		Caps:          caps,
		ProgressGroup: m.ProgressGroup,
	}
}

// FromPB populates an OpMetadata from its protobuf representation.
func OpMetadataFromPB(mpb *pb.OpMetadata) OpMetadata {
	if mpb == nil {
		return OpMetadata{}
	}
	m := OpMetadata{
		IgnoreCache:   mpb.IgnoreCache,
		Description:   mpb.Description,
		ExportCache:   mpb.ExportCache,
		ProgressGroup: mpb.ProgressGroup,
	}
	if len(mpb.Caps) > 0 {
		m.Caps = make(map[apicaps.CapID]bool, len(mpb.Caps))
		for k, v := range mpb.Caps {
			m.Caps[apicaps.CapID(k)] = v
		}
	}
	return m
}

// ─── ConstraintsOption ────────────────────────────────────────────────────────

// ConstraintsOption is a functional option that modifies a Constraints value.
type ConstraintsOption func(*Constraints)

// Apply applies all options to c in order.
func ApplyConstraintsOptions(c *Constraints, opts ...ConstraintsOption) {
	for _, o := range opts {
		o(c)
	}
}

// WithPlatform sets the target platform.
func WithPlatform(p v1.Platform) ConstraintsOption {
	return func(c *Constraints) { c.Platform = &p }
}

// WithWorkerConstraint appends a worker label filter.
func WithWorkerConstraint(filter string) ConstraintsOption {
	return func(c *Constraints) {
		c.WorkerConstraints = append(c.WorkerConstraints, filter)
	}
}

// WithIgnoreCache sets the ignore-cache flag on the metadata.
func WithIgnoreCache() ConstraintsOption {
	return func(c *Constraints) { c.Metadata.IgnoreCache = true }
}

// WithDescription adds key/value pairs to the metadata description map.
func WithDescription(key, value string) ConstraintsOption {
	return func(c *Constraints) {
		if c.Metadata.Description == nil {
			c.Metadata.Description = map[string]string{}
		}
		c.Metadata.Description[key] = value
	}
}

// WithCustomName sets the "llb.customname" description field.
func WithCustomName(name string) ConstraintsOption {
	return WithDescription("llb.customname", name)
}

// WithCaps sets the capability set.
func WithCaps(caps apicaps.CapSet) ConstraintsOption {
	return func(c *Constraints) { c.Caps = &caps }
}

// WithExportCache forces cache export for this vertex.
func WithExportCache() ConstraintsOption {
	return func(c *Constraints) {
		c.Metadata.ExportCache = &pb.ExportCache{Value: true}
	}
}

// WithSourceLocation attaches a source file range to the vertex.
func WithSourceLocation(sm *SourceMap, ranges ...*pb.Range) ConstraintsOption {
	return func(c *Constraints) {
		c.SourceLocations = append(c.SourceLocations, &SourceLocation{
			SourceMap: sm,
			Ranges:    ranges,
		})
	}
}

package core

import (
	"maps"
	"slices"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/apicaps"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Constraints ─────────────────────────────────────────────────────────────

// Constraints carry build-time context that flows through the graph but is not
// baked into any vertex's content address. They are supplied externally at
// marshal time.
type Constraints struct {
	// Platform is the target OS/arch. Defaults to host platform.
	Platform *ocispecs.Platform

	// WorkerConstraints are label-selector filters for choosing a BuildKit worker.
	WorkerConstraints []string

	// Metadata carries per-vertex LLB metadata (cache hints, descriptions, caps).
	Metadata OpMetadata

	// LocalUniqueID prevents cross-build local-source cache collisions.
	LocalUniqueID string

	// Caps is the capability set advertised by the connected BuildKit daemon.
	Caps *apicaps.CapSet

	// SourceLocations are source file positions to attach to this vertex.
	SourceLocations []*SourceLocation

	// BuildArgs are arbitrary key/value pairs available to conditional and
	// selector vertices at definition time.
	BuildArgs map[string]string
}

// DefaultConstraints returns a Constraints initialised with the host platform
// and a freshly generated LocalUniqueID.
func DefaultConstraints() *Constraints {
	p := platforms.Normalize(platforms.DefaultSpec())
	return &Constraints{
		Platform:  &p,
		BuildArgs: make(map[string]string),
	}
}

// Clone returns a deep copy that shares no mutable state with the receiver.
func (c *Constraints) Clone() *Constraints {
	if c == nil {
		return DefaultConstraints()
	}
	out := *c
	out.WorkerConstraints = slices.Clone(c.WorkerConstraints)
	out.Metadata = c.Metadata.Clone()
	out.SourceLocations = slices.Clone(c.SourceLocations)
	out.BuildArgs = maps.Clone(c.BuildArgs)
	if c.Platform != nil {
		p := *c.Platform
		if c.Platform.OSFeatures != nil {
			p.OSFeatures = slices.Clone(c.Platform.OSFeatures)
		}
		out.Platform = &p
	}
	return &out
}

// Merge overlays override on top of the receiver, returning a new Constraints.
func (c *Constraints) Merge(override *Constraints) *Constraints {
	if override == nil {
		return c.Clone()
	}
	m := c.Clone()
	if override.Platform != nil {
		p := *override.Platform
		m.Platform = &p
	}
	m.WorkerConstraints = append(m.WorkerConstraints, override.WorkerConstraints...)
	m.Metadata = m.Metadata.MergeWith(override.Metadata)
	if override.LocalUniqueID != "" {
		m.LocalUniqueID = override.LocalUniqueID
	}
	if override.Caps != nil {
		m.Caps = override.Caps
	}
	m.SourceLocations = append(m.SourceLocations, override.SourceLocations...)
	for k, v := range override.BuildArgs {
		m.BuildArgs[k] = v
	}
	return m
}

// AddCap registers a capability requirement.
func (c *Constraints) AddCap(id apicaps.CapID) {
	if c.Metadata.Caps == nil {
		c.Metadata.Caps = make(map[apicaps.CapID]bool)
	}
	c.Metadata.Caps[id] = true
}

// BuildArg returns the value of a build argument, or ("", false) if absent.
func (c *Constraints) BuildArg(key string) (string, bool) {
	if c.BuildArgs == nil {
		return "", false
	}
	v, ok := c.BuildArgs[key]
	return v, ok
}

// ─── OpMetadata ───────────────────────────────────────────────────────────────

// OpMetadata is the user-friendly representation of pb.OpMetadata.
type OpMetadata struct {
	IgnoreCache   bool
	Description   map[string]string
	ExportCache   *pb.ExportCache
	Caps          map[apicaps.CapID]bool
	ProgressGroup *pb.ProgressGroup
}

func (m OpMetadata) Clone() OpMetadata {
	out := m
	if m.Description != nil {
		out.Description = maps.Clone(m.Description)
	}
	if m.Caps != nil {
		out.Caps = maps.Clone(m.Caps)
	}
	return out
}

func (m OpMetadata) MergeWith(m2 OpMetadata) OpMetadata {
	out := m.Clone()
	if m2.IgnoreCache {
		out.IgnoreCache = true
	}
	for k, v := range m2.Description {
		if out.Description == nil {
			out.Description = make(map[string]string)
		}
		out.Description[k] = v
	}
	if m2.ExportCache != nil {
		out.ExportCache = m2.ExportCache
	}
	for k := range m2.Caps {
		if out.Caps == nil {
			out.Caps = make(map[apicaps.CapID]bool)
		}
		out.Caps[k] = true
	}
	if m2.ProgressGroup != nil {
		out.ProgressGroup = m2.ProgressGroup
	}
	return out
}

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

// ApplyConstraintsOptions applies all options to c in order.
func ApplyConstraintsOptions(c *Constraints, opts ...ConstraintsOption) {
	for _, o := range opts {
		o(c)
	}
}

func WithPlatform(p ocispecs.Platform) ConstraintsOption {
	return func(c *Constraints) { c.Platform = &p }
}

func WithWorkerConstraint(filter string) ConstraintsOption {
	return func(c *Constraints) { c.WorkerConstraints = append(c.WorkerConstraints, filter) }
}

func WithIgnoreCache() ConstraintsOption {
	return func(c *Constraints) { c.Metadata.IgnoreCache = true }
}

func WithDescription(key, value string) ConstraintsOption {
	return func(c *Constraints) {
		if c.Metadata.Description == nil {
			c.Metadata.Description = map[string]string{}
		}
		c.Metadata.Description[key] = value
	}
}

func WithCustomName(name string) ConstraintsOption {
	return WithDescription("llb.customname", name)
}

func WithCaps(caps apicaps.CapSet) ConstraintsOption {
	return func(c *Constraints) { c.Caps = &caps }
}

func WithExportCache() ConstraintsOption {
	return func(c *Constraints) {
		c.Metadata.ExportCache = &pb.ExportCache{Value: true}
	}
}

func WithBuildArg(key, value string) ConstraintsOption {
	return func(c *Constraints) {
		if c.BuildArgs == nil {
			c.BuildArgs = make(map[string]string)
		}
		c.BuildArgs[key] = value
	}
}

func WithSourceLocation(sm *SourceMap, ranges ...*pb.Range) ConstraintsOption {
	return func(c *Constraints) {
		c.SourceLocations = append(c.SourceLocations, &SourceLocation{
			SourceMap: sm,
			Ranges:    ranges,
		})
	}
}

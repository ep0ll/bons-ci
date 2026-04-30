// Package llb provides a Go DSL for constructing BuildKit LLB (Low-Level Build)
// definition graphs. States are immutable DAG nodes connected through Vertex and
// Output interfaces. Each operation implements Vertex to participate in the graph.
//
// The package extends BuildKit's standard operations (source, exec, file, merge,
// diff) with five custom operations: SolveOp, ConditionalOp, MatrixOp, InspectOp,
// and GateOp.
package llb

import (
	"maps"
	"slices"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/apicaps"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constraints
// ─────────────────────────────────────────────────────────────────────────────

// Constraints groups the build-time constraints that influence how a Vertex is
// marshalled and eventually executed by a backend. Constraints include the
// target platform, worker filters, capability requirements, operation metadata,
// and source-location debugging info.
type Constraints struct {
	Platform          *ocispecs.Platform
	WorkerConstraints []string
	Metadata          OpMetadata
	SourceLocations   []*SourceLocation
}

// ─────────────────────────────────────────────────────────────────────────────
// ConstraintsOpt
// ─────────────────────────────────────────────────────────────────────────────

// ConstraintsOpt is a functional option that mutates Constraints.
// Implementations typically close over one value and set a single field.
type ConstraintsOpt interface {
	SetConstraintsOption(*Constraints)
}

// constraintsOptFunc adapts a plain function to the ConstraintsOpt interface.
type constraintsOptFunc func(*Constraints)

func (f constraintsOptFunc) SetConstraintsOption(c *Constraints) { f(c) }

// NewConstraints applies all provided opts to a fresh Constraints value.
func NewConstraints(opts ...ConstraintsOpt) *Constraints {
	c := &Constraints{}
	for _, o := range opts {
		o.SetConstraintsOption(c)
	}
	return c
}

// ─────────────────────────────────────────────────────────────────────────────
// OpMetadata
// ─────────────────────────────────────────────────────────────────────────────

// OpMetadata carries per-vertex metadata that is either embedded in the wire
// format or consumed by frontends/debuggers.
type OpMetadata struct {
	Description   map[string]string
	ExportCache   *pb.ExportCache
	Caps          map[apicaps.CapID]bool
	ProgressGroup *pb.ProgressGroup
	IgnoreCache   bool
}

// ToPB converts OpMetadata to its protobuf representation.
func (m OpMetadata) ToPB() *pb.OpMetadata {
	md := &pb.OpMetadata{
		Description: maps.Clone(m.Description),
		ExportCache: m.ExportCache,
		IgnoreCache: m.IgnoreCache,
	}
	if m.Caps != nil {
		md.Caps = make(map[string]bool, len(m.Caps))
		for k, v := range m.Caps {
			md.Caps[string(k)] = v
		}
	}
	if m.ProgressGroup != nil {
		md.ProgressGroup = m.ProgressGroup
	}
	return md
}

// NewOpMetadata constructs an OpMetadata from its protobuf representation.
func NewOpMetadata(md *pb.OpMetadata) OpMetadata {
	om := OpMetadata{
		Description: maps.Clone(md.Description),
		ExportCache: md.ExportCache,
		IgnoreCache: md.IgnoreCache,
	}
	if md.Caps != nil {
		om.Caps = make(map[apicaps.CapID]bool, len(md.Caps))
		for k, v := range md.Caps {
			om.Caps[apicaps.CapID(k)] = v
		}
	}
	if md.ProgressGroup != nil {
		om.ProgressGroup = md.ProgressGroup
	}
	return om
}

// mergeMetadata merges two OpMetadata values, with override taking precedence.
func mergeMetadata(base, override OpMetadata) OpMetadata {
	m := OpMetadata{
		Description:   maps.Clone(base.Description),
		ExportCache:   base.ExportCache,
		Caps:          maps.Clone(base.Caps),
		ProgressGroup: base.ProgressGroup,
		IgnoreCache:   base.IgnoreCache,
	}
	if override.IgnoreCache {
		m.IgnoreCache = true
	}
	if override.ExportCache != nil {
		m.ExportCache = override.ExportCache
	}
	if override.ProgressGroup != nil {
		m.ProgressGroup = override.ProgressGroup
	}
	if len(override.Description) > 0 {
		if m.Description == nil {
			m.Description = make(map[string]string, len(override.Description))
		}
		maps.Copy(m.Description, override.Description)
	}
	if len(override.Caps) > 0 {
		if m.Caps == nil {
			m.Caps = make(map[apicaps.CapID]bool, len(override.Caps))
		}
		maps.Copy(m.Caps, override.Caps)
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// Capability helpers
// ─────────────────────────────────────────────────────────────────────────────

// AddCap registers a required capability in the constraints metadata.
func AddCap(c *Constraints, id apicaps.CapID) {
	if c.Metadata.Caps == nil {
		c.Metadata.Caps = make(map[apicaps.CapID]bool)
	}
	c.Metadata.Caps[id] = true
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience ConstraintsOpt constructors
// ─────────────────────────────────────────────────────────────────────────────

// Platform returns a ConstraintsOpt that sets the target platform.
func Platform(p ocispecs.Platform) ConstraintsOpt {
	return constraintsOptFunc(func(c *Constraints) {
		c.Platform = &p
	})
}

// WorkerConstraint appends a worker filter to the constraints.
func WorkerConstraint(filter string) ConstraintsOpt {
	return constraintsOptFunc(func(c *Constraints) {
		c.WorkerConstraints = append(c.WorkerConstraints, filter)
	})
}

// IgnoreCache marks the vertex as non-cacheable.
func IgnoreCache(c *Constraints) {
	c.Metadata.IgnoreCache = true
}

// WithDescription adds a description key-value to the vertex metadata.
func WithDescription(kvs map[string]string) ConstraintsOpt {
	return constraintsOptFunc(func(c *Constraints) {
		if c.Metadata.Description == nil {
			c.Metadata.Description = make(map[string]string, len(kvs))
		}
		maps.Copy(c.Metadata.Description, kvs)
	})
}

// WithExportCache configures the export-cache behaviour for the vertex.
func WithExportCache(ec *pb.ExportCache) ConstraintsOpt {
	return constraintsOptFunc(func(c *Constraints) {
		c.Metadata.ExportCache = ec
	})
}

// WithProgressGroup sets the progress group for frontend progress reporting.
func WithProgressGroup(pg *pb.ProgressGroup) ConstraintsOpt {
	return constraintsOptFunc(func(c *Constraints) {
		c.Metadata.ProgressGroup = pg
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// MarshalConstraints
// ─────────────────────────────────────────────────────────────────────────────

// MarshalConstraints merges base and override Constraints into a pb.Op and
// pb.OpMetadata pair for wire-format serialization. The override platform,
// worker constraints, and metadata take precedence.
func MarshalConstraints(base, override *Constraints) (*pb.Op, *pb.OpMetadata) {
	c := *base
	c.WorkerConstraints = slices.Clone(c.WorkerConstraints)

	if p := override.Platform; p != nil {
		c.Platform = p
	}

	c.WorkerConstraints = append(c.WorkerConstraints, override.WorkerConstraints...)
	c.Metadata = mergeMetadata(c.Metadata, override.Metadata)

	if c.Platform == nil {
		defaultPlatform := defaultPlatformSpec()
		c.Platform = &defaultPlatform
	}

	opPlatform := pb.Platform{
		OS:           c.Platform.OS,
		Architecture: c.Platform.Architecture,
		Variant:      c.Platform.Variant,
		OSVersion:    c.Platform.OSVersion,
	}
	if c.Platform.OSFeatures != nil {
		opPlatform.OSFeatures = slices.Clone(c.Platform.OSFeatures)
	}

	return &pb.Op{
		Platform: &opPlatform,
		Constraints: &pb.WorkerConstraints{
			Filter: c.WorkerConstraints,
		},
	}, c.Metadata.ToPB()
}

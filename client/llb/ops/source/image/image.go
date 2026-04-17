// Package image provides the Docker/OCI image source operation.
package image

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ResolveMode controls pull behaviour.
type ResolveMode int

const (
	ResolveModeDefault     ResolveMode = iota
	ResolveModeForcePull
	ResolveModePreferLocal
)

func (r ResolveMode) String() string {
	switch r {
	case ResolveModeForcePull:
		return pb.AttrImageResolveModeForcePull
	case ResolveModePreferLocal:
		return pb.AttrImageResolveModePreferLocal
	default:
		return pb.AttrImageResolveModeDefault
	}
}

// Config holds all parameters for the image source op.
type Config struct {
	Ref         string              // required, normalised on build
	ResolveMode ResolveMode
	Platform    *ocispecs.Platform  // overrides build-level platform
	LayerLimit  int                 // 0 = no limit
	Checksum    digest.Digest       // pins the image manifest
	RecordType  string              // "internal" etc.
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithRef(ref string) Option         { return func(c *Config) { c.Ref = ref } }
func WithResolveMode(m ResolveMode) Option { return func(c *Config) { c.ResolveMode = m } }
func WithPlatform(p ocispecs.Platform) Option { return func(c *Config) { c.Platform = &p } }
func WithLayerLimit(n int) Option        { return func(c *Config) { c.LayerLimit = n } }
func WithChecksum(d digest.Digest) Option { return func(c *Config) { c.Checksum = d } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// Vertex is the image source vertex.
type Vertex struct {
	config     Config
	cache      marshal.Cache
	normalised string
	identifier string
	attrs      map[string]string
}

// New constructs an image source vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Ref == "" {
		return nil, fmt.Errorf("image.New: Ref is required")
	}
	v := &Vertex{config: cfg}
	if err := v.build(); err != nil {
		return nil, err
	}
	return v, nil
}

// Must constructs a vertex and panics on error.
func Must(opts ...Option) *Vertex {
	v, err := New(opts...)
	if err != nil {
		panic(err)
	}
	return v
}

func (v *Vertex) build() error {
	cfg := &v.config
	r, err := reference.ParseNormalizedNamed(cfg.Ref)
	if err != nil {
		return fmt.Errorf("image: parse ref: %w", err)
	}
	r = reference.TagNameOnly(r)
	v.normalised = r.String()

	if cfg.Checksum != "" {
		if named, ok := r.(reference.Named); ok {
			pinned, err := reference.WithDigest(named, cfg.Checksum)
			if err != nil {
				return fmt.Errorf("image: pin digest: %w", err)
			}
			v.normalised = pinned.String()
		}
	}
	v.identifier = "docker-image://" + v.normalised

	attrs := make(map[string]string)
	if cfg.ResolveMode != ResolveModeDefault {
		attrs[pb.AttrImageResolveMode] = cfg.ResolveMode.String()
		if cfg.ResolveMode == ResolveModeForcePull {
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceImageResolveMode)
		}
	}
	if cfg.RecordType != "" {
		attrs[pb.AttrImageRecordType] = cfg.RecordType
	}
	if cfg.LayerLimit > 0 {
		attrs[pb.AttrImageLayerLimit] = strconv.Itoa(cfg.LayerLimit)
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceImageLayerLimit)
	}
	if cfg.Checksum != "" {
		attrs[pb.AttrImageChecksum] = cfg.Checksum.String()
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceImageChecksum)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceImage)

	if cfg.Platform != nil {
		v.config.Constraints.Platform = cfg.Platform
	}
	v.attrs = attrs
	return nil
}

// ─── core.Vertex ──────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType    { return core.VertexTypeSource }
func (v *Vertex) Inputs() []core.Edge      { return nil }
func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "image root filesystem"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Ref == "" {
		return &core.ValidationError{Field: "Ref", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}
	pop, md := marshal.MarshalConstraints(c, &v.config.Constraints)
	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: v.identifier,
		Attrs:      v.attrs,
	}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("image.Vertex.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) != 0 {
		return nil, &core.IncompatibleInputsError{VertexType: v.Type(), Want: "exactly 0", Got: len(inputs)}
	}
	return v, nil
}

// NormalisedRef returns the full normalised image reference.
func (v *Vertex) NormalisedRef() string { return v.normalised }

// Config returns a copy of the configuration.
func (v *Vertex) Config() Config { return v.config }

// WithOption returns a new vertex with the option applied.
func (v *Vertex) WithOption(opt Option) (*Vertex, error) {
	cfg := v.config
	opt(&cfg)
	nv := &Vertex{config: cfg}
	if err := nv.build(); err != nil {
		return nil, err
	}
	return nv, nil
}

// Output returns a core.Output for output slot 0.
func (v *Vertex) Output() core.Output { return &vertexOutput{v: v} }

type vertexOutput struct{ v *Vertex }

func (o *vertexOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *vertexOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)

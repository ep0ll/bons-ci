// Package image provides the Docker/OCI image source operation for llbx.
//
// Example
//
//	src := image.New(
//	    image.WithRef("alpine:3.20"),
//	    image.WithResolveMode(image.ResolveModePreferLocal),
//	    image.WithPlatform(ocispecs.Platform{OS: "linux", Architecture: "amd64"}),
//	)
package image

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── ResolveMode ─────────────────────────────────────────────────────────────

// ResolveMode controls how the BuildKit daemon looks up an image.
type ResolveMode int

const (
	// ResolveModeDefault defers to the daemon's configured pull policy.
	ResolveModeDefault ResolveMode = iota
	// ResolveModeForcePull always pulls from the registry.
	ResolveModeForcePull
	// ResolveModePreferLocal uses a locally cached image when available.
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

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all parameters for an image source op.
type Config struct {
	// Ref is the fully-qualified image reference (e.g., "alpine:3.20").
	// Required. Normalised automatically by New.
	Ref string

	// ResolveMode controls pull policy.
	ResolveMode ResolveMode

	// Platform overrides the target platform for this image.
	// Nil inherits the build's global platform constraint.
	Platform *ocispecs.Platform

	// LayerLimit caps the number of image layers fetched.
	// 0 = no limit.
	LayerLimit int

	// Checksum pins the image to an expected OCI manifest digest.
	Checksum digest.Digest

	// RecordType is an internal metadata tag (e.g., "internal").
	RecordType string

	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// WithRef sets the image reference.
func WithRef(ref string) Option {
	return func(c *Config) { c.Ref = ref }
}

// WithResolveMode sets the pull/resolve policy.
func WithResolveMode(m ResolveMode) Option {
	return func(c *Config) { c.ResolveMode = m }
}

// WithPlatform overrides the target platform.
func WithPlatform(p ocispecs.Platform) Option {
	return func(c *Config) { c.Platform = &p }
}

// WithLayerLimit caps the number of layers fetched.
func WithLayerLimit(n int) Option {
	return func(c *Config) { c.LayerLimit = n }
}

// WithChecksum pins the image to a specific manifest digest.
func WithChecksum(dgst digest.Digest) Option {
	return func(c *Config) { c.Checksum = dgst }
}

// WithConstraintsOption applies a core.ConstraintsOption.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx implementation of the Docker/OCI image source op.
type Vertex struct {
	config     Config
	cache      marshal.Cache
	normalised string // normalised image reference
	attrs      map[string]string
	identifier string
}

// New constructs an image source Vertex.
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

// Must constructs a Vertex and panics on error. Useful in tests and init code.
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
		return fmt.Errorf("image.Vertex.build: parse ref: %w", err)
	}
	r = reference.TagNameOnly(r)
	v.normalised = r.String()

	// Apply digest pin if provided.
	if cfg.Checksum != "" {
		if digested, ok := r.(reference.Named); ok {
			pinned, err := reference.WithDigest(digested, cfg.Checksum)
			if err != nil {
				return fmt.Errorf("image.Vertex.build: pin digest: %w", err)
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
		import_strconv_in_build(attrs, cfg)
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceImageLayerLimit)
	}
	if cfg.Checksum != "" {
		attrs[pb.AttrImageChecksum] = cfg.Checksum.String()
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceImageChecksum)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceImage)

	v.attrs = attrs

	// Copy platform into constraints if specified per-image.
	if cfg.Platform != nil {
		v.config.Constraints.Platform = cfg.Platform
	}
	return nil
}

// import_strconv_in_build is a workaround to avoid import cycle;
// inlined directly:
func import_strconv_in_build(attrs map[string]string, cfg *Config) {
	attrs[pb.AttrImageLayerLimit] = fmt.Sprintf("%d", cfg.LayerLimit)
}

// ─── core.Vertex implementation ───────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSource }

func (v *Vertex) Inputs() []core.Edge { return nil }

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
	h := marshal.Acquire(&v.cache)
	defer h.Release()

	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	if err := v.Validate(ctx, c); err != nil {
		return nil, err
	}

	pop, md := marshal.MarshalConstraints(c, &v.config.Constraints)
	pop.Op = &pb.Op_Source{
		Source: &pb.SourceOp{
			Identifier: v.identifier,
			Attrs:      v.attrs,
		},
	}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("image.Vertex.Marshal: %w", err)
	}

	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) != 0 {
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(),
			Want:       "exactly 0",
			Got:        len(inputs),
		}
	}
	return v, nil
}

// ─── Accessors & mutation builders ───────────────────────────────────────────

// NormalisedRef returns the normalised image reference (with tag).
func (v *Vertex) NormalisedRef() string { return v.normalised }

// Config returns a copy of the vertex's configuration.
func (v *Vertex) Config() Config { return v.config }

// WithOption returns a new Vertex with the given option applied.
func (v *Vertex) WithOption(opt Option) (*Vertex, error) {
	newCfg := v.config
	opt(&newCfg)
	nv := &Vertex{config: newCfg}
	if err := nv.build(); err != nil {
		return nil, fmt.Errorf("image.Vertex.WithOption: %w", err)
	}
	return nv, nil
}

// Output returns a core.Output referencing output slot 0.
func (v *Vertex) Output() core.Output { return &vertexOutput{vertex: v} }

type vertexOutput struct{ vertex *Vertex }

func (o *vertexOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex {
	return o.vertex
}
func (o *vertexOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)

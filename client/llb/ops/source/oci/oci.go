// Package oci provides the OCI layout content-store source operation.
package oci

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// Config holds all parameters for the OCI layout source op.
type Config struct {
	// Ref is the OCI layout reference (e.g. "myrepo/image@sha256:..."). Required.
	Ref string
	// SessionID identifies the BuildKit session owning the OCI content store.
	SessionID string
	// StoreID identifies the OCI content store within the session.
	StoreID string
	// LayerLimit caps the number of layers fetched. 0 = unlimited.
	LayerLimit *int
	// Checksum pins the content to an expected OCI manifest digest.
	Checksum    digest.Digest
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithRef(ref string) Option           { return func(c *Config) { c.Ref = ref } }
func WithSessionID(id string) Option      { return func(c *Config) { c.SessionID = id } }
func WithStoreID(id string) Option        { return func(c *Config) { c.StoreID = id } }
func WithLayerLimit(n int) Option         { return func(c *Config) { c.LayerLimit = &n } }
func WithChecksum(d digest.Digest) Option { return func(c *Config) { c.Checksum = d } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// Vertex is the OCI layout source vertex.
type Vertex struct {
	config Config
	cache  marshal.Cache
	attrs  map[string]string
}

// New constructs an OCI layout source vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Ref == "" {
		return nil, fmt.Errorf("oci.New: Ref is required")
	}
	v := &Vertex{config: cfg}
	v.buildAttrs()
	return v, nil
}

func (v *Vertex) buildAttrs() {
	cfg := &v.config
	attrs := make(map[string]string)
	if cfg.SessionID != "" {
		attrs[pb.AttrOCILayoutSessionID] = cfg.SessionID
	}
	if cfg.StoreID != "" {
		attrs[pb.AttrOCILayoutStoreID] = cfg.StoreID
	}
	if cfg.LayerLimit != nil {
		attrs[pb.AttrOCILayoutLayerLimit] = strconv.Itoa(*cfg.LayerLimit)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceOCILayout)
	v.attrs = attrs
}

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSource }
func (v *Vertex) Inputs() []core.Edge   { return nil }
func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "OCI layout image"}}
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
		Identifier: "oci-layout://" + v.config.Ref,
		Attrs:      v.attrs,
	}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("oci.Marshal: %w", err)
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

func (v *Vertex) Config() Config      { return v.config }
func (v *Vertex) Output() core.Output { return &ociOutput{v: v} }

type ociOutput struct{ v *Vertex }

func (o *ociOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *ociOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
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

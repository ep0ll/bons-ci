// Package local provides the local-directory source operation.
package local

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

type DiffType string

const (
	DiffNone     DiffType = pb.AttrLocalDifferNone
	DiffMetadata DiffType = pb.AttrLocalDifferMetadata
)

type Config struct {
	Name               string   // required
	SessionID          string
	IncludePatterns    []string
	ExcludePatterns    []string
	FollowPaths        []string
	SharedKeyHint      string
	Differ             DiffType
	DifferRequired     bool
	MetadataOnly       bool
	MetadataExceptions []string
	Constraints        core.Constraints
}

type Option func(*Config)

func WithName(name string) Option             { return func(c *Config) { c.Name = name } }
func WithSessionID(id string) Option          { return func(c *Config) { c.SessionID = id } }
func WithIncludePatterns(p []string) Option   { return func(c *Config) { c.IncludePatterns = p } }
func WithExcludePatterns(p []string) Option   { return func(c *Config) { c.ExcludePatterns = p } }
func WithFollowPaths(p []string) Option       { return func(c *Config) { c.FollowPaths = p } }
func WithSharedKeyHint(h string) Option       { return func(c *Config) { c.SharedKeyHint = h } }
func WithDiffer(t DiffType, req bool) Option  { return func(c *Config) { c.Differ = t; c.DifferRequired = req } }
func WithMetadataOnly(exc []string) Option    { return func(c *Config) { c.MetadataOnly = true; c.MetadataExceptions = exc } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

type Vertex struct {
	config Config
	cache  marshal.Cache
	attrs  map[string]string
}

func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("local.New: Name is required")
	}
	v := &Vertex{config: cfg}
	v.buildAttrs()
	return v, nil
}

func (v *Vertex) buildAttrs() {
	cfg := &v.config
	attrs := make(map[string]string)
	if cfg.SessionID != "" {
		attrs[pb.AttrLocalSessionID] = cfg.SessionID
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocalSessionID)
	}
	if len(cfg.IncludePatterns) > 0 {
		if dt, err := json.Marshal(cfg.IncludePatterns); err == nil {
			attrs[pb.AttrIncludePatterns] = string(dt)
		}
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocalIncludePatterns)
	}
	if len(cfg.ExcludePatterns) > 0 {
		if dt, err := json.Marshal(cfg.ExcludePatterns); err == nil {
			attrs[pb.AttrExcludePatterns] = string(dt)
		}
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocalExcludePatterns)
	}
	if len(cfg.FollowPaths) > 0 {
		if dt, err := json.Marshal(cfg.FollowPaths); err == nil {
			attrs[pb.AttrFollowPaths] = string(dt)
		}
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocalFollowPaths)
	}
	if cfg.SharedKeyHint != "" {
		attrs[pb.AttrSharedKeyHint] = cfg.SharedKeyHint
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocalSharedKeyHint)
	}
	if cfg.Differ != "" {
		attrs[pb.AttrLocalDiffer] = string(cfg.Differ)
		if cfg.DifferRequired {
			core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocalDiffer)
		}
	}
	if cfg.MetadataOnly {
		attrs[pb.AttrMetadataTransfer] = "true"
		if len(cfg.MetadataExceptions) > 0 {
			if dt, err := json.Marshal(cfg.MetadataExceptions); err == nil {
				attrs[pb.AttrMetadataTransferExclude] = string(dt)
			}
		}
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceMetadataTransfer)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocal)
	v.attrs = attrs
}

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSource }
func (v *Vertex) Inputs() []core.Edge   { return nil }
func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "local directory"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Name == "" {
		return &core.ValidationError{Field: "Name", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	cfg := v.config
	attrs := make(map[string]string, len(v.attrs)+1)
	for k, val := range v.attrs {
		attrs[k] = val
	}
	if _, hasSession := attrs[pb.AttrLocalSessionID]; !hasSession {
		uid := cfg.Constraints.LocalUniqueID
		if uid == "" {
			uid = c.LocalUniqueID
		}
		attrs[pb.AttrLocalUniqueID] = uid
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceLocalUnique)
	}

	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)
	pop.Platform = nil
	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: "local://" + cfg.Name,
		Attrs:      attrs,
	}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("local.Marshal: %w", err)
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

func (v *Vertex) Config() Config { return v.config }
func (v *Vertex) Output() core.Output { return &localOutput{v: v} }

type localOutput struct{ v *Vertex }

func (o *localOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *localOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
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

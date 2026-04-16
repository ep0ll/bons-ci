// Package local provides the local-directory source operation for llbx.
//
// Example
//
//	src := local.New(
//	    local.WithName("context"),
//	    local.WithIncludePatterns([]string{"*.go", "go.mod"}),
//	    local.WithExcludePatterns([]string{"vendor/", "_test.go"}),
//	)
package local

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── DiffType controls file comparison strategy ───────────────────────────────

// DiffType specifies the file-comparison strategy used when syncing the local
// directory to the BuildKit daemon.
type DiffType string

const (
	// DiffNone retransmits all files without comparison.
	DiffNone DiffType = pb.AttrLocalDifferNone
	// DiffMetadata compares size, mtime, mode, owner (default BuildKit behaviour).
	DiffMetadata DiffType = pb.AttrLocalDifferMetadata
)

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all parameters for a local source op.
type Config struct {
	// Name is the logical name of the local directory (e.g., "context").
	// Required.
	Name string

	// SessionID pins the local source to a specific build session. Usually
	// set automatically by the BuildKit client.
	SessionID string

	// IncludePatterns is the list of glob patterns to include.
	// An empty list means "include everything".
	IncludePatterns []string

	// ExcludePatterns is the list of glob patterns to exclude.
	ExcludePatterns []string

	// FollowPaths is a list of paths to follow regardless of include patterns.
	FollowPaths []string

	// SharedKeyHint aids cross-build cache reuse by providing a stable key.
	SharedKeyHint string

	// Differ controls the file-comparison strategy.
	Differ DiffType

	// DifferRequired requires the daemon to support the chosen differ mode.
	DifferRequired bool

	// MetadataOnly enables metadata-only transfer (experimental).
	MetadataOnly bool
	// MetadataExceptions are paths exempt from MetadataOnly transfer.
	MetadataExceptions []string

	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// WithName sets the logical directory name.
func WithName(name string) Option { return func(c *Config) { c.Name = name } }

// WithSessionID pins the source to a build session.
func WithSessionID(id string) Option { return func(c *Config) { c.SessionID = id } }

// WithIncludePatterns sets the include glob patterns.
func WithIncludePatterns(patterns []string) Option {
	return func(c *Config) { c.IncludePatterns = patterns }
}

// WithExcludePatterns sets the exclude glob patterns.
func WithExcludePatterns(patterns []string) Option {
	return func(c *Config) { c.ExcludePatterns = patterns }
}

// WithFollowPaths sets paths to always include.
func WithFollowPaths(paths []string) Option {
	return func(c *Config) { c.FollowPaths = paths }
}

// WithSharedKeyHint sets the shared cache key hint.
func WithSharedKeyHint(hint string) Option {
	return func(c *Config) { c.SharedKeyHint = hint }
}

// WithDiffer sets the file-comparison strategy.
func WithDiffer(t DiffType, required bool) Option {
	return func(c *Config) { c.Differ = t; c.DifferRequired = required }
}

// WithMetadataOnly enables metadata-only transfer with optional exceptions.
func WithMetadataOnly(exceptions []string) Option {
	return func(c *Config) {
		c.MetadataOnly = true
		c.MetadataExceptions = exceptions
	}
}

// WithConstraintsOption applies a core.ConstraintsOption.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx implementation of the local-directory source op.
type Vertex struct {
	config Config
	cache  marshal.Cache
	attrs  map[string]string
}

// New constructs a local source Vertex.
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

// ─── core.Vertex ──────────────────────────────────────────────────────────────

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
	h := marshal.Acquire(&v.cache)
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	cfg := v.config
	attrs := make(map[string]string, len(v.attrs)+1)
	for k, val := range v.attrs {
		attrs[k] = val
	}
	// LocalUniqueID is resolved at marshal time from the Constraints.
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
		return nil, fmt.Errorf("local.Vertex.Marshal: %w", err)
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

// Config returns a copy of the vertex's configuration.
func (v *Vertex) Config() Config { return v.config }

// Output returns a core.Output referencing output slot 0.
func (v *Vertex) Output() core.Output { return &vertexOutput{vertex: v} }

type vertexOutput struct{ vertex *Vertex }

func (o *vertexOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.vertex }
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

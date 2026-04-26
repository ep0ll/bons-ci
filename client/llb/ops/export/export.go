// Package export provides the ExportOp vertex that declares export intent at
// the vertex level. This enables declarative, per-branch export targets such
// as OCI-compliant images, Docker tarballs, local directories, or registry
// pushes.
//
// The ExportOp itself does not perform the actual export; it serialises
// the declaration into the wire format so the solver/exporter subsystem can
// interpret and execute it.
//
// Example
//
//	alpine, _ := image.New(image.WithRef("alpine:3.20"))
//	exp, _ := export.New(
//	    export.WithInput(alpine.Output()),
//	    export.WithFormat(export.FormatOCIImage),
//	    export.WithImageRef("registry.example.com/myapp:latest"),
//	    export.WithPush(true),
//	)
package export

import (
	"context"
	"fmt"
	"strings"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── ExportFormat ────────────────────────────────────────────────────────────

// ExportFormat identifies the output format.
type ExportFormat string

const (
	FormatOCIImage    ExportFormat = "oci-image"
	FormatDockerTar   ExportFormat = "docker-tar"
	FormatLocal       ExportFormat = "local"
	FormatRegistryPush ExportFormat = "registry-push"
)

// Validate checks whether the format is a known type.
func (f ExportFormat) Validate() error {
	switch f {
	case FormatOCIImage, FormatDockerTar, FormatLocal, FormatRegistryPush:
		return nil
	default:
		return fmt.Errorf("%w: %q", core.ErrUnsupportedExportFormat, f)
	}
}

// ─── Compression ─────────────────────────────────────────────────────────────

// Compression specifies layer compression.
type Compression string

const (
	CompressionDefault  Compression = ""
	CompressionGzip     Compression = "gzip"
	CompressionEstargz  Compression = "estargz"
	CompressionZstd     Compression = "zstd"
	CompressionNone     Compression = "uncompressed"
)

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds all parameters for the export op.
type Config struct {
	// Input is the root output to export. Required.
	Input core.Output
	// Format specifies the export type. Required.
	Format ExportFormat
	// ImageRef is the target image reference for OCI/registry exports.
	ImageRef string
	// MediaType overrides the default media type for manifests.
	MediaType string
	// Annotations are OCI annotations attached to the image manifest.
	Annotations map[string]string
	// Compression specifies layer compression (default = gzip).
	Compression Compression
	// Push enables pushing to a remote registry (for FormatRegistryPush).
	Push bool
	// LocalPath is the target directory for FormatLocal exports.
	LocalPath string
	// Description is a human-readable label.
	Description string
	Constraints core.Constraints
	// Observer receives lifecycle notifications (optional).
	Observer Observer
	// Hooks are invoked at marshal boundaries (optional).
	Hooks []Hook
	// Attestations are in-toto attestation predicates to attach (optional).
	Attestations []AttestationPredicate
	// Platforms configures multi-platform export (manifest list) (optional).
	Platforms []string
}

// Option is a functional option for Config.
type Option func(*Config)

func WithInput(out core.Output) Option        { return func(c *Config) { c.Input = out } }
func WithFormat(f ExportFormat) Option        { return func(c *Config) { c.Format = f } }
func WithImageRef(ref string) Option          { return func(c *Config) { c.ImageRef = ref } }
func WithMediaType(mt string) Option          { return func(c *Config) { c.MediaType = mt } }
func WithCompression(comp Compression) Option { return func(c *Config) { c.Compression = comp } }
func WithPush(push bool) Option               { return func(c *Config) { c.Push = push } }
func WithLocalPath(path string) Option        { return func(c *Config) { c.LocalPath = path } }
func WithDescription(desc string) Option      { return func(c *Config) { c.Description = desc } }
func WithAnnotation(key, value string) Option {
	return func(c *Config) {
		if c.Annotations == nil {
			c.Annotations = make(map[string]string)
		}
		c.Annotations[key] = value
	}
}
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex is the export op. It serialises an export declaration into the wire
// format as a custom source op with an "export://" identifier carrying format
// and target attributes. The actual export is performed by the solver/exporter
// subsystem when it encounters this vertex.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs an export vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Input == nil {
		return nil, fmt.Errorf("export.New: Input is required")
	}
	if err := cfg.Format.Validate(); err != nil {
		return nil, fmt.Errorf("export.New: %w", err)
	}
	return &Vertex{config: cfg}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeExport }

func (v *Vertex) Inputs() []core.Edge {
	if v.config.Input == nil {
		return nil
	}
	vtx := v.config.Input.Vertex(context.Background(), nil)
	if vtx == nil {
		return nil
	}
	return []core.Edge{{Vertex: vtx, Index: 0}}
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "export result"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.Input == nil {
		return &core.ValidationError{Field: "Input", Cause: fmt.Errorf("must not be nil")}
	}
	if err := v.config.Format.Validate(); err != nil {
		return &core.ValidationError{Field: "Format", Cause: err}
	}
	switch v.config.Format {
	case FormatOCIImage, FormatRegistryPush:
		if v.config.ImageRef == "" {
			return &core.ValidationError{Field: "ImageRef", Cause: fmt.Errorf("required for format %q", v.config.Format)}
		}
	case FormatLocal:
		if v.config.LocalPath == "" {
			return &core.ValidationError{Field: "LocalPath", Cause: fmt.Errorf("required for format %q", v.config.Format)}
		}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}
	if err := v.Validate(ctx, c); err != nil {
		return nil, err
	}

	cfg := &v.config
	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)

	// Wire the input.
	if cfg.Input != nil {
		inp, err := cfg.Input.ToInput(ctx, c)
		if err != nil {
			return nil, &core.ExportError{Format: string(cfg.Format), Cause: fmt.Errorf("input: %w", err)}
		}
		pop.Inputs = append(pop.Inputs, inp)
	}

	// Build attributes map.
	attrs := make(map[string]string)
	attrs["format"] = string(cfg.Format)
	if cfg.ImageRef != "" {
		attrs["image.ref"] = cfg.ImageRef
	}
	if cfg.MediaType != "" {
		attrs["media.type"] = cfg.MediaType
	}
	if cfg.Compression != CompressionDefault {
		attrs["compression"] = string(cfg.Compression)
	}
	if cfg.Push {
		attrs["push"] = "true"
	}
	if cfg.LocalPath != "" {
		attrs["local.path"] = cfg.LocalPath
	}
	if cfg.Description != "" {
		attrs["description"] = cfg.Description
	}
	// Flatten annotations with "annotation." prefix.
	for k, val := range cfg.Annotations {
		attrs["annotation."+k] = val
	}

	// Use a custom source op with export:// identifier.
	identifier := "export://" + string(cfg.Format)
	if cfg.ImageRef != "" {
		identifier += "/" + strings.TrimPrefix(cfg.ImageRef, "/")
	}

	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: identifier,
		Attrs:      attrs,
	}}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, &core.ExportError{Format: string(cfg.Format), Cause: fmt.Errorf("marshal: %w", err)}
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	switch len(inputs) {
	case 0:
		return v, nil
	case 1:
		newCfg := v.config
		newCfg.Input = &core.EdgeOutput{E: inputs[0]}
		return &Vertex{config: newCfg}, nil
	default:
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(), Got: len(inputs), Want: "0 or 1",
		}
	}
}

// Output returns a core.Output for output slot 0.
func (v *Vertex) Output() core.Output { return &core.SimpleOutput{V: v, Slot: 0} }

// Config returns a copy of the configuration.
func (v *Vertex) Config() Config { return v.config }

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)

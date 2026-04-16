// Package containerimage provides a production-ready OCI/Docker image exporter.
// It satisfies core.Exporter and registers itself with core.DefaultRegistry via init().
//
// Extending the framework with a new backend:
//
//	package mybackend
//
//	type MyExporter struct{}
//
//	func (e *MyExporter) Type() core.ExporterType { return "mybackend" }
//
//	func (e *MyExporter) Resolve(ctx context.Context, opts core.Options) (core.ExporterInstance, error) {
//	    cfg, err := parseConfig(opts)
//	    ...
//	    return &myInstance{cfg: cfg}, nil
//	}
//
//	func init() { core.DefaultRegistry.MustRegister(&MyExporter{}) }
package containerimage

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// ─── Option keys ───────────────────────────────────────────────────────────

// Option key constants accepted by this exporter's Resolve method.
const (
	OptKeyName                = "name"
	OptKeyPush                = "push"
	OptKeyPushByDigest        = "push-by-digest"
	OptKeyInsecure            = "registry.insecure"
	OptKeyStore               = "store"
	OptKeyOCITypes            = "oci-mediatypes"
	OptKeyOCIArtifact         = "oci-artifact"
	OptKeyCompression         = "compression"
	OptKeyCompressionLevel    = "compression-level"
	OptKeyForceCompression    = "force-compression"
	OptKeySourceDateEpoch     = "source-date-epoch"
	OptKeyRewriteTimestamp    = "rewrite-timestamp"
	OptKeyForceInlineAttest   = "attestation-inline"
	OptKeyPreferNondistLayers = "prefer-nondist-layers"
	OptKeyNameCanonical       = "name-canonical"
	OptKeyDanglingPrefix      = "dangling-name-prefix"
	OptKeyUnpack              = "unpack"
)

// CompressionType selects the layer compression algorithm.
type CompressionType string

const (
	CompressionDefault      CompressionType = "gzip"
	CompressionUncompressed CompressionType = "uncompressed"
	CompressionGzip         CompressionType = "gzip"
	CompressionZstd         CompressionType = "zstd"
	CompressionEstargz      CompressionType = "estargz"
)

// ─── Config ────────────────────────────────────────────────────────────────

// Config holds the fully-parsed, validated configuration for a single image
// export invocation. It is deliberately distinct from core.Options (the raw
// string map) to give strong types and default values.
type Config struct {
	// ImageName is the comma-separated list of target image names.
	ImageName string

	// Push causes the produced manifest to be pushed to a remote registry.
	Push bool

	// PushByDigest pushes without a tag.
	PushByDigest bool

	// Insecure allows plain-HTTP registry connections.
	Insecure bool

	// Store persists the image in the local containerd/Docker image store.
	Store bool

	// OCITypes uses OCI media types instead of Docker legacy media types.
	OCITypes bool

	// OCIArtifact packages attestation manifests as OCI artifacts.
	OCIArtifact bool

	// Compression selects the layer compression algorithm.
	Compression CompressionType

	// CompressionLevel is the algorithm-specific compression level.
	// 0 means "use default for the algorithm".
	CompressionLevel int

	// ForceCompression re-compresses already-compressed layers.
	ForceCompression bool

	// RewriteTimestamp applies SOURCE_DATE_EPOCH to all layer timestamps.
	RewriteTimestamp bool

	// ForceInlineAttestations embeds all attestations inline even if they
	// would normally be stored out-of-band.
	ForceInlineAttestations bool

	// PreferNondistributableLayers preserves non-distributable layer types.
	PreferNondistributableLayers bool

	// NameCanonical appends "<name>@<digest>" to the tag name list.
	NameCanonical bool

	// DanglingPrefix is prepended to digest-tagged images.
	DanglingPrefix string

	// Unpack triggers immediate snapshot unpack after store (containerd).
	Unpack bool

	// ExtraAnnotations are additional OCI annotations to merge into produced manifests.
	ExtraAnnotations map[string]string
}

// DefaultConfig returns a Config with production-safe defaults.
func DefaultConfig() Config {
	return Config{
		Store:                   true,
		OCITypes:                true,
		Compression:             CompressionDefault,
		ForceInlineAttestations: true,
	}
}

// ParseConfig validates and parses a core.Options map into a Config.
// Unknown keys are returned in the second return value so the caller can
// decide whether to error or ignore them.
func ParseConfig(opts core.Options) (Config, core.Options, error) {
	cfg := DefaultConfig()
	unknown := make(core.Options)

	for k, v := range opts {
		var err error
		switch k {
		case OptKeyName:
			cfg.ImageName = v
		case OptKeyPush:
			cfg.Push, err = parseBoolOrTrue(v)
		case OptKeyPushByDigest:
			cfg.PushByDigest, err = parseBoolOrTrue(v)
		case OptKeyInsecure:
			cfg.Insecure, err = parseBoolOrTrue(v)
		case OptKeyStore:
			cfg.Store, err = parseBoolOrTrue(v)
		case OptKeyOCITypes:
			cfg.OCITypes, err = parseBoolOrDefault(v, true)
		case OptKeyOCIArtifact:
			cfg.OCIArtifact, err = parseBoolOrTrue(v)
		case OptKeyCompression:
			cfg.Compression, err = parseCompression(v)
		case OptKeyCompressionLevel:
			cfg.CompressionLevel, err = parseInt(k, v)
		case OptKeyForceCompression:
			cfg.ForceCompression, err = parseBoolOrTrue(v)
		case OptKeyRewriteTimestamp:
			cfg.RewriteTimestamp, err = parseBoolOrTrue(v)
		case OptKeyForceInlineAttest:
			cfg.ForceInlineAttestations, err = parseBoolOrTrue(v)
		case OptKeyPreferNondistLayers:
			cfg.PreferNondistributableLayers, err = parseBoolOrTrue(v)
		case OptKeyNameCanonical:
			cfg.NameCanonical, err = parseBoolOrTrue(v)
		case OptKeyDanglingPrefix:
			cfg.DanglingPrefix = v
		case OptKeyUnpack:
			cfg.Unpack, err = parseBoolOrTrue(v)
		default:
			unknown[k] = v
		}
		if err != nil {
			return Config{}, nil, err
		}
	}

	if cfg.OCIArtifact && !cfg.OCITypes {
		cfg.OCITypes = true // OCI artifact requires OCI media types
	}

	return cfg, unknown, nil
}

// ─── Exporter ──────────────────────────────────────────────────────────────

// ContainerImageExporter is the core.Exporter implementation for
// OCI/Docker container images. It is registered in init().
type ContainerImageExporter struct {
	store  core.ContentStore
	pusher Pusher
	storer ImageStorer
}

// ExporterOption is a functional option for ContainerImageExporter.
type ExporterOption func(*ContainerImageExporter)

// WithContentStore injects a ContentStore dependency.
func WithContentStore(s core.ContentStore) ExporterOption {
	return func(e *ContainerImageExporter) { e.store = s }
}

// WithPusher injects a remote-push implementation.
func WithPusher(p Pusher) ExporterOption {
	return func(e *ContainerImageExporter) { e.pusher = p }
}

// WithImageStorer injects a local image-store implementation.
func WithImageStorer(s ImageStorer) ExporterOption {
	return func(e *ContainerImageExporter) { e.storer = s }
}

// New returns a ContainerImageExporter. Callers must supply dependencies
// via functional options; nil dependencies cause Export to fail gracefully.
func New(options ...ExporterOption) *ContainerImageExporter {
	e := &ContainerImageExporter{}
	for _, o := range options {
		o(e)
	}
	return e
}

// Type satisfies core.Exporter.
func (e *ContainerImageExporter) Type() core.ExporterType {
	return core.ExporterTypeContainerImage
}

// Resolve validates opts and creates a configured ContainerImageInstance.
func (e *ContainerImageExporter) Resolve(_ context.Context, opts core.Options) (core.ExporterInstance, error) {
	cfg, unknown, err := ParseConfig(opts)
	if err != nil {
		return nil, fmt.Errorf("containerimage exporter: %w", err)
	}
	// Strict-mode: reject unknown keys.
	if len(unknown) > 0 {
		for k := range unknown {
			return nil, core.NewOptionError(k, opts[k], "unknown option for containerimage exporter")
		}
	}
	return newInstance(cfg, e.store, e.pusher, e.storer), nil
}

// ─── Dependency interfaces ─────────────────────────────────────────────────

// Pusher pushes a manifest digest to a named remote reference.
type Pusher interface {
	Push(ctx context.Context, ref string, dgst core.Digest, store core.ContentStore) error
}

// ImageStorer persists a named image in the local image store.
type ImageStorer interface {
	Store(ctx context.Context, name string, desc core.BlobDescriptor) error
	Unpack(ctx context.Context, name string) error
}

// ─── parse helpers ─────────────────────────────────────────────────────────

func parseBoolOrTrue(v string) (bool, error) {
	if v == "" {
		return true, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("expected bool, got %q", v)
	}
	return b, nil
}

func parseBoolOrDefault(v string, def bool) (bool, error) {
	if v == "" {
		return def, nil
	}
	return parseBoolOrTrue(v)
}

func parseInt(key, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, core.NewOptionError(key, v, "expected integer")
	}
	return n, nil
}

func parseCompression(v string) (CompressionType, error) {
	switch CompressionType(v) {
	case CompressionUncompressed, CompressionGzip, CompressionZstd, CompressionEstargz:
		return CompressionType(v), nil
	case "":
		return CompressionDefault, nil
	default:
		return "", fmt.Errorf("unknown compression type %q", v)
	}
}

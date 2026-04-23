package core

import (
	"fmt"
	"time"
)

// ─── ExportRequest ─────────────────────────────────────────────────────────

// ExportRequest is the fully-validated input to ExporterInstance.Export.
// Use ExportRequestBuilder to construct instances; direct struct literal
// construction is intentionally discouraged (use the builder).
type ExportRequest struct {
	// SessionID identifies the originating build session for auth and tracing.
	SessionID string

	// Artifact is the build output to be exported.
	Artifact *Artifact

	// ImageName is the optional target name (e.g. "registry.example.com/app:v1").
	// Multiple names may be comma-separated.
	ImageName string

	// Push causes the exporter to push the result to a remote registry (where applicable).
	Push bool

	// PushByDigest pushes without a tag, referencing only by digest.
	PushByDigest bool

	// Insecure permits plain-HTTP registry connections.
	Insecure bool

	// Epoch, when set, clamps all timestamps in the produced artifact.
	// Used for reproducible builds (SOURCE_DATE_EPOCH).
	Epoch *time.Time

	// Annotations are OCI annotations to merge into the produced manifest(s).
	Annotations map[string]string

	// Labels are arbitrary key/value pairs attached to the export result
	// for downstream tooling (not part of the OCI spec).
	Labels map[string]string

	// Reporter receives real-time progress events.
	Reporter ProgressReporter

	// Store, when true, persists the image to the local image store (where available).
	Store bool

	// ExtraOptions holds exporter-specific options that do not have typed fields above.
	ExtraOptions Options
}

// Validate returns an error if the request is malformed.
func (r *ExportRequest) Validate() error {
	if r.Artifact == nil {
		return fmt.Errorf("ExportRequest: Artifact must not be nil")
	}
	if r.SessionID == "" {
		return fmt.Errorf("ExportRequest: SessionID must not be empty")
	}
	if r.Reporter == nil {
		return fmt.Errorf("ExportRequest: Reporter must not be nil (use progress.Noop() if no reporting is needed)")
	}
	return nil
}

// ─── ExportResult ──────────────────────────────────────────────────────────

// ExportResult holds all metadata produced by a completed export.
type ExportResult struct {
	// Descriptor is the root OCI descriptor for container-image exports.
	// For other export types this field may be nil.
	Descriptor *BlobDescriptor

	// ImageDigest is the digest of the exported manifest (convenience accessor).
	ImageDigest Digest

	// ImageName is the resolved, canonical image name used during export.
	ImageName string

	// ConfigDigest is the digest of the image config blob.
	ConfigDigest Digest

	// Metadata is an open-ended map of string results surfaced to callers.
	// Well-known keys are declared as ExportResultKey constants.
	Metadata map[string]string
}

// ExportResultKey is a type-safe key for ExportResult.Metadata.
type ExportResultKey string

const (
	ResultKeyImageDigest  ExportResultKey = "containerimage.digest"
	ResultKeyImageName    ExportResultKey = "image.name"
	ResultKeyConfigDigest ExportResultKey = "containerimage.config.digest"
	ResultKeyDescriptor   ExportResultKey = "containerimage.descriptor"
)

// Get returns a metadata value by key.
func (r *ExportResult) Get(key ExportResultKey) string {
	if r.Metadata == nil {
		return ""
	}
	return r.Metadata[string(key)]
}

// Set stores a metadata key/value pair.
func (r *ExportResult) Set(key ExportResultKey, value string) {
	if r.Metadata == nil {
		r.Metadata = make(map[string]string)
	}
	r.Metadata[string(key)] = value
}

// ─── ExportRequestBuilder ──────────────────────────────────────────────────

// ExportRequestBuilder provides a fluent, validated way to construct
// ExportRequest values. Required fields are enforced at Build() time.
//
// Usage:
//
//	req, err := core.NewExportRequest().
//	    WithSessionID("sess-123").
//	    WithArtifact(artifact).
//	    WithImageName("registry.example.com/app:latest").
//	    WithPush(true).
//	    WithReporter(reporter).
//	    Build()
type ExportRequestBuilder struct {
	req ExportRequest
}

// NewExportRequest creates a new builder with sensible defaults.
func NewExportRequest() *ExportRequestBuilder {
	return &ExportRequestBuilder{
		req: ExportRequest{
			Store:        true,
			Annotations:  make(map[string]string),
			Labels:       make(map[string]string),
			ExtraOptions: make(Options),
		},
	}
}

func (b *ExportRequestBuilder) WithSessionID(id string) *ExportRequestBuilder {
	b.req.SessionID = id
	return b
}

func (b *ExportRequestBuilder) WithArtifact(a *Artifact) *ExportRequestBuilder {
	b.req.Artifact = a
	return b
}

func (b *ExportRequestBuilder) WithImageName(name string) *ExportRequestBuilder {
	b.req.ImageName = name
	return b
}

func (b *ExportRequestBuilder) WithPush(push bool) *ExportRequestBuilder {
	b.req.Push = push
	return b
}

func (b *ExportRequestBuilder) WithPushByDigest(v bool) *ExportRequestBuilder {
	b.req.PushByDigest = v
	return b
}

func (b *ExportRequestBuilder) WithInsecure(v bool) *ExportRequestBuilder {
	b.req.Insecure = v
	return b
}

func (b *ExportRequestBuilder) WithEpoch(t *time.Time) *ExportRequestBuilder {
	b.req.Epoch = t
	return b
}

func (b *ExportRequestBuilder) WithAnnotation(key, value string) *ExportRequestBuilder {
	b.req.Annotations[key] = value
	return b
}

func (b *ExportRequestBuilder) WithLabel(key, value string) *ExportRequestBuilder {
	b.req.Labels[key] = value
	return b
}

func (b *ExportRequestBuilder) WithReporter(r ProgressReporter) *ExportRequestBuilder {
	b.req.Reporter = r
	return b
}

func (b *ExportRequestBuilder) WithStore(v bool) *ExportRequestBuilder {
	b.req.Store = v
	return b
}

func (b *ExportRequestBuilder) WithExtraOption(key, value string) *ExportRequestBuilder {
	b.req.ExtraOptions[key] = value
	return b
}

// Build validates and returns the constructed ExportRequest.
func (b *ExportRequestBuilder) Build() (*ExportRequest, error) {
	req := b.req // copy
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return &req, nil
}

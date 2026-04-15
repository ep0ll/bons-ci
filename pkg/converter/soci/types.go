package soci

import (
	"context"

	sociPkg "github.com/awslabs/soci-snapshotter/soci"
	"github.com/bons/bons-ci/core/images/converter"
)

// ─── Annotation constants ─────────────────────────────────────────────────────
// Provide constant aliases from the upstream awslabs soci-snapshotter to
// guarantee type-safety and ensure we never use incorrect label strings.

const (
	// ImageAnnotationSociIndexDigest is placed on the original image manifest
	// after SOCI conversion, pointing to the SOCI Index blob digest.
	ImageAnnotationSociIndexDigest = sociPkg.ImageAnnotationSociIndexDigest // "com.amazon.awslabs.soci.index.digest"

	// IndexAnnotationBuildToolIdentifier is embedded inside the SOCI Index config.
	IndexAnnotationBuildToolIdentifier = appendOptionalIndexAnnotationBuildToolIdentifier

	// SociIndexArtifactTypeV1 is the artifact type associated with SOCI v1.
	SociIndexArtifactTypeV1 = sociPkg.SociIndexArtifactType

	// SociLayerMediaType represents the zTOC layer mediatype.
	SociLayerMediaType = sociPkg.SociLayerMediaType

	configGCLabelKey = "containerd.io/gc.ref.content.config"
)

// In soci 0.5.0/0.6.x there isn't actually an IndexAnnotationBuildToolIdentifier
// exported directly in `sociPkg` but rather building the index adds its own tools.
// We provide an alias for backwards compat/safety.
const appendOptionalIndexAnnotationBuildToolIdentifier = "com.amazon.awslabs.soci.build-tool-identifier"

// ─── Options ──────────────────────────────────────────────────────────────────

// PackOption configures the SOCI index generation behaviour.
type PackOption struct {
	// SpanSize configures the chunk size for zTOC generation (default 4 MiB)
	SpanSize int64

	// MinLayerSize skips zTOC generation for layers smaller than this
	// threshold (default 10 MiB)
	MinLayerSize int64

	// BuildToolIdentifier annotates the SOCI Index with the build tool info.
	BuildToolIdentifier string

	// GCRoot controls whether we place a containerd gc root label on the index.
	GCRoot bool
}

// LayerConvertHook exposes reactive lifecycle hooks for the layer pass.
// Because SOCI layers are passed through un-modified, the hook is the primary
// mechanism for ensuring the overarching `orchestrator` can track bytes-read
// during the ingestion flow.
type LayerConvertHook struct {
	// LayerReader gives the converter orchestrator a SectionReader over the layer blob.
	LayerReader converter.LayerReaderFunc

	// Done triggers when the layer conversion pass finishes.
	Done func(ctx context.Context)
}

// MergeOption configures the manifest Merge hook.
type MergeOption struct {
	// OCI forces the resulting manifest to use OCI media types.
	OCI bool

	// MergeManifest unwraps a single-entry index into a bare manifest.
	MergeManifest bool

	// WithReferrer uses the newly adopted OCI Referrers API to link the
	// resulting SOCI index manifest to its parent original manifest using
	// the `Subject` field.
	WithReferrer bool
}

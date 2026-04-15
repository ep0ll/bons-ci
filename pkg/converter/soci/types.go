package soci

import (
	"context"
	"sync"

	sociPkg "github.com/awslabs/soci-snapshotter/soci"
	"github.com/bons/bons-ci/core/images/converter"
)

// ─── Annotation / label constants ────────────────────────────────────────────
//
// All constants alias the upstream soci-snapshotter package so downstream code
// (snapshotters, runtimes, tests) remains in sync without embedding magic strings.

const (
	// ImageAnnotationSociIndexDigest is placed on the original image manifest
	// after SOCI conversion, recording the SOCI Index blob digest so that the
	// soci-snapshotter can locate the pre-built zTOCs at run-time.
	// Value: "com.amazon.soci.index-digest"
	ImageAnnotationSociIndexDigest = sociPkg.ImageAnnotationSociIndexDigest

	// IndexAnnotationBuildToolIdentifier is an annotation on the SOCI Index
	// manifest that records which build tool produced it.
	// Value: "com.amazon.soci.build-tool-identifier"
	IndexAnnotationBuildToolIdentifier = sociPkg.IndexAnnotationBuildToolIdentifier

	// SociIndexArtifactTypeV1 is the OCI artifact type of a v1 SOCI Index.
	// V1 uses the OCI subject/referrers API to link the index to its image.
	// Value: "application/vnd.amazon.soci.index.v1+json"
	SociIndexArtifactTypeV1 = sociPkg.SociIndexArtifactTypeV1

	// SociLayerMediaType is the media type used for zTOC layer blobs
	// stored alongside the SOCI Index.
	// Value: "application/octet-stream"
	SociLayerMediaType = sociPkg.SociLayerMediaType

	// configGCLabelKey is the containerd GC root label that pins the image
	// config blob to its manifest so the garbage collector does not reclaim
	// it independently.
	configGCLabelKey = "containerd.io/gc.ref.content.config"
)

// ─── Buffer pool ──────────────────────────────────────────────────────────────

// bufPool recycles 1 MiB scratch buffers used for content-store I/O,
// keeping heap pressure low when multiple layers are processed concurrently.
var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 1<<20) // 1 MiB
		return &buf
	},
}

// ─── Options ──────────────────────────────────────────────────────────────────

// PackOption configures SOCI index generation behaviour per ConvertHookFunc
// call.
type PackOption struct {
	// SpanSize is the zTOC checkpoint granularity in bytes.
	// 0 uses the soci-snapshotter default (4 MiB).
	SpanSize int64

	// MinLayerSize skips zTOC generation for layers smaller than this threshold.
	// 0 uses the soci-snapshotter default (10 MiB).
	MinLayerSize int64

	// BuildToolIdentifier annotates the SOCI Index manifest with the name and
	// version of the tool that produced it.
	BuildToolIdentifier string

	// GCRoot controls whether a containerd GC root label is placed on the
	// SOCI Index blob so that it is retained for the lifetime of this manifest.
	GCRoot bool
}

// ─── Hooks ────────────────────────────────────────────────────────────────────

// LayerConvertHook carries the reactive lifecycle callbacks for the layer pass.
//
// SOCI does not rewrite layer blobs, so the hook is the primary mechanism for
// the snapshot-ingestion orchestrator to track bytes read and signal progress.
//
// Both fields are optional; nil values are silently skipped.
//
// Synchronous contract (identical to nydus / stargz / overlaybd hooks):
//   - LayerReader is called synchronously on the calling goroutine.
//   - Done is always deferred, once per conversion attempt, regardless of
//     success or failure.
type LayerConvertHook struct {
	// LayerReader is invoked with a SectionReader over the raw source blob so
	// the pipeline can observe bytes without modifying them.
	LayerReader converter.LayerReaderFunc

	// Done is a deferred completion callback, typically used to signal the
	// pipeline that this layer slot is free.
	Done func(ctx context.Context)
}

// ─── Merge options ────────────────────────────────────────────────────────────

// MergeOption configures the manifest-level ConvertHookFunc.
type MergeOption struct {
	// OCI forces the resulting manifest to use OCI media types even when the
	// source uses Docker schema2 media types.
	OCI bool

	// MergeManifest unwraps a single-entry index into a bare manifest
	// (consistent with nydus / stargz / overlaybd converters).
	MergeManifest bool

	// WithReferrer sets manifest.Subject to the original source manifest
	// descriptor, enabling the OCI referrers API for the converted image.
	WithReferrer bool
}

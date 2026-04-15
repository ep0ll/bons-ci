package stargz

import (
	"context"
	"sync"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/stargz-snapshotter/estargz"
)

// ─── Annotation keys ──────────────────────────────────────────────────────────
//
// These match the constants defined by the upstream estargz and containerd
// ecosystems so that any downstream consumer (snapshotter, runtime) can
// interpret the metadata without a bespoke convention.

const (
	// LayerAnnotationUncompressed is the OCI DiffID annotation. It holds the
	// digest of the *uncompressed* layer content so that runtimes can compute
	// the chain-ID without decompressing the blob.
	LayerAnnotationUncompressed = "containerd.io/uncompressed"

	// LayerAnnotationTOCDigest is the annotation that carries the digest of the
	// eStargz table-of-contents JSON stream, enabling fast random-access of
	// individual files without fetching the entire layer.
	LayerAnnotationTOCDigest = estargz.TOCJSONDigestAnnotation // "containerd.io/snapshot/stargz/toc.digest"

	// LayerAnnotationUncompressedSize carries the uncompressed byte-count for
	// containers/storage lazy-pulling support.
	LayerAnnotationUncompressedSize = estargz.StoreUncompressedSizeAnnotation // "io.containers.estargz.uncompressed-size"

	// LayerAnnotationStargz is a boolean flag on the content-store Info labels
	// that records whether a blob has already been converted to eStargz format.
	// Used as a remote-cache fast-path: if set, conversion is a no-op.
	LayerAnnotationStargz = "containerd.io/snapshot/stargz"

	// configGCLabelKey is the containerd GC label key that pins the image config
	// to the manifest so the GC doesn't reclaim it independently.
	configGCLabelKey = "containerd.io/gc.ref.content.config"
)

// ─── Buffer pool ──────────────────────────────────────────────────────────────

// bufPool recycles 1 MiB I/O scratch buffers used during content-store writes,
// keeping heap allocations low when many layers are converted concurrently.
var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 1<<20) // 1 MiB
		return &buf
	},
}

// ─── Options ──────────────────────────────────────────────────────────────────

// PackOption configures per-layer eStargz conversion.
type PackOption struct {
	// ChunkSize is the granularity (in bytes) at which the blob is divided into
	// independently-seekable gzip streams. Smaller chunks improve random-access
	// at the cost of a larger TOC.  0 uses the estargz default (~4 MiB).
	ChunkSize int

	// MinChunkSize enables "merged chunk" mode: consecutive entries whose total
	// uncompressed size is below this threshold are packed into a single gzip
	// stream, reducing TOC overhead for images with many tiny files.
	MinChunkSize int

	// CompressionLevel is the gzip compression level for the eStargz payload.
	// 0 uses gzip.BestCompression (the estargz default).
	CompressionLevel int

	// PrioritizedFiles is an ordered list of absolute-or-root-relative paths
	// that should appear at the start of the eStargz blob and be prefetched
	// when the container starts.  The resulting blob contains a
	// ".prefetch.landmark" entry that marks the prefetch boundary.
	PrioritizedFiles []string

	// Compression is an alternative compression algorithm (e.g., zstd-chunked).
	// When non-nil, CompressionLevel is ignored.
	Compression estargz.Compression
}

// MergeOption configures the manifest-level ConvertHookFunc.
type MergeOption struct {
	// OCI forces the converted manifest to use OCI media types even when the
	// source manifest uses Docker schema2 media types.
	OCI bool

	// MergeManifest collapses a multi-platform index into a single manifest
	// when the index contains only one entry after conversion.
	MergeManifest bool

	// WithReferrer sets the `subject` field of the converted manifest to the
	// original source manifest descriptor, creating an OCI referrer chain that
	// registries (v1.1+) can query.
	WithReferrer bool
}

// ─── Hooks ────────────────────────────────────────────────────────────────────

// LayerConvertHook carries the reactive lifecycle callbacks that integrate
// eStargz layer conversion into a broader snapshot-ingestion pipeline.
//
// Both fields are optional; nil values are silently skipped.
//
// Synchronous contract:
//   - LayerReader is called synchronously on the calling goroutine **before**
//     estargz.Build begins writing.  Any pipeline event sent inside the hook
//     completes before LayerConvertFunc returns.
//   - Done is always called via defer, once per conversion attempt — whether it
//     succeeds or fails.
type LayerConvertHook struct {
	// LayerReader is invoked with a SectionReader over the raw source blob
	// (before eStargz recompression) and the content store / descriptor.
	LayerReader converter.LayerReaderFunc

	// Done is a deferred completion callback, typically used to signal the
	// pipeline that this conversion slot is free.
	Done func(ctx context.Context)
}

package overlaybd

import (
	"context"
	"sync"

	"github.com/bons/bons-ci/core/images/converter"
	aciLabel "github.com/containerd/accelerated-container-image/pkg/label"
)

// ─── Annotation keys ──────────────────────────────────────────────────────────
//
// All constants below are aliases of the official accelerated-container-image
// label package to guarantee bit-for-bit compatibility with the snapshotter
// runtime that reads these values.

const (
	// LayerAnnotationOverlayBDDigest is the canonical OCI layer annotation
	// recording the digest of the OverlayBD blob.
	LayerAnnotationOverlayBDDigest = aciLabel.OverlayBDBlobDigest // "containerd.io/snapshot/overlaybd/blob-digest"

	// LayerAnnotationOverlayBDSize is the OCI layer annotation recording the
	// byte-count of the OverlayBD blob.
	LayerAnnotationOverlayBDSize = aciLabel.OverlayBDBlobSize // "containerd.io/snapshot/overlaybd/blob-size"

	// LayerAnnotationOverlayBDFsType is the OCI layer annotation recording the
	// filesystem type (e.g., "ext4", "erofs") of the OverlayBD blob.
	LayerAnnotationOverlayBDFsType = aciLabel.OverlayBDBlobFsType // "containerd.io/snapshot/overlaybd/blob-fs-type"

	// LayerAnnotationAcceleration is set to "yes" on an acceleration layer —
	// a top-most read-only layer that contains pre-warmed file-access metadata
	// to speed up container startup.
	LayerAnnotationAcceleration = aciLabel.AccelerationLayer // "containerd.io/snapshot/overlaybd/acceleration-layer"

	// LayerAnnotationUncompressed carries the OCI DiffID (uncompressed digest)
	// for config.RootFS.DiffIDs reconciliation.
	LayerAnnotationUncompressed = "containerd.io/uncompressed"

	// configGCLabelKey is the containerd GC root label that pins the image
	// config blob to its manifest.
	configGCLabelKey = "containerd.io/gc.ref.content.config"
)

// ─── Buffer pool ──────────────────────────────────────────────────────────────

// bufPool recycles 1 MiB scratch buffers for tar-pack I/O, keeping heap
// pressure negligible when many layers are converted in parallel.
var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 1<<20) // 1 MiB
		return &buf
	},
}

// ─── Options ──────────────────────────────────────────────────────────────────

// ZFileConfig mirrors the upstream overlaybd ZFile compression configuration
// used when creating the commit blob.
type ZFileConfig struct {
	// Algorithm is the block-level compression algorithm (e.g. "lz4", "zstd").
	Algorithm string `json:"algorithm,omitempty"`
	// BlockSize is the compression block size in bytes.
	BlockSize int `json:"blockSize,omitempty"`
}

// PackOption configures per-layer OverlayBD conversion.
type PackOption struct {
	// FsType is the target filesystem type ("ext4" by default).
	// Supported values depend on what the overlaybd tools were compiled with.
	FsType string

	// IsAccelLayer marks this layer as an acceleration layer (pre-warmed
	// file-access metadata).  Sets LayerAnnotationAcceleration = "yes".
	IsAccelLayer bool

	// Vsize is the virtual disk size (in GiB) for the overlaybd block device.
	// 0 uses the overlaybd default (256 GiB).
	Vsize int

	// ZFile holds the compression settings passed on to the overlaybd-commit
	// binary via the config.json.
	ZFile ZFileConfig
}

// MergeOption configures the manifest-level ConvertHookFunc.
type MergeOption struct {
	// OCI forces OCI media types in the converted manifest even when the source
	// uses Docker schema2 media types.
	OCI bool

	// MergeManifest collapses a single-entry index into a bare manifest.
	MergeManifest bool

	// WithReferrer sets manifest.Subject to the original source descriptor,
	// enabling the OCI referrers API for the converted image.
	WithReferrer bool
}

// ─── Hook ─────────────────────────────────────────────────────────────────────

// LayerConvertHook carries the reactive lifecycle callbacks that integrate
// OverlayBD layer conversion into the snapshot-ingestion pipeline.
//
// Both fields are optional; nil values are silently skipped.
//
// Synchronous contract (same as nydus/stargz hooks):
//   - LayerReader is called synchronously *before* the overlaybd CLI tools run,
//     giving the pipeline a chance to observe the raw source bytes.
//   - Done is always deferred, regardless of success or failure.
type LayerConvertHook struct {
	// LayerReader is invoked with a SectionReader over the raw source blob.
	LayerReader converter.LayerReaderFunc

	// Done is a deferred completion callback signalling conversion completion.
	Done func(ctx context.Context)
}

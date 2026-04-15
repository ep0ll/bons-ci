package overlaybd

import (
	"context"

	"github.com/bons/bons-ci/core/images/converter"
)

const (
	LayerAnnotationOverlayBDDigest = "containerd.io/snapshot/overlaybd/blob.digest"
	LayerAnnotationOverlayBDSize   = "containerd.io/snapshot/overlaybd/blob.size"
	LayerAnnotationOverlayBDFsType = "containerd.io/snapshot/overlaybd/fsType"
	LayerAnnotationUncompressed    = "containerd.io/uncompressed"
	LayerAnnotationAcceleration    = "containerd.io/accelerationLayer"

	configGCLabelKey = "containerd.io/gc.ref.content.config"
)

// PackOption holds the configurations for overlaybd layer conversion.
type PackOption struct {
	FsType       string // defaults to "ext4"
	IsAccelLayer bool
}

// LayerConvertHook aligns with the reactive ingestion pipeline event hooks.
type LayerConvertHook struct {
	LayerReader converter.LayerReaderFunc
	Done        func(ctx context.Context)
}

// MergeOption defines options for manifest conversion hooks.
type MergeOption struct {
	OCI           bool
	MergeManifest bool
}

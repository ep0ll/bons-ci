package overlaybd

import (
	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// IsOverlayBDBlob reports whether desc carries the OverlayBD digest annotation,
// indicating the blob has already been produced by LayerConvertFunc.
//
// Use this as a pre-flight guard on the descriptor side; if you only have a
// content.Info use IsOverlayBDLayer instead.
func IsOverlayBDBlob(desc ocispec.Descriptor) bool {
	if desc.Annotations == nil {
		return false
	}
	return desc.Annotations[LayerAnnotationOverlayBDDigest] != ""
}

// IsOverlayBDLayer reports whether the blob identified by info has already been
// converted to an OverlayBD block image, using the label persisted by
// makeBlobDesc.
//
// Exposing this check lets upstream orchestrators inspect conversion state
// without duplicating the label key.
func IsOverlayBDLayer(info content.Info) bool {
	return info.Labels[LayerAnnotationOverlayBDDigest] != ""
}

// IsAccelerationLayer reports whether desc is marked as an OverlayBD
// acceleration layer (pre-warmed file-access metadata).
func IsAccelerationLayer(desc ocispec.Descriptor) bool {
	if desc.Annotations == nil {
		return false
	}
	return desc.Annotations[LayerAnnotationAcceleration] == "yes"
}

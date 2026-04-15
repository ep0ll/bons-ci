package stargz

import (
	"github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// IsStargzBlob reports whether desc carries eStargz annotations, indicating
// the blob has already been converted.  Use this as a fast pre-flight check
// before calling LayerConvertFunc when descriptors are available.
//
// If you only have a content.Info (from cs.Info), prefer IsStargzLayer, which
// reads the persisted label instead of the descriptor annotations.
func IsStargzBlob(desc ocispec.Descriptor) bool {
	if desc.Annotations == nil {
		return false
	}
	return desc.Annotations[LayerAnnotationStargz] == "true"
}

// IsStargzLayer reports whether the blob identified by info has already been
// converted to eStargz format, using the label persisted by makeBlobDesc.
//
// This is the same check performed inside LayerConvertFunc; exposing it lets
// callers schedule conversion work without opening a second code path.
func IsStargzLayer(info content.Info) bool {
	return info.Labels[LayerAnnotationStargz] == "true"
}

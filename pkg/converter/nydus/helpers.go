package nydus

import (
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// isNydusImage checks if the last layer is nydus bootstrap,
// so that we can ensure it is a nydus image.
func isNydusImage(manifest *ocispec.Manifest) bool {
	layers := manifest.Layers
	if len(layers) != 0 {
		desc := layers[len(layers)-1]
		if IsNydusBootstrap(desc) {
			return true
		}
	}
	return false
}

// IsNydusBootstrap returns true when the specified descriptor is nydus bootstrap layer.
func IsNydusBootstrap(desc ocispec.Descriptor) bool {
	if desc.Annotations == nil {
		return false
	}

	_, hasAnno := desc.Annotations[nydusConv.LayerAnnotationNydusBootstrap]
	return hasAnno
}

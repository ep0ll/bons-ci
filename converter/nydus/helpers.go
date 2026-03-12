package nydus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/opencontainers/go-digest"
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

// Copied from containerd/containerd project, copyright The containerd Authors.
// https://github.com/containerd/containerd/blob/4902059cb554f4f06a8d06a12134c17117809f4e/images/converter/default.go#L385
func readJSON(ctx context.Context, cs content.Store, x interface{}, desc ocispec.Descriptor) (map[string]string, error) {
	info, err := cs.Info(ctx, desc.Digest)
	if err != nil {
		return nil, err
	}
	labels := info.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	b, err := content.ReadBlob(ctx, cs, desc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, x); err != nil {
		return nil, err
	}
	return labels, nil
}

// Copied from containerd/containerd project, copyright The containerd Authors.
// https://github.com/containerd/containerd/blob/4902059cb554f4f06a8d06a12134c17117809f4e/images/converter/default.go#L401
func writeJSON(ctx context.Context, cs content.Store, x interface{}, oldDesc ocispec.Descriptor, labels map[string]string) (*ocispec.Descriptor, error) {
	b, err := json.Marshal(x)
	if err != nil {
		return nil, err
	}
	dgst := digest.SHA256.FromBytes(b)
	ref := fmt.Sprintf("converter-write-json-%s", dgst.String())
	w, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
	if err != nil {
		return nil, err
	}
	if err := content.Copy(ctx, w, bytes.NewReader(b), int64(len(b)), dgst, content.WithLabels(labels)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	newDesc := oldDesc
	newDesc.Size = int64(len(b))
	newDesc.Digest = dgst
	return &newDesc, nil
}

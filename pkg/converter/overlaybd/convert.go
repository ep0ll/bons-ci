package overlaybd

import (
	"context"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ConvertHookFunc configures manifestation replacements matching the target
// layer bindings produced natively by overlaybd.
func ConvertHookFunc(opt MergeOption) converter.ConvertHookFunc {
	return func(ctx context.Context, cs content.Store, orgDesc ocispec.Descriptor, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
		if newDesc == nil {
			return &orgDesc, nil
		}
		switch {
		case images.IsIndexType(newDesc.MediaType):
			return convertIndex(ctx, cs, newDesc)
		case images.IsManifestType(newDesc.MediaType):
			return convertManifest(ctx, cs, orgDesc, newDesc, opt)
		default:
			return newDesc, nil
		}
	}
}

func convertIndex(ctx context.Context, cs content.Store, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
	var index ocispec.Index
	_, err := converter.ReadJSON(ctx, cs, &index, *newDesc)
	if err != nil {
		return nil, errors.Wrap(err, "read index JSON structures")
	}

	if len(index.Manifests) == 1 {
		return &index.Manifests[0], nil
	}
	return newDesc, nil
}

func convertManifest(ctx context.Context, cs content.Store, oldDesc ocispec.Descriptor, newDesc *ocispec.Descriptor, opt MergeOption) (*ocispec.Descriptor, error) {
	var manifest ocispec.Manifest
	manifestDesc := *newDesc
	manifestLabels, err := converter.ReadJSON(ctx, cs, &manifest, manifestDesc)
	if err != nil {
		return nil, errors.Wrap(err, "read manifest JSON layout payloads")
	}

	var config ocispec.Image
	configLabels, err := converter.ReadJSON(ctx, cs, &config, manifest.Config)
	if err != nil {
		return nil, errors.Wrap(err, "extract underlying config formats")
	}

	if len(manifest.Layers) != len(config.RootFS.DiffIDs) {
		return nil, errors.Errorf("layer mismatch mappings detected: %d configs vs %d physical layers", len(config.RootFS.DiffIDs), len(manifest.Layers))
	}

	for i, layer := range manifest.Layers {
		if uncompressed, ok := layer.Annotations[LayerAnnotationUncompressed]; ok {
			config.RootFS.DiffIDs[i] = digest.Digest(uncompressed)
			delete(manifest.Layers[i].Annotations, LayerAnnotationUncompressed)
		}
	}

	newConfigDesc, err := converter.WriteJSON(ctx, cs, config, manifest.Config, configLabels)
	if err != nil {
		return nil, errors.Wrap(err, "write explicit config payload")
	}

	manifestLabels[configGCLabelKey] = newConfigDesc.Digest.String()
	manifest.Config = *newConfigDesc

	newManifestDesc, err := converter.WriteJSON(ctx, cs, manifest, manifestDesc, manifestLabels)
	if err != nil {
		return nil, errors.Wrap(err, "remap fully constructed manifest bindings")
	}

	return newManifestDesc, nil
}

package overlaybd

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ConvertHookFunc returns a ConvertHookFunc that fixes up the image manifest
// and config after all OCI layers have been converted to OverlayBD blobs.
//
// Descriptor routing:
//   - Index    → unwrap single-manifests list to bare manifest.
//   - Manifest → reconcile config DiffIDs from LayerAnnotationUncompressed,
//     write GC labels for all layers and the config,
//     optionally wire the OCI referrer Subject field.
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

// convertIndex flattens a single-entry manifest list into a bare manifest
// descriptor, matching the behaviour of the nydus and stargz converters.
func convertIndex(ctx context.Context, cs content.Store, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
	var index ocispec.Index
	if _, err := converter.ReadJSON(ctx, cs, &index, *newDesc); err != nil {
		return nil, errors.Wrap(err, "read index JSON")
	}
	if len(index.Manifests) == 1 {
		return &index.Manifests[0], nil
	}
	return newDesc, nil
}

// convertManifest reconciles the image manifest and config after OverlayBD
// layer conversion.  It:
//
//  1. Reads the manifest and config from the content store.
//  2. Validates layer/DiffID count consistency.
//  3. Folds the LayerAnnotationUncompressed annotation from each converted
//     layer into config.RootFS.DiffIDs and removes the transient annotation.
//  4. Writes GC-pinning labels for every layer and the config on the manifest.
//  5. Optionally wires manifest.Subject (OCI referrers API).
//  6. Writes the updated config and manifest back to the content store.
func convertManifest(
	ctx context.Context,
	cs content.Store,
	oldDesc ocispec.Descriptor,
	newDesc *ocispec.Descriptor,
	opt MergeOption,
) (*ocispec.Descriptor, error) {

	var manifest ocispec.Manifest
	manifestDesc := *newDesc
	manifestLabels, err := converter.ReadJSON(ctx, cs, &manifest, manifestDesc)
	if err != nil {
		return nil, errors.Wrap(err, "read manifest JSON")
	}
	if manifestLabels == nil {
		manifestLabels = make(map[string]string)
	}

	var config ocispec.Image
	configLabels, err := converter.ReadJSON(ctx, cs, &config, manifest.Config)
	if err != nil {
		return nil, errors.Wrap(err, "read image config JSON")
	}

	if len(manifest.Layers) != len(config.RootFS.DiffIDs) {
		return nil, errors.Errorf(
			"layer/DiffID count mismatch: manifest has %d layers, config has %d DiffIDs",
			len(manifest.Layers), len(config.RootFS.DiffIDs),
		)
	}

	// ── 1. Reconcile DiffIDs ──────────────────────────────────────────────────
	// LayerConvertFunc stamps LayerAnnotationUncompressed with the block-blob
	// digest (self-referential for OverlayBD) so the config stays consistent.
	for i, layer := range manifest.Layers {
		if uncompressed, ok := layer.Annotations[LayerAnnotationUncompressed]; ok {
			config.RootFS.DiffIDs[i] = digest.Digest(uncompressed)
			delete(manifest.Layers[i].Annotations, LayerAnnotationUncompressed)
		}
		// GC label: prevent the GC from reclaiming the layer blob independently.
		manifestLabels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = layer.Digest.String()
	}

	// ── 2. Write updated config ───────────────────────────────────────────────
	newConfigDesc, err := converter.WriteJSON(ctx, cs, config, manifest.Config, configLabels)
	if err != nil {
		return nil, errors.Wrap(err, "write updated image config")
	}
	manifestLabels[configGCLabelKey] = newConfigDesc.Digest.String()
	manifest.Config = *newConfigDesc

	// ── 3. OCI referrer subject ───────────────────────────────────────────────
	if opt.WithReferrer {
		subject := oldDesc
		subject.Platform = nil
		manifest.Subject = &subject
	}

	// ── 4. Write updated manifest ─────────────────────────────────────────────
	newManifestDesc, err := converter.WriteJSON(ctx, cs, manifest, manifestDesc, manifestLabels)
	if err != nil {
		return nil, errors.Wrap(err, "write updated manifest")
	}

	return newManifestDesc, nil
}

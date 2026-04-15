package stargz

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
// and config after all layers have been converted to eStargz blobs.
//
// It handles two descriptor types:
//   - Index   — flattens a single-entry manifest list to a bare manifest
//               (identical behaviour to the nydus converter).
//   - Manifest — reconciles RootFS.DiffIDs from the LayerAnnotationUncompressed
//               annotations written by LayerConvertFunc, writes the TOC-digest
//               GC labels, and optionally sets the OCI referrer subject field.
func ConvertHookFunc(opt MergeOption) converter.ConvertHookFunc {
	return func(ctx context.Context, cs content.Store, orgDesc ocispec.Descriptor, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
		// When the conversion produced no new blob (e.g. the layer was already
		// eStargz), newDesc is nil — return the original unchanged.
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

// convertIndex flattens a manifest index that contains exactly one manifest
// into a bare manifest descriptor.  This mirrors the nydus and stargz upstream
// behaviour: if a multi-platform image was converted but only one platform
// remains, return the manifest directly rather than a single-entry index.
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

// convertManifest reconciles the image manifest and config after eStargz layer
// conversion.  It:
//
//  1. Reads the manifest and config from the content store.
//  2. Reconciles config.RootFS.DiffIDs from the LayerAnnotationUncompressed
//     annotations stamped on each converted layer descriptor.
//  3. Writes the GC label for the TOC digest on each layer.
//  4. Optionally sets manifest.Subject (OCI referrers API).
//  5. Writes the updated config and manifest back to the content store.
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

	// ── Guard: layers and DiffIDs must be consistent ──────────────────────────
	if len(manifest.Layers) != len(config.RootFS.DiffIDs) {
		return nil, errors.Errorf(
			"layer/DiffID count mismatch: manifest has %d layers, config has %d DiffIDs",
			len(manifest.Layers), len(config.RootFS.DiffIDs),
		)
	}

	// ── 1. Reconcile DiffIDs and GC labels ────────────────────────────────────
	// For each converted layer the LayerConvertFunc wrote:
	//   - LayerAnnotationUncompressed → the diffID (uncompressed digest)
	//   - LayerAnnotationTOCDigest    → the eStargz TOC digest
	//
	// We fold these into the config and set GC labels on the manifest, then
	// strip them from the layer annotations to produce a clean OCI manifest.
	for i, layer := range manifest.Layers {
		if uncompressed, ok := layer.Annotations[LayerAnnotationUncompressed]; ok {
			config.RootFS.DiffIDs[i] = digest.Digest(uncompressed)
			delete(manifest.Layers[i].Annotations, LayerAnnotationUncompressed)
		}
		if tocDigest, ok := layer.Annotations[LayerAnnotationTOCDigest]; ok {
			// GC label: pin the TOC blob to this manifest so the containerd GC
			// does not reclaim it while the manifest lives.
			manifestLabels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = layer.Digest.String()
			_ = tocDigest // retained in the annotation; label is the digest above
		}
	}

	// ── 2. Write updated config ───────────────────────────────────────────────
	newConfigDesc, err := converter.WriteJSON(ctx, cs, config, manifest.Config, configLabels)
	if err != nil {
		return nil, errors.Wrap(err, "write updated image config")
	}
	manifestLabels[configGCLabelKey] = newConfigDesc.Digest.String()
	manifest.Config = *newConfigDesc

	// ── 3. OCI referrer subject (optional) ───────────────────────────────────
	if opt.WithReferrer {
		// Associate this converted manifest with the original source manifest.
		// Registries supporting the OCI referrers API (v1.1+) surface this
		// relationship via GET /v2/<name>/referrers/<digest>.
		subject := oldDesc
		subject.Platform = nil // platform field is not relevant on the subject
		manifest.Subject = &subject
	}

	// ── 4. Write updated manifest ─────────────────────────────────────────────
	newManifestDesc, err := converter.WriteJSON(ctx, cs, manifest, manifestDesc, manifestLabels)
	if err != nil {
		return nil, errors.Wrap(err, "write updated manifest")
	}

	return newManifestDesc, nil
}

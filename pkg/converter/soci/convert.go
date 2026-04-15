package soci

import (
	"context"
	"fmt"

	sociPkg "github.com/awslabs/soci-snapshotter/soci"
	sociStore "github.com/awslabs/soci-snapshotter/soci/store"
	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	pkgerrors "github.com/pkg/errors"
)

// ConvertHookFunc returns a ConvertHookFunc that attaches SOCI zTOC side-cars
// to an already-converted (or unchanged) image manifest.
//
// # SOCI conversion model
//
// Unlike nydus, stargz, and overlaybd, SOCI does NOT rewrite layer blobs.
// The layer pass (LayerConvertFunc) is effectively a pass-through.  All
// heavyweight work happens here in the ConvertHookFunc after the layer pass
// completes:
//
//  1. For each layer, the upstream IndexBuilder (from soci-snapshotter) builds
//     a zTOC and stores it in blobStore.
//  2. A SOCI Index manifest (v1) is created and pushed to blobStore.
//  3. The image manifest is annotated with the SOCI Index digest so that the
//     soci-snapshotter runtime can locate the zTOCs at pull time.
//
// # BlobStore
//
// SOCI requires a store.Store (the SOCI blob-store interface) rather than a
// plain content.Store.  A default SociStore backed by the standard on-disk
// path is created automatically.
//
// # ErrEmptyIndex
//
// If all layers are below MinLayerSize or use an unsupported compression
// format, IndexBuilder.Build returns ErrEmptyIndex.  This is treated as a
// graceful skip: the manifest is returned unmodified rather than failing the
// conversion.
func ConvertHookFunc(opt PackOption, mergeOpt MergeOption, builderOpts ...sociPkg.BuilderOption) converter.ConvertHookFunc {
	return func(ctx context.Context, cs content.Store, orgDesc ocispec.Descriptor, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
		// Normalise: when no layer conversion occurred newDesc is nil —
		// route on the original descriptor in that case.
		targetDesc := newDesc
		if targetDesc == nil {
			targetDesc = &orgDesc
		}

		switch {
		case images.IsIndexType(targetDesc.MediaType):
			return convertIndex(ctx, cs, targetDesc)
		case images.IsManifestType(targetDesc.MediaType):
			return convertManifest(ctx, cs, orgDesc, targetDesc, opt, mergeOpt, builderOpts...)
		default:
			return targetDesc, nil
		}
	}
}

// convertIndex flattens a single-entry manifest list into a bare manifest
// descriptor, matching the behaviour of the nydus / stargz / overlaybd
// converters for consistency.
func convertIndex(ctx context.Context, cs content.Store, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
	var index ocispec.Index
	if _, err := converter.ReadJSON(ctx, cs, &index, *newDesc); err != nil {
		return nil, pkgerrors.Wrap(err, "soci convert: read index JSON")
	}
	if len(index.Manifests) == 1 {
		return &index.Manifests[0], nil
	}
	return newDesc, nil
}

// convertManifest builds the SOCI Index and zTOC side-cars for manifestDesc,
// annotates the manifest, and writes the updated manifest to the content store.
func convertManifest(
	ctx context.Context,
	cs content.Store,
	orgDesc ocispec.Descriptor,
	manifestDesc *ocispec.Descriptor,
	opt PackOption,
	mergeOpt MergeOption,
	builderOpts ...sociPkg.BuilderOption,
) (*ocispec.Descriptor, error) {
	var manifest ocispec.Manifest
	manifestLabels, err := converter.ReadJSON(ctx, cs, &manifest, *manifestDesc)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "soci convert: read manifest JSON")
	}
	if manifestLabels == nil {
		manifestLabels = make(map[string]string)
	}

	var config ocispec.Image
	configLabels, err := converter.ReadJSON(ctx, cs, &config, manifest.Config)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "soci convert: read image config JSON")
	}

	// ── 1. Resolve blob store ──────────────────────────────────────────────
	// The SOCI IndexBuilder requires a soci/store.Store (for pushing zTOC
	// blobs and the SOCI Index manifest).  Default to the local SociStore so
	// the caller does not have to wire anything up for the common case.
	bs, err := resolveBlobStore(opt)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "soci convert: resolve blob store")
	}

	// ── 2. Apply PackOption as BuilderOptions ──────────────────────────────
	if opt.SpanSize > 0 {
		builderOpts = append(builderOpts, sociPkg.WithSpanSize(opt.SpanSize))
	}
	if opt.MinLayerSize > 0 {
		builderOpts = append(builderOpts, sociPkg.WithMinLayerSize(opt.MinLayerSize))
	}
	if opt.BuildToolIdentifier != "" {
		builderOpts = append(builderOpts, sociPkg.WithBuildToolIdentifier(opt.BuildToolIdentifier))
	}

	builder, err := sociPkg.NewIndexBuilder(cs, bs, builderOpts...)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "soci convert: create index builder")
	}

	// ── 3. Build SOCI Index ────────────────────────────────────────────────
	// The builder expects an images.Image whose Target points at the manifest.
	sociImg := images.Image{
		Target: *manifestDesc,
	}

	indexMeta, buildErr := builder.Build(ctx, sociImg)
	if buildErr != nil {
		// ErrEmptyIndex: all layers were below minLayerSize or used an
		// unsupported compression format.  Gracefully return the manifest
		// unchanged — SOCI acceleration is simply unavailable for this image.
		if isErrEmptyIndex(buildErr) {
			return manifestDesc, nil
		}
		return nil, pkgerrors.Wrap(buildErr, "soci convert: build SOCI index")
	}

	// ── 4. Annotate manifest with SOCI Index digest ────────────────────────
	if manifest.Annotations == nil {
		manifest.Annotations = make(map[string]string, 2)
	}
	manifest.Annotations[ImageAnnotationSociIndexDigest] = indexMeta.Desc.Digest.String()

	// ── 5. GC root: pin SOCI Index to this manifest ───────────────────────
	if opt.GCRoot {
		manifestLabels["containerd.io/gc.ref.content.soci.index"] = indexMeta.Desc.Digest.String()
	}

	// ── 6. Write updated config (GC pinning) ──────────────────────────────
	newConfigDesc, err := converter.WriteJSON(ctx, cs, config, manifest.Config, configLabels)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "soci convert: write updated image config")
	}
	manifestLabels[configGCLabelKey] = newConfigDesc.Digest.String()
	manifest.Config = *newConfigDesc

	// ── 7. OCI Referrers API (optional) ───────────────────────────────────
	if mergeOpt.WithReferrer {
		subject := orgDesc
		subject.Platform = nil // platform field is not valid on subject descriptors
		manifest.Subject = &subject
	}

	// ── 8. Write updated manifest ─────────────────────────────────────────
	newManifestDesc, err := converter.WriteJSON(ctx, cs, manifest, *manifestDesc, manifestLabels)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "soci convert: write updated manifest")
	}

	return newManifestDesc, nil
}

// resolveBlobStore returns the blob store to inject into the SOCI IndexBuilder.
// Uses a default local SociStore (state written to /var/lib/soci-snapshotter-grpc/content).
func resolveBlobStore(_ PackOption) (sociStore.Store, error) {
	bs, err := sociStore.NewSociStore()
	if err != nil {
		return nil, fmt.Errorf("create default soci blob store: %w", err)
	}
	return bs, nil
}

// isErrEmptyIndex checks whether an error from IndexBuilder.Build indicates
// that no ztocs could be generated (all layers skipped).  It wraps the
// upstream package-level sentinel so callers don't need to import soci directly.
func isErrEmptyIndex(err error) bool {
	// sociPkg.ErrEmptyIndex is a non-wrapping sentinel from the upstream package.
	return err == sociPkg.ErrEmptyIndex || pkgerrors.Is(err, sociPkg.ErrEmptyIndex)
}

package soci

import (
	"context"

	sociPkg "github.com/awslabs/soci-snapshotter/soci"
	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ConvertHookFunc returns a ConvertHookFunc that manages the heavy lifting for
// SOCI: building the zTOC side-cars for applicable layers and constructing the
// SOCI Index manifest.
//
// Because SOCI delegates conversion out of the layer-iteration pass and handles
// it entirely in this manifest hook, this function creates a soci.IndexBuilder,
// executes it across the entire unmodified OCI image layers, and pins the resulting
// SOCI index to the manifest annotations.
func ConvertHookFunc(opt PackOption, mergeOpt MergeOption, builderOpts ...sociPkg.BuilderOption) converter.ConvertHookFunc {
	return func(ctx context.Context, cs content.Store, orgDesc ocispec.Descriptor, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
		targetDesc := newDesc
		if targetDesc == nil {
			targetDesc = &orgDesc
		}

		switch {
		case images.IsIndexType(targetDesc.MediaType):
			return convertIndex(ctx, cs, targetDesc)
		case images.IsManifestType(targetDesc.MediaType):
			return convertManifest(ctx, cs, targetDesc, opt, mergeOpt, builderOpts...)
		default:
			return targetDesc, nil
		}
	}
}

func convertIndex(ctx context.Context, cs content.Store, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
	var index ocispec.Index
	if _, err := converter.ReadJSON(ctx, cs, &index, *newDesc); err != nil {
		return nil, errors.Wrap(err, "soci convert: read index JSON")
	}
	if len(index.Manifests) == 1 {
		return &index.Manifests[0], nil
	}
	return newDesc, nil
}

func convertManifest(
	ctx context.Context,
	cs content.Store,
	manifestDesc *ocispec.Descriptor,
	opt PackOption,
	mergeOpt MergeOption,
	builderOpts ...sociPkg.BuilderOption,
) (*ocispec.Descriptor, error) {
	var manifest ocispec.Manifest
	manifestLabels, err := converter.ReadJSON(ctx, cs, &manifest, *manifestDesc)
	if err != nil {
		return nil, errors.Wrap(err, "soci convert: read manifest JSON")
	}

	var config ocispec.Image
	configLabels, err := converter.ReadJSON(ctx, cs, &config, manifest.Config)
	if err != nil {
		return nil, errors.Wrap(err, "soci convert: read image config JSON")
	}

	// 1. Build SOCI Index + zTOC side-cars using the AWS Labs builder.
	// We inject the original manifest descriptor as the Target.
	sociImg := images.Image{
		Target: *manifestDesc,
	}

	// If the user specified specific options on PackOption, map them.
	if opt.SpanSize > 0 {
		builderOpts = append(builderOpts, sociPkg.WithSpanSize(opt.SpanSize))
	}
	if opt.MinLayerSize > 0 {
		builderOpts = append(builderOpts, sociPkg.WithMinLayerSize(opt.MinLayerSize))
	}
	if opt.BuildToolIdentifier != "" {
		builderOpts = append(builderOpts, sociPkg.WithBuildToolIdentifier(opt.BuildToolIdentifier))
	}

	builder, err := sociPkg.NewIndexBuilder(cs, cs, builderOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "soci convert: create index builder")
	}

	index, err := builder.Build(ctx, sociImg)
	if err != nil {
		return nil, errors.Wrap(err, "soci convert: build soci index and ztocs")
	}

	// 2. Tie the SOCI index to the manifest via annotation.
	if manifest.Annotations == nil {
		manifest.Annotations = make(map[string]string)
	}
	manifest.Annotations[ImageAnnotationSociIndexDigest] = index.ImageIndex.Digest.String()
	
	// Set a GC root pinning the side-car index so containerd's garbage collector
	// retains it for the lifetime of this manifest.
	if opt.GCRoot {
		manifestLabels["containerd.io/gc.ref.content.soci.index"] = index.ImageIndex.Digest.String()
	}

	// 3. Write Config (GC pinning for normal conversion)
	newConfigDesc, err := converter.WriteJSON(ctx, cs, config, manifest.Config, configLabels)
	if err != nil {
		return nil, errors.Wrap(err, "soci convert: write updated image config")
	}
	manifestLabels[configGCLabelKey] = newConfigDesc.Digest.String()
	manifest.Config = *newConfigDesc

	// 4. OCI Subject (Referrers API)
	if mergeOpt.WithReferrer {
		subject := *manifestDesc
		subject.Platform = nil
		manifest.Subject = &subject
	}

	// 5. Write Updated Manifest
	newManifestDesc, err := converter.WriteJSON(ctx, cs, manifest, *manifestDesc, manifestLabels)
	if err != nil {
		return nil, errors.Wrap(err, "soci convert: write updated manifest")
	}

	return newManifestDesc, nil
}

package stargz

import (
	"context"
	"fmt"
	"io"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// LayerConvertFunc returns a ConvertFunc that unpacks an OCI layer
// and recompresses it effectively into an eStargz blob concurrently.
func LayerConvertFunc(opt PackOption, hook LayerConvertHook) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		if hook.Done != nil {
			defer hook.Done(ctx)
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Skip non-layer types
		if !images.IsLayerType(desc.MediaType) {
			return nil, nil
		}

		// Skip if already converted to stargz
		if desc.Annotations[LayerAnnotationStargz] == "true" {
			return nil, nil
		}

		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get source blob reader for %s", desc.Digest)
		}
		defer ra.Close()

		sr := io.NewSectionReader(ra, 0, ra.Size())

		// Trigger the pipeline hook before reading
		if hook.LayerReader != nil {
			if err := hook.LayerReader(ctx, sr, cs, desc); err != nil {
				return nil, errors.Wrap(err, "layer reader hook execution failed")
			}
		}

		// Map stargz build options
		var buildOpts []estargz.Option
		if opt.ChunkSize > 0 {
			buildOpts = append(buildOpts, estargz.WithChunkSize(opt.ChunkSize))
		}
		if opt.MinChunkSize > 0 {
			buildOpts = append(buildOpts, estargz.WithMinChunkSize(opt.MinChunkSize))
		}
		if opt.CompressionLevel != 0 {
			buildOpts = append(buildOpts, estargz.WithCompressionLevel(opt.CompressionLevel))
		}
		buildOpts = append(buildOpts, estargz.WithContext(ctx))

		// Process OCI tar stream and synchronously compress to stargz Blob
		blob, err := estargz.Build(sr, buildOpts...)
		if err != nil {
			return nil, errors.Wrap(err, "estargz build process failed")
		}
		defer blob.Close()

		ref := fmt.Sprintf("convert-stargz-from-%s", desc.Digest)
		dst, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
		if err != nil {
			return nil, errors.Wrap(err, "open blob target writer")
		}
		defer func() {
			dst.Close()
			_ = cs.Abort(context.Background(), ref)
		}()

		// Stream concurrently to content store
		if err := content.Copy(ctx, dst, blob, 0, ""); err != nil {
			return nil, errors.Wrap(err, "copy stargz blob payload to content store")
		}

		targetDigest := dst.Digest()

		uncompressedSize, err := blob.UncompressedSize()
		if err != nil {
			return nil, errors.Wrap(err, "fetch uncompressed size for estargz")
		}

		return makeBlobDesc(ctx, cs, desc, targetDigest, blob.DiffID(), blob.TOCDigest(), uncompressedSize)
	}
}

func makeBlobDesc(ctx context.Context, cs content.Store, orgDesc ocispec.Descriptor, targetDigest, diffID, tocDigest digest.Digest, uncompressedSize int64) (*ocispec.Descriptor, error) {
	targetInfo, err := cs.Info(ctx, targetDigest)
	if err != nil {
		return nil, errors.Wrapf(err, "get target blob info %s", targetDigest)
	}
	if targetInfo.Labels == nil {
		targetInfo.Labels = make(map[string]string)
	}

	targetInfo.Labels[labels.LabelUncompressed] = diffID.String()
	targetInfo.Labels[LayerAnnotationStargz] = "true"
	if _, err = cs.Update(ctx, targetInfo); err != nil {
		return nil, errors.Wrap(err, "update layer containerd.io properties")
	}

	// Preserve original media type (stargz uses standard gzip media type or backwards compatible type)
	mediaType := orgDesc.MediaType
	if mediaType == ocispec.MediaTypeImageLayer {
		mediaType = ocispec.MediaTypeImageLayerGzip
	}

	desc := ocispec.Descriptor{
		Digest:    targetDigest,
		Size:      targetInfo.Size,
		MediaType: mediaType,
		Annotations: map[string]string{
			LayerAnnotationUncompressed: diffID.String(),
			LayerAnnotationTOCDigest:    tocDigest.String(),
			LayerAnnotationStargz:       "true",
		},
	}

	return &desc, nil
}

package soci

import (
	"context"
	"io"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// LayerConvertFunc returns a ConvertFunc that integrates soci layer processing
// into the conversion pipeline.
//
// Unlike nydus, stargz, and overlaybd, the SOCI converter does NOT rewrite the
// layer blob itself. The layer stays completely identical bit-for-bit to the
// upstream. Instead, SOCI relies on a separate zTOC side-car generation, which
// is handled by the index builder in the ConvertHookFunc manifest hook.
//
// Therefore, this function serves purely to let the ingestion pipeline hook
// observe the layer bits via hook.LayerReader (allowing progress tracking,
// synchronous pre-processing, etc.) while returning nil, nil so the upstream
// converter driver knows the layer is unchanged.
func LayerConvertFunc(opt PackOption, hook LayerConvertHook) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		if hook.Done != nil {
			defer hook.Done(ctx)
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if !images.IsLayerType(desc.MediaType) {
			return nil, nil // Return nil, nil exactly as required for no-op/skip
		}

		// Fast-path: if there is no LayerReader hook to execute, we can return
		// immediately without opening a SectionReader over the content store.
		// A nil conversion return value instructs `converter.convertManifest` to
		// retain the original layer unmodified.
		if hook.LayerReader == nil {
			return nil, nil
		}

		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, errors.Wrapf(err, "soci convert: get source blob reader for %s", desc.Digest)
		}
		defer ra.Close()

		sr := io.NewSectionReader(ra, 0, ra.Size())
		if err := hook.LayerReader(ctx, sr, cs, desc); err != nil {
			return nil, errors.Wrap(err, "soci convert: layer reader hook failed")
		}

		// Return nil, nil: we do not rewrite the layer. This signals the driver
		// to simply retain the original descriptor in the converted manifest.
		return nil, nil
	}
}

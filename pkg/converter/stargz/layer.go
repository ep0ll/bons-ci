package stargz

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// LayerConvertFunc returns a ConvertFunc that rewrites one OCI/Docker layer
// blob as an eStargz blob.
//
// # Fast-paths
//
// 1. Non-layer blobs (configs, manifests) → return nil, nil (no-op).
// 2. Blobs already carrying LayerAnnotationStargz == "true" on their stored
//    Info → return nil, nil (idempotent; skip re-encoding).
//
// # Concurrency
//
// Each call is fully self-contained: it opens its own ReaderAt and Writer,
// pools a scratch buffer independently, and communicates with the pipeline
// exclusively via the synchronous LayerReader hook.  Multiple goroutines may
// call the returned function simultaneously without any shared mutable state.
func LayerConvertFunc(opt PackOption, hook LayerConvertHook) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		// Always invoke the completion hook, even on early returns.
		if hook.Done != nil {
			defer hook.Done(ctx)
		}

		// Respect caller cancellation immediately — avoids opening writers that
		// will never be committed.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// ── Fast-path 1: non-layer blob ───────────────────────────────────────
		if !images.IsLayerType(desc.MediaType) {
			return nil, nil
		}

		// ── Fast-path 2: already eStargz ──────────────────────────────────────
		// Check the label persisted by a previous makeBlobDesc call.  This
		// avoids re-reading and re-encoding an already-converted blob, making
		// repeated convert-on-pull calls O(1) for warm layers.
		info, err := cs.Info(ctx, desc.Digest)
		if err != nil {
			return nil, errors.Wrapf(err, "get blob info %s", desc.Digest)
		}
		if info.Labels[LayerAnnotationStargz] == "true" {
			return nil, nil
		}

		// ── Open source reader ────────────────────────────────────────────────
		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, errors.Wrapf(err, "get source blob reader for %s", desc.Digest)
		}
		defer ra.Close()

		sr := io.NewSectionReader(ra, 0, ra.Size())

		// ── Synchronous pipeline hook ─────────────────────────────────────────
		// Called before any recompression so the pipeline can observe the raw
		// layer bytes.  Any event sent inside returns before we proceed.
		if hook.LayerReader != nil {
			if err := hook.LayerReader(ctx, sr, cs, desc); err != nil {
				return nil, errors.Wrap(err, "layer reader hook failed")
			}
			// Reset section reader position after the hook may have consumed it.
			sr = io.NewSectionReader(ra, 0, ra.Size())
		}

		// ── Build eStargz blob ────────────────────────────────────────────────
		buildOpts := buildEstargzOptions(opt, ctx)
		blob, err := estargz.Build(sr, buildOpts...)
		if err != nil {
			return nil, errors.Wrap(err, "estargz build failed")
		}
		defer blob.Close()

		// ── Write to content store ────────────────────────────────────────────
		ref := fmt.Sprintf("convert-stargz-from-%s", desc.Digest)
		dst, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
		if err != nil {
			return nil, errors.Wrap(err, "open stargz blob writer")
		}
		defer func() {
			dst.Close()
			_ = cs.Abort(context.Background(), ref)
		}()

		// Stream the eStargz payload into the content store using a pooled
		// buffer for zero-extra-allocation data flow.
		buf := bufPool.Get().(*[]byte)
		defer bufPool.Put(buf)
		if _, err := io.CopyBuffer(dst, blob, *buf); err != nil {
			return nil, errors.Wrap(err, "copy stargz blob to content store")
		}

		targetDigest := dst.Digest()

		// Retrieve uncompressed size — only valid after the blob has been fully
		// read (i.e., after CopyBuffer above has drained the reader).
		uncompressedSize, err := blob.UncompressedSize()
		if err != nil {
			return nil, errors.Wrap(err, "get uncompressed size from estargz blob")
		}

		// Commit; tolerate a concurrent writer having already committed the same
		// digest (idempotent push semantics).
		if err := dst.Commit(ctx, 0, targetDigest); err != nil {
			if !errdefs.IsAlreadyExists(err) {
				return nil, errors.Wrap(err, "commit stargz blob to content store")
			}
		}

		return makeBlobDesc(ctx, cs, desc, targetDigest, blob.DiffID(), blob.TOCDigest(), uncompressedSize)
	}
}

// buildEstargzOptions translates a PackOption into estargz build options.
func buildEstargzOptions(opt PackOption, ctx context.Context) []estargz.Option {
	var opts []estargz.Option

	if opt.ChunkSize > 0 {
		opts = append(opts, estargz.WithChunkSize(opt.ChunkSize))
	}
	if opt.MinChunkSize > 0 {
		opts = append(opts, estargz.WithMinChunkSize(opt.MinChunkSize))
	}
	if opt.CompressionLevel != 0 {
		opts = append(opts, estargz.WithCompressionLevel(opt.CompressionLevel))
	}
	if len(opt.PrioritizedFiles) > 0 {
		opts = append(opts, estargz.WithPrioritizedFiles(opt.PrioritizedFiles))
	}
	if opt.Compression != nil {
		opts = append(opts, estargz.WithCompression(opt.Compression))
	}

	opts = append(opts, estargz.WithContext(ctx))
	return opts
}

// makeBlobDesc constructs the descriptor for a converted eStargz blob and
// persists the metadata labels on the blob's content-store Info record.
//
// Persisted labels enable two critical fast-paths:
//   - images.GetDiffID reads containerd.io/uncompressed directly from Info,
//     skipping decompression on subsequent calls (O(1) DiffID retrieval).
//   - LayerConvertFunc's idempotency check reads LayerAnnotationStargz, so
//     re-converting an already-converted layer is a pure label-check no-op.
func makeBlobDesc(
	ctx context.Context,
	cs content.Store,
	orgDesc ocispec.Descriptor,
	targetDigest digest.Digest,
	diffID digest.Digest,
	tocDigest digest.Digest,
	uncompressedSize int64,
) (*ocispec.Descriptor, error) {
	targetInfo, err := cs.Info(ctx, targetDigest)
	if err != nil {
		return nil, errors.Wrapf(err, "get stargz blob info %s", targetDigest)
	}
	if targetInfo.Labels == nil {
		targetInfo.Labels = make(map[string]string, 4)
	}

	// Fast-path label for images.GetDiffID.
	targetInfo.Labels[labels.LabelUncompressed] = diffID.String()
	// Idempotency flag for future LayerConvertFunc calls.
	targetInfo.Labels[LayerAnnotationStargz] = "true"

	if _, err = cs.Update(ctx, targetInfo, "labels."+labels.LabelUncompressed, "labels."+LayerAnnotationStargz); err != nil {
		return nil, errors.Wrap(err, "update stargz blob labels")
	}

	// Preserve the original media type — eStargz is a gzip-compatible format
	// so it can always be served as the original gzip media type.
	// If the source was an uncompressed layer, upgrade to gzip.
	mediaType := orgDesc.MediaType
	if mediaType == ocispec.MediaTypeImageLayer {
		mediaType = ocispec.MediaTypeImageLayerGzip
	}

	desc := ocispec.Descriptor{
		Digest:    targetDigest,
		Size:      targetInfo.Size,
		MediaType: mediaType,
		Annotations: map[string]string{
			// DiffID for config RootFS.DiffIDs reconciliation.
			LayerAnnotationUncompressed: diffID.String(),
			// TOC digest so snapshotters can verify lazy-fetched chunks.
			LayerAnnotationTOCDigest: tocDigest.String(),
			// Uncompressed size for containers/storage lazy-pull support.
			LayerAnnotationUncompressedSize: strconv.FormatInt(uncompressedSize, 10),
			// Boolean flag for idempotency fast-path.
			LayerAnnotationStargz: "true",
		},
	}
	return &desc, nil
}

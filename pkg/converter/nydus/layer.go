//go:build linux

package nydus

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/bons/bons-ci/pkg/archive/compression"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/containerd/nydus-snapshotter/pkg/label"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public types
// ─────────────────────────────────────────────────────────────────────────────

// LayerConvertHook provides lifecycle callbacks for a single layer conversion.
//
//   - LayerReader is called synchronously when the raw layer bytes are
//     available (before Nydus packing begins).
//   - Done is called (when non-nil) after conversion completes — whether it
//     succeeded or failed — before the converter returns.
type LayerConvertHook struct {
	LayerReader converter.LayerReaderFunc
	Done        func(ctx context.Context)
}

// ─────────────────────────────────────────────────────────────────────────────
// Buffer pool
// ─────────────────────────────────────────────────────────────────────────────

// bufPool recycles 1 MiB I/O buffers used by io.CopyBuffer in the layer pack
// goroutine, keeping heap pressure low when many layers are converted in
// parallel.
var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 1<<20) // 1 MiB
		return &buf
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// Layer conversion
// ─────────────────────────────────────────────────────────────────────────────

// layerConvertFunc returns a converter.ConvertFunc that rewrites one OCI layer
// as a Nydus blob (media type nydusConv.MediaTypeNydusBlob).
//
// # Concurrency notes
//
// This function is designed to be called from multiple goroutines in parallel
// (one per layer). Each call is fully independent: it opens its own
// ReaderAt/Writer, uses a pooled buffer, and communicates with the snapshot
// pipeline only via the synchronous LayerReader hook.
//
// Goroutine layout inside one layerConvertFunc call:
//
//	calling goroutine
//	   │
//	   ├── [sync] hook.LayerReader  — sends pipeline event before packing
//	   │
//	   ├── [goroutine A] io.CopyBuffer(tw, tr) — feeds decompressed bytes into packer
//	   │
//	   ├── [goroutine B] pipe closer — propagates copy/ctx errors → pw; closes tw
//	   │
//	   └── [sync] content.Copy(dst, pr) — consumes packed bytes; blocks until B closes pw
func LayerConvertFunc(opt PackOption, hook LayerConvertHook) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		if hook.Done != nil {
			defer hook.Done(ctx)
		}

		// Bail immediately on a pre-cancelled context so we don't open
		// writers that will never be committed.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Only convert OCI/Docker layer blobs.
		if !images.IsLayerType(desc.MediaType) {
			return nil, nil
		}
		// Already in Nydus format — nothing to do.
		if nydusConv.IsNydusBlob(desc) || nydusConv.IsNydusBootstrap(desc) {
			return nil, nil
		}

		// ── Remote cache fast path ────────────────────────────────────────
		// If a previous conversion wrote LayerAnnotationNydusTargetDigest on
		// this blob's info, reuse the already-converted Nydus blob.
		info, err := cs.Info(ctx, desc.Digest)
		if err != nil {
			return nil, errors.Wrapf(err, "get blob info %s", desc.Digest)
		}
		if cached := digest.Digest(info.Labels[nydusConv.LayerAnnotationNydusTargetDigest]); cached.Validate() == nil {
			return makeBlobDesc(ctx, cs, opt, desc.Digest, cached)
		}

		// ── Open source reader ────────────────────────────────────────────
		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, errors.Wrap(err, "get source blob reader")
		}
		defer ra.Close()

		// ── Open destination writer ───────────────────────────────────────
		ref := fmt.Sprintf("convert-nydus-from-%s", desc.Digest)
		dst, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
		if err != nil {
			return nil, errors.Wrap(err, "open blob writer")
		}
		defer func() {
			dst.Close()
			_ = cs.Abort(context.Background(), ref)
		}()

		// ── Decompress source (or use raw for OCIRef) ─────────────────────
		rdr := io.NewSectionReader(ra, 0, ra.Size())
		var tr io.ReadCloser
		if opt.OCIRef {
			tr = io.NopCloser(rdr)
		} else {
			tr, err = compression.DecompressStream(rdr)
			if err != nil {
				return nil, errors.Wrap(err, "decompress blob stream")
			}
		}

		// ── Set up Nydus packer ───────────────────────────────────────────
		digester := digest.SHA256.Digester()
		pr, pw := io.Pipe()

		tw, err := Pack(ctx, io.MultiWriter(pw, digester.Hash()), opt)
		if err != nil {
			return nil, errors.Wrap(err, "pack tar to nydus")
		}

		// ── Goroutine A: producer ─────────────────────────────────────────
		// Copies decompressed layer bytes into the Nydus packer (tw).
		// Must run concurrently with content.Copy below, which consumes pr.
		copyDone := make(chan error, 1)
		go func() {
			buf := bufPool.Get().(*[]byte)
			defer bufPool.Put(buf)
			_, err := io.CopyBuffer(tw, tr, *buf)
			copyDone <- err
		}()

		// ── Goroutine B: pipe closer ──────────────────────────────────────
		// Waits for goroutine A to finish (or ctx to be cancelled), then:
		//   1. Closes tr (decompressor) — releases gzip state.
		//   2. Closes tw (nydus packer) — flushes the Nydus footer to pw.
		//   3. Closes pw — signals content.Copy (consumer) that data is done.
		//
		// Any error closes pw with the error so content.Copy returns it.
		go func() {
			defer pw.Close()

			var producerErr error
			select {
			case <-ctx.Done():
				producerErr = ctx.Err()
			case producerErr = <-copyDone:
			}

			// Clean up components natively. tw.Close() MUST proceed to unblock Packer processes
			trErr := tr.Close()
			twErr := tw.Close()

			if producerErr != nil {
				pw.CloseWithError(producerErr)
				return
			}
			if trErr != nil {
				pw.CloseWithError(trErr)
				return
			}
			if twErr != nil {
				pw.CloseWithError(twErr)
			}
		}()

		// ── Consumer: write packed bytes to the content store ─────────────
		// Blocks until goroutine B closes pw (success) or closes it with an
		// error (failure / cancellation).
		if err := content.Copy(ctx, dst, pr, 0, ""); err != nil {
			return nil, errors.Wrap(err, "copy nydus blob to content store")
		}

		return makeBlobDesc(ctx, cs, opt, desc.Digest, digester.Digest())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Blob descriptor construction
// ─────────────────────────────────────────────────────────────────────────────

// makeBlobDesc constructs the ocispec.Descriptor for a converted Nydus blob
// and persists the containerd.io/uncompressed label on the blob's Info record.
//
// Why we persist the label: images.GetDiffID has a fast path — if
// containerd.io/uncompressed is set on the blob's stored Info, it returns the
// label value directly and skips decompression. Persisting it here means every
// subsequent GetDiffID call for this blob is O(1) with no I/O.
//
// Self-referential digest: Nydus blobs are not gzip-compressed in the OCI
// sense. We use the blob's own digest as its "uncompressed" identity, which is
// the same convention used by nydusStore.Info for pre-existing Nydus blobs.
func makeBlobDesc(ctx context.Context, cs content.Store, opt PackOption, sourceDigest, targetDigest digest.Digest) (*ocispec.Descriptor, error) {
	targetInfo, err := cs.Info(ctx, targetDigest)
	if err != nil {
		return nil, errors.Wrapf(err, "get target blob info %s", targetDigest)
	}
	if targetInfo.Labels == nil {
		targetInfo.Labels = make(map[string]string, 2)
	}

	// Persist the uncompressed label so future GetDiffID calls use the fast
	// path and never attempt to decompress the Nydus blob.
	// See: https://github.com/containerd/containerd/blob/e4fefea5/images/diffid.go#L49
	targetInfo.Labels[labels.LabelUncompressed] = targetDigest.String()
	if _, err = cs.Update(ctx, targetInfo); err != nil {
		return nil, errors.Wrap(err, "update layer label")
	}

	desc := ocispec.Descriptor{
		Digest:    targetDigest,
		Size:      targetInfo.Size,
		MediaType: nydusConv.MediaTypeNydusBlob,
		Annotations: map[string]string{
			// OCI DiffID annotation — used by tooling that inspects layer metadata.
			nydusConv.LayerAnnotationUncompressed: targetDigest.String(),
			// Mark this as a Nydus blob so IsNydusBlob returns true on the descriptor.
			nydusConv.LayerAnnotationNydusBlob: "true",
		},
	}

	if opt.OCIRef {
		// Record the original source digest so the Nydus runtime can resolve
		// the OCI ref layer without an extra round-trip to the registry.
		desc.Annotations[label.NydusRefLayer] = sourceDigest.String()
	}
	if opt.Encrypt {
		desc.Annotations[nydusConv.LayerAnnotationNydusEncryptedBlob] = "true"
	}

	return &desc, nil
}

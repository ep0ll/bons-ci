package nydus

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/bons/bons-ci/pkg/archive/compression"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/images/converter"
	"github.com/containerd/containerd/v2/pkg/labels"
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/containerd/nydus-snapshotter/pkg/label"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

var bufPool = sync.Pool{
	New: func() interface{} {
		buffer := make([]byte, 1<<20)
		return &buffer
	},
}

// LayerConvertFunc returns a function which converts an OCI image layer to
// a nydus blob layer, and set the media type to "application/vnd.oci.image.layer.nydus.blob.v1".
func LayerConvertFunc(opt nydusConv.PackOption) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		if ctx.Err() != nil {
			// The context is already cancelled, no need to proceed.
			return nil, ctx.Err()
		}
		if !images.IsLayerType(desc.MediaType) {
			return nil, nil
		}

		// Skip the conversion of nydus layer.
		if nydusConv.IsNydusBlob(desc) || nydusConv.IsNydusBootstrap(desc) {
			return nil, nil
		}

		// Use remote cache to avoid unnecessary conversion
		info, err := cs.Info(ctx, desc.Digest)
		if err != nil {
			return nil, errors.Wrapf(err, "get blob info %s", desc.Digest)
		}
		if targetDigest := digest.Digest(info.Labels[nydusConv.LayerAnnotationNydusTargetDigest]); targetDigest.Validate() == nil {
			return makeBlobDesc(ctx, cs, opt, desc.Digest, targetDigest)
		}

		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, errors.Wrap(err, "get source blob reader")
		}
		defer ra.Close()
		rdr := io.NewSectionReader(ra, 0, ra.Size())

		ref := fmt.Sprintf("convert-nydus-from-%s", desc.Digest)
		dst, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
		if err != nil {
			return nil, errors.Wrap(err, "open blob writer")
		}
		defer dst.Close()

		var tr io.ReadCloser
		if opt.OCIRef {
			tr = io.NopCloser(rdr)
		} else {
			tr, err = compression.DecompressStream(rdr)
			if err != nil {
				return nil, errors.Wrap(err, "decompress blob stream")
			}
		}

		digester := digest.SHA256.Digester()
		pr, pw := io.Pipe()
		tw, err := nydusConv.Pack(ctx, io.MultiWriter(pw, digester.Hash()), opt)
		if err != nil {
			return nil, errors.Wrap(err, "pack tar to nydus")
		}

		copyBufferDone := make(chan error, 1)
		go func() {
			buffer := bufPool.Get().(*[]byte)
			defer bufPool.Put(buffer)
			_, err := io.CopyBuffer(tw, tr, *buffer)
			copyBufferDone <- err
		}()

		go func() {
			defer pw.Close()
			select {
			case <-ctx.Done():
				// The context was cancelled!
				// Close the pipe with the context's error to signal
				// the reader to stop.
				pw.CloseWithError(ctx.Err())
				return
			case err := <-copyBufferDone:
				if err != nil {
					pw.CloseWithError(err)
					return
				}
			}
			if err := tr.Close(); err != nil {
				pw.CloseWithError(err)
				return
			}
			if err := tw.Close(); err != nil {
				pw.CloseWithError(err)
				return
			}
		}()

		if err := content.Copy(ctx, dst, pr, 0, ""); err != nil {
			return nil, errors.Wrap(err, "copy nydus blob to content store")
		}

		blobDigest := digester.Digest()
		newDesc, err := makeBlobDesc(ctx, cs, opt, desc.Digest, blobDigest)
		if err != nil {
			return nil, err
		}

		return newDesc, nil
	}
}

// makeBlobDesc returns a ocispec.Descriptor by the given information.
func makeBlobDesc(ctx context.Context, cs content.Store, opt nydusConv.PackOption, sourceDigest, targetDigest digest.Digest) (*ocispec.Descriptor, error) {
	targetInfo, err := cs.Info(ctx, targetDigest)
	if err != nil {
		return nil, errors.Wrapf(err, "get target blob info %s", targetDigest)
	}
	if targetInfo.Labels == nil {
		targetInfo.Labels = map[string]string{}
	}
	// Write a diff id label of layer in content store for simplifying
	// diff id calculation to speed up the conversion.
	// See: https://github.com/containerd/containerd/blob/e4fefea5544d259177abb85b64e428702ac49c97/images/diffid.go#L49
	targetInfo.Labels[labels.LabelUncompressed] = targetDigest.String()
	_, err = cs.Update(ctx, targetInfo)
	if err != nil {
		return nil, errors.Wrap(err, "update layer label")
	}

	targetDesc := ocispec.Descriptor{
		Digest:    targetDigest,
		Size:      targetInfo.Size,
		MediaType: nydusConv.MediaTypeNydusBlob,
		Annotations: map[string]string{
			// Use `containerd.io/uncompressed` to generate DiffID of
			// layer defined in OCI spec.
			nydusConv.LayerAnnotationUncompressed: targetDigest.String(),
			nydusConv.LayerAnnotationNydusBlob:    "true",
		},
	}

	if opt.OCIRef {
		targetDesc.Annotations[label.NydusRefLayer] = sourceDigest.String()
	}

	if opt.Encrypt {
		targetDesc.Annotations[nydusConv.LayerAnnotationNydusEncryptedBlob] = "true"
	}

	return &targetDesc, nil
}

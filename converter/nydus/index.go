package nydus

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/images/converter"

	"github.com/containerd/platforms"

	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
)

// DefaultIndexConvertFunc wraps containerd's converter to handle Nydus blob reconversion.
//
// Problem: Containerd's convertManifest calls images.GetDiffID() which attempts to
// decompress layers. Nydus blobs use a custom format and fail with "magic number mismatch".
//
// Solution: Wrap content store with a proxy that adds containerd.io/uncompressed label
// for Nydus blobs. This makes GetDiffID take the fast path and skip decompression.
func DefaultIndexConvertFunc(layerConvertFunc converter.ConvertFunc, docker2oci bool, platformMC platforms.MatchComparer) converter.ConvertFunc {
	hooks := converter.ConvertHooks{
		PostConvertHook: ConvertHookFunc(nydusConv.MergeOption{
			OCI:    true,
			OCIRef: true,
		}),
	}

	fn := converter.IndexConvertFuncWithHook(layerConvertFunc, docker2oci, platformMC, hooks)

	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		logrus.Debugf("DefaultIndexConvertFunc called for desc mediaType=%s digest=%s", desc.MediaType, desc.Digest.String())

		nydusBlobs := collectNydusBlobDigests(ctx, cs, desc)
		if len(nydusBlobs) == 0 {
			return fn(ctx, cs, desc)
		}

		ws := &wrappedStore{
			Store:      cs,
			nydusBlobs: nydusBlobs,
		}
		return fn(ctx, ws, desc)
	}
}

// This is used to identify which blobs need the uncompressed label workaround.
func collectNydusBlobDigests(ctx context.Context, cs content.Store, desc ocispec.Descriptor) map[digest.Digest]bool {
	nydusBlobs := make(map[digest.Digest]bool)

	if images.IsIndexType(desc.MediaType) {
		var index ocispec.Index
		if _, err := readJSON(ctx, cs, &index, desc); err != nil {
			logrus.WithError(err).Warn("failed to read index")
			return nydusBlobs
		}
		for _, m := range index.Manifests {
			if images.IsManifestType(m.MediaType) {
				collectFromManifest(ctx, cs, m, nydusBlobs)
			}
		}
	} else if images.IsManifestType(desc.MediaType) {
		collectFromManifest(ctx, cs, desc, nydusBlobs)
	}

	logrus.Debugf("Collected %d Nydus blob digests", len(nydusBlobs))
	return nydusBlobs
}

func collectFromManifest(ctx context.Context, cs content.Store, desc ocispec.Descriptor, nydusBlobs map[digest.Digest]bool) {
	var manifest ocispec.Manifest
	if _, err := readJSON(ctx, cs, &manifest, desc); err != nil {
		logrus.WithError(err).Warnf("failed to read manifest %s", desc.Digest)
		return
	}

	for _, l := range manifest.Layers {
		if nydusConv.IsNydusBlob(l) {
			logrus.Debugf("Found Nydus blob: %s (mediaType=%s)", l.Digest, l.MediaType)
			nydusBlobs[l.Digest] = true
		}
	}
}

// wrappedStore wraps the content store to add containerd.io/uncompressed labels
// for Nydus blobs. This makes images.GetDiffID skip decompression and return
// the digest directly.
type wrappedStore struct {
	content.Store
	nydusBlobs map[digest.Digest]bool // Set of Nydus blob digests
}

func (s *wrappedStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	info, err := s.Store.Info(ctx, dgst)
	if err != nil {
		return info, err
	}

	// If this is a Nydus blob, add the uncompressed label
	if s.nydusBlobs[dgst] {
		if info.Labels == nil {
			info.Labels = make(map[string]string)
		}
		// Use the blob's own digest as the "uncompressed" digest
		// This makes GetDiffID return the blob digest directly
		info.Labels["containerd.io/uncompressed"] = dgst.String()
		logrus.Debugf("Added uncompressed label for Nydus blob: %s", dgst)
	}

	return info, nil
}

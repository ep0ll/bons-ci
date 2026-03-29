//go:build linux

package nydus

import (
	"context"
	"fmt"
	"io"

	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/containerd/nydus-snapshotter/pkg/label"
	gzip "github.com/klauspost/pgzip"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ConvertHookFunc returns a function which will be used as a callback
// called for each blob after conversion is done. The function only hooks
// the index conversion and the manifest conversion.
func ConvertHookFunc(opt nydusConv.MergeOption) converter.ConvertHookFunc {
	return func(ctx context.Context, cs content.Store, orgDesc ocispec.Descriptor, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
		// If the previous conversion did not occur, the `newDesc` may be nil.
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

// convertIndex modifies the original index converting it to manifest directly if it contains only one manifest.
func convertIndex(ctx context.Context, cs content.Store, newDesc *ocispec.Descriptor) (*ocispec.Descriptor, error) {
	var index ocispec.Index
	_, err := converter.ReadJSON(ctx, cs, &index, *newDesc)
	if err != nil {
		return nil, errors.Wrap(err, "read index json")
	}

	// If the converted manifest list contains only one manifest,
	// convert it directly to manifest.
	if len(index.Manifests) == 1 {
		return &index.Manifests[0], nil
	}
	return newDesc, nil
}

// convertManifest merges all the nydus blob layers into a
// nydus bootstrap layer, update the image config,
// and modify the image manifest.
func convertManifest(ctx context.Context, cs content.Store, oldDesc ocispec.Descriptor, newDesc *ocispec.Descriptor, opt nydusConv.MergeOption) (*ocispec.Descriptor, error) {
	var manifest ocispec.Manifest
	manifestDesc := *newDesc
	manifestLabels, err := converter.ReadJSON(ctx, cs, &manifest, manifestDesc)
	if err != nil {
		return nil, errors.Wrap(err, "read manifest json")
	}

	if isNydusImage(&manifest) {
		return &manifestDesc, nil
	}

	var layersToKeep []ocispec.Descriptor
	bootstrapIndex := -1

	// 1. Filter Layers: Remove Nydus Bootstrap Layer
	for i, l := range manifest.Layers {
		if IsNydusBootstrap(l) {
			bootstrapIndex = i
			// Clean GC labels for the removed layer
			converter.ClearGCLabels(manifestLabels, l.Digest)
		} else {
			layersToKeep = append(layersToKeep, l)
		}
	}

	manifest.Layers = layersToKeep

	// 2. Read and Update Config
	var config ocispec.Image
	configLabels, err := converter.ReadJSON(ctx, cs, &config, manifest.Config)
	if err != nil {
		return nil, errors.Wrap(err, "read image config")
	}

	// 2.1 Remove corresponding DiffID
	if bootstrapIndex != -1 && len(config.RootFS.DiffIDs) > bootstrapIndex {
		config.RootFS.DiffIDs = append(config.RootFS.DiffIDs[:bootstrapIndex], config.RootFS.DiffIDs[bootstrapIndex+1:]...)
	}

	// 2.2 Clean History
	var newHistory []ocispec.History
	for _, h := range config.History {
		// Remove Nydus Bootstrap History
		if h.Comment == "Nydus Bootstrap Layer" && h.CreatedBy == "Nydus Converter" {
			continue
		}
		newHistory = append(newHistory, h)
	}
	config.History = newHistory

	// This option needs to be enabled for image scenario.
	opt.WithTar = true

	// If the original image is already an OCI type, we should forcibly set the
	// bootstrap layer to the OCI type.
	if !opt.OCI && oldDesc.MediaType == ocispec.MediaTypeImageManifest {
		opt.OCI = true
	}

	// Append bootstrap layer to manifest, encrypt bootstrap layer if needed.
	bootstrapDesc, blobDescs, err := MergeLayers(ctx, cs, manifest.Layers, opt)
	if err != nil {
		return nil, errors.Wrap(err, "merge nydus layers")
	}
	if opt.Backend != nil {
		// Only append nydus bootstrap layer into manifest, and do not put nydus
		// blob layer into manifest if blob storage backend is specified.
		manifest.Layers = []ocispec.Descriptor{*bootstrapDesc}
	} else {
		for idx, blobDesc := range blobDescs {
			blobGCLabelKey := fmt.Sprintf("containerd.io/gc.ref.content.l.%d", idx)
			manifestLabels[blobGCLabelKey] = blobDesc.Digest.String()
		}
		// Affected by chunk dict, the blob list referenced by final bootstrap
		// are from different layers, part of them are from original layers, part
		// from chunk dict bootstrap, so we need to rewrite manifest's layers here.
		blobDescs := append(blobDescs, *bootstrapDesc)
		manifest.Layers = blobDescs
	}

	// Update the gc label of bootstrap layer
	bootstrapGCLabelKey := fmt.Sprintf("containerd.io/gc.ref.content.l.%d", len(manifest.Layers)-1)
	manifestLabels[bootstrapGCLabelKey] = bootstrapDesc.Digest.String()

	bootstrapHistory := ocispec.History{
		CreatedBy: "Nydus Converter",
		Comment:   "Nydus Bootstrap Layer",
	}
	if opt.Backend != nil {
		config.RootFS.DiffIDs = []digest.Digest{digest.Digest(bootstrapDesc.Annotations[nydusConv.LayerAnnotationUncompressed])}
		config.History = []ocispec.History{bootstrapHistory}
	} else {
		config.RootFS.DiffIDs = make([]digest.Digest, 0, len(manifest.Layers))
		for i, layer := range manifest.Layers {
			config.RootFS.DiffIDs = append(config.RootFS.DiffIDs, digest.Digest(layer.Annotations[nydusConv.LayerAnnotationUncompressed]))
			// Remove useless annotation.
			delete(manifest.Layers[i].Annotations, nydusConv.LayerAnnotationUncompressed)
		}
		// Append history item for bootstrap layer, to ensure the history consistency.
		// See https://github.com/distribution/distribution/blob/e5d5810851d1f17a5070e9b6f940d8af98ea3c29/manifest/schema1/config_builder.go#L136
		config.History = append(config.History, bootstrapHistory)
	}
	// Update image config in content store.
	newConfigDesc, err := converter.WriteJSON(ctx, cs, config, manifest.Config, configLabels)
	if err != nil {
		return nil, errors.Wrap(err, "write image config")
	}
	// When manifests are merged, we need to put a special value for the config mediaType.
	// This values must be one that containerd doesn't understand to ensure it doesn't try tu pull the nydus image
	// but use the OCI one instead. And then if the nydus-snapshotter is used, it can pull the nydus image instead.
	if opt.MergeManifest {
		newConfigDesc.MediaType = nydusConv.ManifestConfigNydus
	}
	manifest.Config = *newConfigDesc
	// Update the config gc label
	manifestLabels[configGCLabelKey] = newConfigDesc.Digest.String()

	if opt.WithReferrer {
		// Associate a reference to the original OCI manifest.
		// See the `subject` field description in
		// https://github.com/opencontainers/image-spec/blob/main/manifest.md#image-manifest-property-descriptions
		manifest.Subject = &oldDesc
		// Remove the platform field as it is not supported by certain registries like ECR.
		manifest.Subject.Platform = nil
	}

	// Update image manifest in content store.
	newManifestDesc, err := converter.WriteJSON(ctx, cs, manifest, manifestDesc, manifestLabels)
	if err != nil {
		return nil, errors.Wrap(err, "write manifest")
	}

	return newManifestDesc, nil
}

// MergeLayers merges a list of nydus blob layer into a nydus bootstrap layer.
// The media type of the nydus bootstrap layer is "application/vnd.oci.image.layer.v1.tar+gzip".
func MergeLayers(ctx context.Context, cs content.Store, descs []ocispec.Descriptor, opt nydusConv.MergeOption) (*ocispec.Descriptor, []ocispec.Descriptor, error) {
	// Extracts nydus bootstrap from nydus format for each layer.
	layers := []nydusConv.Layer{}

	var chainID digest.Digest
	nydusBlobDigests := []digest.Digest{}
	for _, nydusBlobDesc := range descs {
		ra, err := cs.ReaderAt(ctx, nydusBlobDesc)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "get reader for blob %q", nydusBlobDesc.Digest)
		}
		defer ra.Close()
		var originalDigest *digest.Digest
		if opt.OCIRef {
			digestStr := nydusBlobDesc.Annotations[label.NydusRefLayer]
			_originalDigest, err := digest.Parse(digestStr)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "invalid label %s=%s", label.NydusRefLayer, digestStr)
			}
			originalDigest = &_originalDigest
		}
		layers = append(layers, nydusConv.Layer{
			Digest:         nydusBlobDesc.Digest,
			OriginalDigest: originalDigest,
			ReaderAt:       ra,
		})
		if chainID == "" {
			chainID = identity.ChainID([]digest.Digest{nydusBlobDesc.Digest})
		} else {
			chainID = identity.ChainID([]digest.Digest{chainID, nydusBlobDesc.Digest})
		}
		nydusBlobDigests = append(nydusBlobDigests, nydusBlobDesc.Digest)
	}

	// Merge all nydus bootstraps into a final nydus bootstrap.
	pr, pw := io.Pipe()
	// BUG FIX: Prevent goroutine leak if cw.Commit or io.CopyBuffer fails bounds below.
	defer pr.Close()
	
	originalBlobDigestChan := make(chan []digest.Digest, 1)
	go func() {
		defer pw.Close()
		originalBlobDigests, err := Merge(ctx, layers, pw, opt)
		if err != nil {
			pw.CloseWithError(errors.Wrapf(err, "merge nydus bootstrap"))
		}
		originalBlobDigestChan <- originalBlobDigests
	}()

	// Compress final nydus bootstrap to tar.gz and write into content store.
	ref := "nydus-merge-" + chainID.String()
	cw, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
	if err != nil {
		return nil, nil, errors.Wrap(err, "open content store writer")
	}
	defer func() {
		cw.Close()
		_ = cs.Abort(context.Background(), ref)
	}()

	gw := gzip.NewWriter(cw)
	uncompressedDgst := digest.SHA256.Digester()
	compressed := io.MultiWriter(gw, uncompressedDgst.Hash())
	buffer := bufPool.Get().(*[]byte)
	defer bufPool.Put(buffer)
	if _, err := io.CopyBuffer(compressed, pr, *buffer); err != nil {
		return nil, nil, errors.Wrapf(err, "copy bootstrap targz into content store")
	}
	if err := gw.Close(); err != nil {
		return nil, nil, errors.Wrap(err, "close gzip writer")
	}

	compressedDgst := cw.Digest()
	if err := cw.Commit(ctx, 0, compressedDgst, content.WithLabels(map[string]string{
		nydusConv.LayerAnnotationUncompressed: uncompressedDgst.Digest().String(),
	})); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return nil, nil, errors.Wrap(err, "commit to content store")
		}
	}
	if err := cw.Close(); err != nil {
		return nil, nil, errors.Wrap(err, "close content store writer")
	}

	bootstrapInfo, err := cs.Info(ctx, compressedDgst)
	if err != nil {
		return nil, nil, errors.Wrap(err, "get info from content store")
	}

	originalBlobDigests := <-originalBlobDigestChan
	blobDescs := []ocispec.Descriptor{}

	var blobDigests []digest.Digest
	if opt.OCIRef {
		blobDigests = nydusBlobDigests
	} else {
		blobDigests = originalBlobDigests
	}

	for idx, blobDigest := range blobDigests {
		blobInfo, err := cs.Info(ctx, blobDigest)
		if err != nil {
			return nil, nil, errors.Wrap(err, "get info from content store")
		}
		blobDesc := ocispec.Descriptor{
			Digest:    blobDigest,
			Size:      blobInfo.Size,
			MediaType: nydusConv.MediaTypeNydusBlob,
			Annotations: map[string]string{
				nydusConv.LayerAnnotationUncompressed: blobDigest.String(),
				nydusConv.LayerAnnotationNydusBlob:    "true",
			},
		}
		if opt.OCIRef {
			blobDesc.Annotations[label.NydusRefLayer] = layers[idx].OriginalDigest.String()
		}

		if opt.Encrypt != nil {
			blobDesc.Annotations[nydusConv.LayerAnnotationNydusEncryptedBlob] = "true"
		}

		blobDescs = append(blobDescs, blobDesc)
	}

	if opt.FsVersion == "" {
		opt.FsVersion = "6"
	}
	mediaType := images.MediaTypeDockerSchema2LayerGzip
	if opt.OCI {
		mediaType = ocispec.MediaTypeImageLayerGzip
	}

	bootstrapDesc := ocispec.Descriptor{
		Digest:    compressedDgst,
		Size:      bootstrapInfo.Size,
		MediaType: mediaType,
		Annotations: map[string]string{
			nydusConv.LayerAnnotationUncompressed: uncompressedDgst.Digest().String(),
			nydusConv.LayerAnnotationFSVersion:    opt.FsVersion,
			// Use this annotation to identify nydus bootstrap layer.
			nydusConv.LayerAnnotationNydusBootstrap: "true",
		},
	}

	if opt.Encrypt != nil {
		// Encrypt the Nydus bootstrap layer.
		bootstrapDesc, err = opt.Encrypt(ctx, cs, bootstrapDesc)
		if err != nil {
			return nil, nil, errors.Wrap(err, "encrypt bootstrap layer")
		}
	}
	return &bootstrapDesc, blobDescs, nil
}

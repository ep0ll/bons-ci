//go:build !windows

package ingestion

import (
	"context"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/containerd/nydus-snapshotter/pkg/converter"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// ExportLayer holds the descriptor and DiffID for a re-compressed layer
// intended for exporting to a target registry.
type ExportLayer struct {
	Desc   ocispec.Descriptor
	DiffID digest.Digest
}

// ConvertLayer converts a single OCI layer to Nydus format and optionally
// re-compresses it for export. It reads the layer from the content store,
// decompresses it, and tees the uncompressed stream to:
// 1. Nydus converter.Pack
// 2. Requested compressor (for export)
// 3. Digester (for DiffID)
func ConvertLayer(
	ctx context.Context,
	cs content.Store,
	desc ocispec.Descriptor,
	opt converter.PackOption,
	backend converter.Backend,
	exportComp compression.Compression,
) (*ocispec.Descriptor, *converter.Layer, *ExportLayer, error) {
	if !images.IsLayerType(desc.MediaType) {
		return nil, nil, nil, nil
	}

	// Skip already-converted layers.
	if converter.IsNydusBlob(desc) || converter.IsNydusBootstrap(desc) {
		return nil, nil, nil, nil
	}

	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("convert: open source layer %s: %w", desc.Digest, err)
	}
	defer ra.Close()

	rdr := io.NewSectionReader(ra, 0, ra.Size())

	// Decompress the source stream.
	uncompressedRdr, err := compression.DecompressStream(rdr)
	if err != nil {
		// Fallback to reading directly if decompression fails (might not be compressed)
		uncompressedRdr = &readCloserWrapper{Reader: rdr}
	} else {
		defer uncompressedRdr.Close()
	}

	var (
		diffIDDigester = digest.SHA256.Digester()
		writers        []io.Writer
		eg, gctx       = errgroup.WithContext(ctx)

		nydusDigester digest.Digester
		nydusCw       content.Writer
		packWriter    io.WriteCloser

		exportCw             content.Writer
		exportDigester       digest.Digester
		exportCompressWriter io.WriteCloser
	)

	writers = append(writers, diffIDDigester.Hash())

	// Set up Nydus Pack pipeline
	nydusDigester = digest.SHA256.Digester()
	nydusRef := fmt.Sprintf("convert-nydus-from-%s", desc.Digest)
	nydusCw, err = content.OpenWriter(gctx, cs, content.WithRef(nydusRef))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("convert: open nydus writer: %w", err)
	}
	defer nydusCw.Close()

	packWriter, err = converter.Pack(gctx, io.MultiWriter(nydusCw, nydusDigester.Hash()), opt)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("convert: init pack: %w", err)
	}
	writers = append(writers, packWriter)

	// Set up Export re-compression pipeline
	if exportComp != compression.Unknown {
		exportDigester = digest.SHA256.Digester()
		exportRef := fmt.Sprintf("export-%s-%s", exportComp.Extension(), desc.Digest)
		exportCw, err = content.OpenWriter(gctx, cs, content.WithRef(exportRef))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("open export writer: %w", err)
		}
		defer exportCw.Close()

		exportCompressWriter, err = compression.CompressStream(io.MultiWriter(exportCw, exportDigester.Hash()), exportComp)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("init export compressor: %w", err)
		}
		writers = append(writers, exportCompressWriter)
	}

	// Copy the decompressed stream into all writers concurrently.
	eg.Go(func() error {
		defer packWriter.Close()
		if exportCompressWriter != nil {
			defer exportCompressWriter.Close()
		}

		buf := make([]byte, 1<<20)
		mw := io.MultiWriter(writers...)
		_, err := io.CopyBuffer(mw, uncompressedRdr, buf)
		return err
	})

	// Wait for stream processing to finish.
	if err := eg.Wait(); err != nil {
		return nil, nil, nil, err
	}

	diffID := diffIDDigester.Digest()
	blobDigest := nydusDigester.Digest()

	// Build the Nydus blob descriptor.
	blobInfo, err := cs.Info(ctx, blobDigest)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("convert: get blob info %s: %w", blobDigest, err)
	}

	nydusDesc := ocispec.Descriptor{
		Digest:    blobDigest,
		Size:      blobInfo.Size,
		MediaType: converter.MediaTypeNydusBlob,
		Annotations: map[string]string{
			converter.LayerAnnotationUncompressed: blobDigest.String(),
			converter.LayerAnnotationNydusBlob:    "true",
		},
	}

	// Push to backend (S3/B2) if configured.
	if backend != nil {
		if err := backend.Push(ctx, cs, nydusDesc); err != nil {
			return nil, nil, nil, fmt.Errorf("convert: push blob to backend: %w", err)
		}
	}

	// Build the Layer info for bootstrap merging.
	blobRA, err := cs.ReaderAt(ctx, nydusDesc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("convert: open nydus blob reader: %w", err)
	}

	layer := &converter.Layer{
		Digest:   blobDigest,
		ReaderAt: blobRA,
	}

	var exportLayer *ExportLayer
	if exportComp != compression.Unknown {
		exportDigest := exportDigester.Digest()
		expInfo, err := cs.Info(ctx, exportDigest)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("get export blob info: %w", err)
		}

		mediaType := ocispec.MediaTypeImageLayer
		if exportComp == compression.Gzip {
			mediaType = ocispec.MediaTypeImageLayerGzip
		} else if exportComp == compression.Zstd {
			mediaType = ocispec.MediaTypeImageLayerZstd
		}

		exportLayer = &ExportLayer{
			Desc: ocispec.Descriptor{
				MediaType: mediaType,
				Digest:    exportDigest,
				Size:      expInfo.Size,
			},
			DiffID: diffID,
		}
	}

	return &nydusDesc, layer, exportLayer, nil
}

type readCloserWrapper struct {
	io.Reader
}

func (r *readCloserWrapper) Close() error                            { return nil }
func (r *readCloserWrapper) GetCompression() compression.Compression { return compression.Uncompressed }

// ConvertAndMerge runs the full image conversion pipeline:
//  1. Converts all OCI layers to Nydus blobs in parallel (bounded by concurrency)
//  2. Merges all per-layer bootstraps into a final composite bootstrap
//  3. Rewrites the manifest and config for the Nydus image
//  4. Pushes the bootstrap and final manifest to the backend
//  5. Optionally outputs re-compressed export layers
//
// Returns the final Nydus image manifest descriptor and a slice of ExportLayer.
func ConvertAndMerge(
	ctx context.Context,
	cs content.Store,
	manifest ocispec.Manifest,
	packOpt converter.PackOption,
	mergeOpt converter.MergeOption,
	backend converter.Backend,
	exportComp compression.Compression,
	concurrency int,
) (*ocispec.Descriptor, []ExportLayer, error) {
	if concurrency <= 0 {
		concurrency = 4
	}

	layers := manifest.Layers
	sem := semaphore.NewWeighted(int64(concurrency))

	// Parallel layer conversion.
	type convResult struct {
		Desc        *ocispec.Descriptor
		Layer       *converter.Layer
		ExportLayer *ExportLayer
	}

	results := make([]*convResult, len(layers))
	eg, gctx := errgroup.WithContext(ctx)

	for i, layerDesc := range layers {
		if err := sem.Acquire(gctx, 1); err != nil {
			break
		}
		eg.Go(func() error {
			defer sem.Release(1)

			nydusDesc, layer, exportLayer, err := ConvertLayer(gctx, cs, layerDesc, packOpt, backend, exportComp)
			if err != nil {
				return fmt.Errorf("convert layer %d (%s): %w", i, layerDesc.Digest, err)
			}

			results[i] = &convResult{
				Desc:        nydusDesc,
				Layer:       layer,
				ExportLayer: exportLayer,
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		// Close any opened ReaderAt handles.
		for _, r := range results {
			if r != nil && r.Layer != nil {
				r.Layer.ReaderAt.Close()
			}
		}
		return nil, nil, err
	}

	// Collect converted layers and descriptors for merge.
	var (
		mergeLayers  []converter.Layer
		nydusDescs   []ocispec.Descriptor
		exportLayers []ExportLayer
	)
	for _, r := range results {
		if r == nil {
			continue
		}
		if r.Desc != nil {
			mergeLayers = append(mergeLayers, *r.Layer)
			nydusDescs = append(nydusDescs, *r.Desc)
		}
		if r.ExportLayer != nil {
			exportLayers = append(exportLayers, *r.ExportLayer)
		}
	}
	defer func() {
		for _, l := range mergeLayers {
			l.ReaderAt.Close()
		}
	}()

	// Merge bootstraps into a final Nydus bootstrap.
	mergeOpt.WithTar = true
	mergeOpt.Backend = backend

	bootstrapDesc, blobDescs, err := converter.MergeLayers(ctx, cs, nydusDescs, mergeOpt)
	if err != nil {
		return nil, nil, fmt.Errorf("merge layers: %w", err)
	}

	// Build the final Nydus manifest.
	if backend != nil {
		// With a backend, only store the bootstrap in the manifest.
		manifest.Layers = []ocispec.Descriptor{*bootstrapDesc}
	} else {
		finalLayers := make([]ocispec.Descriptor, 0, len(blobDescs)+1)
		finalLayers = append(finalLayers, blobDescs...)
		finalLayers = append(finalLayers, *bootstrapDesc)
		manifest.Layers = finalLayers
	}

	// Rewrite the config with updated DiffIDs.
	config, err := readImageConfig(ctx, cs, manifest.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}

	config.RootFS.DiffIDs = make([]digest.Digest, 0, len(manifest.Layers))
	for _, layer := range manifest.Layers {
		uncompressed := layer.Annotations[converter.LayerAnnotationUncompressed]
		config.RootFS.DiffIDs = append(config.RootFS.DiffIDs, digest.Digest(uncompressed))
	}
	config.History = append(config.History, ocispec.History{
		CreatedBy: "Nydus Converter",
		Comment:   "Nydus Bootstrap Layer",
	})

	newConfigDesc, err := writeJSON(ctx, cs, config, manifest.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("write config: %w", err)
	}
	manifest.Config = *newConfigDesc

	// Write updated manifest to content store.
	newManifestDesc, err := writeJSON(ctx, cs, manifest, ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("write manifest: %w", err)
	}

	return newManifestDesc, exportLayers, nil
}

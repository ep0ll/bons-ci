//go:build !windows

package ingestion

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bons/bons-ci/core/content/registry/registry_repo"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/transfer"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/containerd/nydus-snapshotter/pkg/converter"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

// ExportTarget configures a destination for exported layers.
type ExportTarget struct {
	// Ref is the destination image reference.
	Ref string
	// RegistryOpts configures registry authentication and transport.
	RegistryOpts []registry.Opt
	// LayerFilter identifies which layers (by original descriptor) should be included.
	LayerFilter func(ocispec.Descriptor) bool
}

// PipelineConfig holds all dependencies for the ingestion pipeline.
type PipelineConfig struct {
	// SourceRef is the remote image reference (e.g., "docker.io/library/nginx:latest").
	SourceRef string

	// ContentStore is the primary local content store for caching fetched blobs.
	ContentStore content.Store

	// SnapshotterStore is an optional content store backed by snapshotters.
	// If provided, fetched content will be tee'd to this store in parallel.
	SnapshotterStore content.Store

	// NydusBackend is the S3/B2 backend for pushing Nydus-converted blobs.
	// If nil, Nydus blobs are only written to the ContentStore.
	NydusBackend converter.Backend

	// PackOpt configures per-layer Nydus conversion (fs version, compressor, etc).
	PackOpt converter.PackOption

	// MergeOpt configures bootstrap merging behavior.
	MergeOpt converter.MergeOption

	// ExportCompression is the requested compression format for exported layers.
	// If set to Unknown, no re-compression or export is performed.
	ExportCompression compression.Compression

	// ExportTargets defines multiple registries to push specified subsets of layers.
	ExportTargets []ExportTarget

	// RegistryOpts configures registry authentication and transport.
	RegistryOpts []registry.Opt

	// Concurrency limits the number of layers processed in parallel.
	// Defaults to 4 if not set.
	Concurrency int
}

// Pipeline is the main orchestrator for pulling OCI images from a remote
// registry, storing them to content stores and snapshotters, converting
// to Nydus format, and pushing to S3/B2 — all in parallel.
type Pipeline struct {
	cfg     PipelineConfig
	regRepo registry_repo.RegistryRepo
}

// NewPipeline creates a new ingestion pipeline with the given configuration.
func NewPipeline(cfg PipelineConfig) (*Pipeline, error) {
	if cfg.SourceRef == "" {
		return nil, fmt.Errorf("pipeline: source reference is required")
	}
	if cfg.ContentStore == nil {
		return nil, fmt.Errorf("pipeline: content store is required")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}

	repo := registry_repo.NewOCIRegistryRepo()
	if _, err := repo.Put(context.Background(), cfg.SourceRef, cfg.RegistryOpts...); err != nil {
		return nil, fmt.Errorf("pipeline: initialize source registry: %w", err)
	}

	for i, target := range cfg.ExportTargets {
		opts := target.RegistryOpts
		if len(opts) == 0 {
			opts = cfg.RegistryOpts
		}
		if _, err := repo.Put(context.Background(), target.Ref, opts...); err != nil {
			return nil, fmt.Errorf("pipeline: initialize target registry %d: %w", i, err)
		}
	}

	return &Pipeline{
		cfg:     cfg,
		regRepo: repo,
	}, nil
}

// Run executes the full ingestion pipeline:
//  1. Resolves the image reference to a root descriptor
//  2. Fetches all content (index → manifests → configs + layers) from the registry
//  3. Stores each blob to the content store and snapshotter store in parallel
//  4. Converts every layer to Nydus format (Pack) in parallel
//  5. Merges all per-layer bootstraps into a final Nydus bootstrap
//  6. Rewrites the manifest/config/index for the Nydus image
//  7. Pushes everything to the S3/B2 backend
//
// Returns the final Nydus image descriptor (manifest or index).
func (p *Pipeline) Run(ctx context.Context) (*ocispec.Descriptor, error) {
	// Step 1: Resolve the image reference.
	reg, err := p.regRepo.Get(ctx, p.cfg.SourceRef)
	if err != nil {
		return nil, fmt.Errorf("pipeline: get registry: %w", err)
	}

	_, rootDesc, err := reg.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipeline: resolve %s: %w", p.cfg.SourceRef, err)
	}

	// Step 2: Fetch root descriptor content.
	fetcher, err := reg.Fetcher(ctx, p.cfg.SourceRef)
	if err != nil {
		return nil, fmt.Errorf("pipeline: create fetcher: %w", err)
	}

	if err := p.fetchAndStore(ctx, fetcher, rootDesc); err != nil {
		return nil, fmt.Errorf("pipeline: fetch root: %w", err)
	}

	// Step 3: Walk the image tree and process according to type.
	switch {
	case images.IsIndexType(rootDesc.MediaType):
		return p.processIndex(ctx, fetcher, rootDesc)
	case images.IsManifestType(rootDesc.MediaType):
		return p.processManifest(ctx, fetcher, rootDesc)
	default:
		return nil, fmt.Errorf("pipeline: unsupported root media type: %s", rootDesc.MediaType)
	}
}

// processIndex handles an OCI index by processing each manifest entry in parallel.
func (p *Pipeline) processIndex(ctx context.Context, fetcher transfer.Fetcher, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
	index, err := readIndex(ctx, p.cfg.ContentStore, desc)
	if err != nil {
		return nil, err
	}

	eg, gctx := errgroup.WithContext(ctx)
	nydusManifests := make([]ocispec.Descriptor, len(index.Manifests))

	for i, mDesc := range index.Manifests {
		// Fetch the manifest content first.
		if err := p.fetchAndStore(gctx, fetcher, mDesc); err != nil {
			return nil, fmt.Errorf("pipeline: fetch manifest %d: %w", i, err)
		}

		eg.Go(func() error {
			nydusDesc, err := p.processManifest(gctx, fetcher, mDesc)
			if err != nil {
				return fmt.Errorf("process manifest %d: %w", i, err)
			}
			nydusManifests[i] = *nydusDesc
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// Write the Nydus index.
	nydusIndex := ocispec.Index{
		Versioned: index.Versioned,
		MediaType: index.MediaType,
		Manifests: nydusManifests,
	}

	nydusIndexDesc, err := writeJSON(ctx, p.cfg.ContentStore, nydusIndex, ocispec.Descriptor{
		MediaType: index.MediaType,
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline: write nydus index: %w", err)
	}

	return nydusIndexDesc, nil
}

// processManifest handles a single OCI manifest:
//   - Fetches and stores the config and all layers in parallel
//   - Converts each layer to Nydus format
//   - Merges bootstraps and rewrites the manifest
func (p *Pipeline) processManifest(ctx context.Context, fetcher transfer.Fetcher, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
	manifest, err := readManifest(ctx, p.cfg.ContentStore, desc)
	if err != nil {
		return nil, err
	}

	// Fetch config in parallel with layers.
	eg, gctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return p.fetchAndStore(gctx, fetcher, manifest.Config)
	})

	// Fetch all layers in parallel, storing to content store + snapshotter.
	for _, layerDesc := range manifest.Layers {
		eg.Go(func() error {
			return p.fetchAndStore(gctx, fetcher, layerDesc)
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, fmt.Errorf("pipeline: fetch manifest content: %w", err)
	}

	// Convert all layers to Nydus and merge bootstraps.
	nydusDesc, exportLayers, err := ConvertAndMerge(
		ctx,
		p.cfg.ContentStore,
		*manifest,
		p.cfg.PackOpt,
		p.cfg.MergeOpt,
		p.cfg.NydusBackend,
		p.cfg.ExportCompression,
		p.cfg.Concurrency,
	)
	if err != nil {
		return nil, fmt.Errorf("pipeline: convert and merge: %w", err)
	}

	// Process exports in parallel
	if len(p.cfg.ExportTargets) > 0 {
		egExport, gctxExport := errgroup.WithContext(ctx)
		for _, target := range p.cfg.ExportTargets {
			t := target
			egExport.Go(func() error {
				return p.exportAssembler(gctxExport, t, manifest.Config, manifest.Layers, exportLayers)
			})
		}
		if err := egExport.Wait(); err != nil {
			return nil, fmt.Errorf("pipeline: export push failed: %w", err)
		}
	}

	return nydusDesc, nil
}

// fetchAndStore fetches content from the registry and stores it to the
// content store (and optionally the snapshotter store) in parallel.
func (p *Pipeline) fetchAndStore(ctx context.Context, fetcher transfer.Fetcher, desc ocispec.Descriptor) error {
	// Check if content already exists locally.
	if _, err := p.cfg.ContentStore.Info(ctx, desc.Digest); err == nil {
		return nil
	}

	ra, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", desc.Digest, err)
	}
	defer ra.Close()

	// The fetcher returns an io.ReadCloser, use it directly as a reader.
	rdr := ra

	// Determine target stores.
	stores := []content.Store{p.cfg.ContentStore}
	if p.cfg.SnapshotterStore != nil {
		stores = append(stores, p.cfg.SnapshotterStore)
	}

	if len(stores) == 1 {
		// Single store: direct write.
		w, err := p.cfg.ContentStore.Writer(ctx, content.WithDescriptor(desc))
		if err != nil {
			return fmt.Errorf("open writer: %w", err)
		}
		defer w.Close()

		return content.Copy(ctx, w, rdr, desc.Size, desc.Digest)
	}

	// Multiple stores: fan-out write.
	fw, err := NewFanoutStore(stores[0], stores[1:]...).Writer(ctx, content.WithDescriptor(desc))
	if err != nil {
		return fmt.Errorf("create fanout writer: %w", err)
	}
	defer fw.Close()

	return content.Copy(ctx, fw, rdr, desc.Size, desc.Digest)
}

// resolveManifest reads the raw bytes from the content store and detects
// whether a descriptor points to an index or a manifest by attempting
// to unmarshal as each type.
func resolveManifest(data []byte) (bool, error) {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false, err
	}
	return images.IsIndexType(probe.MediaType), nil
}

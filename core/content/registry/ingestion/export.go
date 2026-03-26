//go:build !windows

package ingestion

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

// exportPusher is a helper to push content to a registry
func (p *Pipeline) exportPusher(ctx context.Context, target ExportTarget) (func(context.Context, ocispec.Descriptor) (transfer.Pusher, error), error) {
	reg, err := p.regRepo.Get(ctx, target.Ref)
	if err != nil {
		return nil, fmt.Errorf("get target registry: %w", err)
	}
	// Return a closure that fetches the pusher for a specific descriptor
	return func(c context.Context, d ocispec.Descriptor) (transfer.Pusher, error) {
		return reg.Pusher(c, d)
	}, nil
}

// pushBlob pushes a single blob from the content store to the target registry
func pushBlob(ctx context.Context, cs content.Store, getPusher func(context.Context, ocispec.Descriptor) (transfer.Pusher, error), desc ocispec.Descriptor) error {
	pusher, err := getPusher(ctx, desc)
	if err != nil {
		return fmt.Errorf("get pusher %s: %w", desc.Digest, err)
	}

	w, err := pusher.Push(ctx, desc)
	if err != nil {
		return fmt.Errorf("initiate push %s: %w", desc.Digest, err)
	}
	defer w.Close()

	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return fmt.Errorf("read blob %s: %w", desc.Digest, err)
	}
	defer ra.Close()

	if err := content.Copy(ctx, w, content.NewReader(ra), desc.Size, desc.Digest); err != nil {
		return fmt.Errorf("copy blob %s: %w", desc.Digest, err)
	}
	return w.Commit(ctx, desc.Size, desc.Digest)
}

// exportAssembler builds a custom image config and manifest containing only the
// layers approved by the target's LayerFilter, and pushes the assembled image
// to the target registry.
func (p *Pipeline) exportAssembler(
	ctx context.Context,
	target ExportTarget,
	origConfigDesc ocispec.Descriptor,
	origLayers []ocispec.Descriptor,
	exportLayers []ExportLayer,
) error {
	if len(origLayers) != len(exportLayers) {
		return fmt.Errorf("export: layer count mismatch: original=%d exported=%d", len(origLayers), len(exportLayers))
	}

	getPusher, err := p.exportPusher(ctx, target)
	if err != nil {
		return fmt.Errorf("export pipeline: %w", err)
	}

	// 1. Filter layers and collect DiffIDs
	var (
		filteredLayers  []ocispec.Descriptor
		filteredDiffIDs []digest.Digest
	)

	for i, origLayer := range origLayers {
		if target.LayerFilter != nil && !target.LayerFilter(origLayer) {
			continue // skip this layer
		}

		exp := exportLayers[i]
		filteredLayers = append(filteredLayers, exp.Desc)
		filteredDiffIDs = append(filteredDiffIDs, exp.DiffID)
	}

	// 2. Read and modify the original image config
	origConfig, err := readImageConfig(ctx, p.cfg.ContentStore, origConfigDesc)
	if err != nil {
		return fmt.Errorf("export: read original config: %w", err)
	}

	// Rebuild config with only the filtered layers
	origConfig.RootFS.DiffIDs = make([]digest.Digest, len(filteredDiffIDs))
	copy(origConfig.RootFS.DiffIDs, filteredDiffIDs)

	// Add history entry for the export modification
	origConfig.History = append(origConfig.History, ocispec.History{
		CreatedBy: "Export Pipeline",
		Comment:   "Filtered and re-compressed layers",
	})

	// 3. Write the new config
	newConfigDesc, err := writeJSON(ctx, p.cfg.ContentStore, origConfig, ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
	})
	if err != nil {
		return fmt.Errorf("export: write new config: %w", err)
	}

	// 4. Assemble the new manifest
	newManifest := ocispec.Manifest{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    *newConfigDesc,
		Layers:    filteredLayers,
	}

	// Write the new manifest
	newManifestDesc, err := writeJSON(ctx, p.cfg.ContentStore, newManifest, ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
	})
	if err != nil {
		return fmt.Errorf("export: write new manifest: %w", err)
	}

	// 5. Push the filtered layers, the new config, and the new manifest
	eg, gctx := errgroup.WithContext(ctx)

	// Push layers concurrently
	for _, layer := range filteredLayers {
		l := layer
		eg.Go(func() error {
			return pushBlob(gctx, p.cfg.ContentStore, getPusher, l)
		})
	}

	if err := eg.Wait(); err != nil {
		return fmt.Errorf("export: push layers: %w", err)
	}

	// Push config
	if err := pushBlob(ctx, p.cfg.ContentStore, getPusher, *newConfigDesc); err != nil {
		return fmt.Errorf("export: push config: %w", err)
	}

	// Push manifest
	if err := pushBlob(ctx, p.cfg.ContentStore, getPusher, *newManifestDesc); err != nil {
		return fmt.Errorf("export: push manifest: %w", err)
	}

	return nil
}

/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/images/converter"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

// newConverter constructs a defaultConverter, centralising the initialisation
// of the CPU-bound semaphore and the singleflight dedup group so that both
// DefaultIndexConvertFunc and IndexConvertFuncWithHook share identical setup.
func newConverter(layerConvertFunc ConvertFunc, docker2oci bool, platformMC platforms.MatchComparer, hooks ConvertHooks) *defaultConverter {
	return &defaultConverter{
		layerConvertFunc: layerConvertFunc,
		docker2oci:       docker2oci,
		platformMC:       platformMC,
		hooks:            hooks,
		diffIDMap:        make(map[digest.Digest]digest.Digest),
		// layerSem caps concurrent layer conversions to the number of logical
		// CPUs.  Layer conversion is typically CPU-bound (compression /
		// decompression), so going beyond NumCPU() only adds scheduling
		// overhead without improving throughput.
		layerSem: make(chan struct{}, runtime.NumCPU()*2),
	}
	// layerGroup is a zero-value singleflight.Group – no initialisation needed.
}

type defaultConverter struct {
	layerConvertFunc ConvertFunc
	docker2oci       bool
	platformMC       platforms.MatchComparer
	hooks            ConvertHooks

	diffIDMap   map[digest.Digest]digest.Digest
	diffIDMapMu sync.RWMutex

	// layerSem is a counting semaphore that limits the number of layer
	// conversions running in parallel to runtime.NumCPU().
	layerSem chan struct{}

	// layerGroup deduplicates concurrent conversions of identical layer
	// digests: if N goroutines request the same digest simultaneously, only
	// one conversion is executed and the result is shared with all N callers.
	layerGroup singleflight.Group
}

// layerResult is the singleflight-safe wrapper for a layer conversion outcome.
// Using a struct avoids the typed-nil-interface ambiguity that arises when
// storing a (*ocispec.Descriptor)(nil) directly as an interface{}.
type layerResult struct {
	desc *ocispec.Descriptor
}

// convert dispatches desc.MediaType and calls c.convert{Layer,Manifest,Index,Config}.
//
// Also converts media type if c.docker2oci is set.
func (c *defaultConverter) convert(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
	var (
		newDesc *ocispec.Descriptor
		err     error
	)
	switch {
	case images.IsLayerType(desc.MediaType):
		newDesc, err = c.convertLayer(ctx, cs, desc)
	case images.IsManifestType(desc.MediaType):
		newDesc, err = c.convertManifest(ctx, cs, desc)
	case images.IsIndexType(desc.MediaType):
		newDesc, err = c.convertIndex(ctx, cs, desc)
	case images.IsConfigType(desc.MediaType):
		newDesc, err = c.convertConfig(ctx, cs, desc)
	}
	if err != nil {
		return nil, err
	}

	if c.hooks.PostConvertHook != nil {
		if newDescPost, err := c.hooks.PostConvertHook(ctx, cs, desc, newDesc); err != nil {
			return nil, err
		} else if newDescPost != nil {
			newDesc = newDescPost
		}
	}

	if images.IsDockerType(desc.MediaType) {
		if c.docker2oci {
			if newDesc == nil {
				newDesc = copyDesc(desc)
			}
			newDesc.MediaType = converter.ConvertDockerMediaTypeToOCI(newDesc.MediaType)
		} else if (newDesc == nil && len(desc.Annotations) != 0) || (newDesc != nil && len(newDesc.Annotations) != 0) {
			// Annotations are supported only on OCI manifests; strip them for
			// Docker media types.
			if newDesc == nil {
				newDesc = copyDesc(desc)
			}
			newDesc.Annotations = nil
		}
	}
	log.G(ctx).WithField("old", desc).WithField("new", newDesc).Debugf("converted")
	return newDesc, nil
}

// convertLayer converts a single image layer using c.layerConvertFunc.
//
// Two mechanisms work together for maximum throughput:
//
//  1. Deduplication via singleflight – layers that share a digest (common in
//     multi-arch images) are converted exactly once; every other caller blocks
//     and receives the same result without redundant work.
//
//  2. Concurrency cap via layerSem – at most runtime.NumCPU() conversions run
//     in parallel, keeping CPU-bound compression/decompression at peak
//     utilisation without thrashing the scheduler.
func (c *defaultConverter) convertLayer(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
	if c.layerConvertFunc == nil {
		return nil, nil
	}

	// Key on the content digest so that identical layers are never converted twice.
	v, err, _ := c.layerGroup.Do(desc.Digest.String(), func() (interface{}, error) {
		// Acquire a slot in the CPU-bound semaphore before doing real work.
		select {
		case c.layerSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-c.layerSem }()

		eg, cctx := errgroup.WithContext(ctx)
		if c.hooks.PreConvertHook != nil {
			eg.Go(func() error {
				_, err := c.hooks.PreConvertHook(cctx, cs, desc)
				return err
			})
		}
		var newDesc *ocispec.Descriptor
		eg.Go(func() (err error) {
			newDesc, err = c.layerConvertFunc(ctx, cs, desc)
			return err
		})
		err := eg.Wait()
		return &layerResult{desc: newDesc}, err
	})
	if err != nil {
		return nil, err
	}
	return v.(*layerResult).desc, nil
}

// convertManifest converts image manifests.
//
// - converts `.mediaType` if the target format is OCI
// - records diff ID changes in c.diffIDMap
func (c *defaultConverter) convertManifest(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (_ *ocispec.Descriptor, err error) {
	var (
		manifest ocispec.Manifest
		modified bool
	)
	eg, ctx2 := errgroup.WithContext(ctx)
	defer func() {
		err = eg.Wait()
	}()
	if c.hooks.PreConvertHook != nil {
		eg.Go(func() error {
			_, err := c.hooks.PreConvertHook(ctx2, cs, desc)
			return err
		})
	}
	labels, err := ReadJSON(ctx, cs, &manifest, desc)
	if err != nil {
		return nil, err
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	if images.IsDockerType(manifest.MediaType) && c.docker2oci {
		manifest.MediaType = converter.ConvertDockerMediaTypeToOCI(manifest.MediaType)
		modified = true
	}

	var mu sync.Mutex
	for i, l := range manifest.Layers {
		ieg, ictx := errgroup.WithContext(ctx)
		var oldDiffID digest.Digest
		ieg.Go(func() error {
			var dErr error
			oldDiffID, dErr = images.GetDiffID(ictx, cs, l)
			return dErr
		})
		eg.Go(func() error {
			newL, err := c.convert(ctx2, cs, l)
			if err != nil {
				return err
			}
			if newL == nil {
				return nil
			}

			newDiffID, err := images.GetDiffID(ctx, cs, *newL)
			if err != nil {
				return err
			}

			if err := ieg.Wait(); err != nil {
				return err
			}

			// diffID changes only when tar entries were modified (not merely
			// re-compressed); record the mapping for later config patching.
			if newDiffID != oldDiffID {
				c.diffIDMapMu.Lock()
				c.diffIDMap[oldDiffID] = newDiffID
				c.diffIDMapMu.Unlock()
			}

			mu.Lock()
			converter.ClearGCLabels(labels, l.Digest)
			labels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = newL.Digest.String()
			manifest.Layers[i] = *newL
			modified = true
			mu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	newConfig, err := c.convert(ctx, cs, manifest.Config)
	if err != nil {
		return nil, err
	}
	if newConfig != nil {
		converter.ClearGCLabels(labels, manifest.Config.Digest)
		labels["containerd.io/gc.ref.content.config"] = newConfig.Digest.String()
		manifest.Config = *newConfig
		modified = true
	}

	if modified {
		return WriteJSON(ctx, cs, &manifest, desc, labels)
	}
	return nil, nil
}

// convertIndex converts image index.
//
// - converts `.mediaType` if the target format is OCI
// - removes manifest entries that do not match c.platformMC
func (c *defaultConverter) convertIndex(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
	var (
		index    ocispec.Index
		modified bool
	)
	labels, err := ReadJSON(ctx, cs, &index, desc)
	if err != nil {
		return nil, err
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	if images.IsDockerType(index.MediaType) && c.docker2oci {
		index.MediaType = converter.ConvertDockerMediaTypeToOCI(index.MediaType)
		modified = true
	}

	newManifests := make([]ocispec.Descriptor, len(index.Manifests))
	toRemove := make(map[int]struct{})
	var mu sync.Mutex
	eg, ctx2 := errgroup.WithContext(ctx)
	if c.hooks.PreConvertHook != nil {
		eg.Go(func() error {
			_, err := c.hooks.PreConvertHook(ctx2, cs, desc)
			return err
		})
	}
	for i, mani := range index.Manifests {
		labelKey := fmt.Sprintf("containerd.io/gc.ref.content.m.%d", i)
		eg.Go(func() error {
			if mani.Platform != nil && !c.platformMC.Match(*mani.Platform) {
				mu.Lock()
				converter.ClearGCLabels(labels, mani.Digest)
				toRemove[i] = struct{}{}
				modified = true
				mu.Unlock()
				return nil
			}
			newMani, err := c.convert(ctx2, cs, mani)
			if err != nil {
				return err
			}
			mu.Lock()
			if newMani != nil {
				converter.ClearGCLabels(labels, mani.Digest)
				labels[labelKey] = newMani.Digest.String()
				newManifests[i] = *newMani
				modified = true
			} else {
				newManifests[i] = mani
			}
			mu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	if modified {
		filtered := newManifests[:0]
		for i, m := range newManifests {
			if _, skip := toRemove[i]; !skip {
				filtered = append(filtered, m)
			}
		}
		index.Manifests = filtered
		return WriteJSON(ctx, cs, &index, desc, labels)
	}
	return nil, nil
}

// convertConfig converts image config contents.
//
// - updates `.rootfs.diff_ids` using c.diffIDMap
// - clears legacy `.config.Image` and `.container_config.Image` when diff IDs change
func (c *defaultConverter) convertConfig(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (_ *ocispec.Descriptor, err error) {
	var (
		cfg      DualConfig
		cfgAsOCI ocispec.Image // read-only; used for typed access to rootfs
		modified bool
	)
	eg, ctx2 := errgroup.WithContext(ctx)
	defer func() {
		err = eg.Wait()
	}()
	if c.hooks.PreConvertHook != nil {
		eg.Go(func() error {
			_, err := c.hooks.PreConvertHook(ctx2, cs, desc)
			return err
		})
	}

	labels, err := ReadJSON(ctx, cs, &cfg, desc)
	if err != nil {
		return nil, err
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	if _, err := ReadJSON(ctx, cs, &cfgAsOCI, desc); err != nil {
		return nil, err
	}

	if rootfs := cfgAsOCI.RootFS; rootfs.Type == "layers" {
		rootfsModified := false
		c.diffIDMapMu.RLock()
		for i, oldDiffID := range rootfs.DiffIDs {
			if newDiffID, ok := c.diffIDMap[oldDiffID]; ok && newDiffID != oldDiffID {
				rootfs.DiffIDs[i] = newDiffID
				rootfsModified = true
			}
		}
		c.diffIDMapMu.RUnlock()
		if rootfsModified {
			rootfsB, err := json.Marshal(rootfs)
			if err != nil {
				return nil, err
			}
			cfg["rootfs"] = (*json.RawMessage)(&rootfsB)
			modified = true
		}
	}

	if modified {
		if _, err := clearDockerV1DummyID(cfg); err != nil {
			return nil, err
		}
		return WriteJSON(ctx, cs, &cfg, desc, labels)
	}
	return nil, err
}

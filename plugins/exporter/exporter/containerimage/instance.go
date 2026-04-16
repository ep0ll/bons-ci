package containerimage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// instance is a single-use, configured container image export operation.
// It is created by ContainerImageExporter.Resolve and consumed by Export.
type instance struct {
	cfg    Config
	store  core.ContentStore
	pusher Pusher
	storer ImageStorer
	writer *manifestWriter
}

func newInstance(cfg Config, store core.ContentStore, pusher Pusher, storer ImageStorer) *instance {
	return &instance{
		cfg:    cfg,
		store:  store,
		pusher: pusher,
		storer: storer,
		writer: newManifestWriter(store),
	}
}

// Export is the core export implementation. It:
//  1. Determines whether to produce a single-platform manifest or a multi-platform index.
//  2. Commits all layer blobs to the content store.
//  3. Builds and commits the image config blob.
//  4. Builds and commits the manifest / index blob.
//  5. Stores the image name(s) locally (if Store is set).
//  6. Returns a FinalizeFunc that pushes to the remote registry (if Push is set).
func (i *instance) Export(ctx context.Context, req *core.ExportRequest) (*core.ExportResult, core.FinalizeFunc, error) {
	if err := i.validateDeps(); err != nil {
		return nil, nil, err
	}

	a := req.Artifact
	reporter := req.Reporter

	// Resolve image name from request (overrides config).
	imageName := req.ImageName
	if imageName == "" {
		imageName = i.cfg.ImageName
	}

	reporter.Start(ctx, "export", "exporting container image")
	defer func() { reporter.Complete(ctx, "export", nil) }()

	// Determine mode: multi-platform (index) vs single-platform (manifest).
	isMultiPlatform := len(a.Platforms) > 1

	var rootDesc core.BlobDescriptor
	var err error

	if isMultiPlatform {
		rootDesc, err = i.exportIndex(ctx, req, a, imageName, reporter)
	} else {
		rootDesc, err = i.exportManifest(ctx, req, a, imageName, reporter)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("containerimage export: %w", err)
	}

	result := &core.ExportResult{
		Descriptor:  &rootDesc,
		ImageDigest: rootDesc.Digest,
		ImageName:   imageName,
	}
	result.Set(core.ResultKeyImageDigest, rootDesc.Digest.String())
	if imageName != "" {
		result.Set(core.ResultKeyImageName, imageName)
	}

	// Encode descriptor for downstream tools.
	if dtdesc, err := json.Marshal(rootDesc); err == nil {
		result.Set(core.ResultKeyDescriptor, string(dtdesc))
	}

	// Store locally.
	if i.cfg.Store && i.storer != nil && imageName != "" {
		reporter.Start(ctx, "store", "naming image "+imageName)
		for _, name := range splitNames(imageName) {
			if err := i.storer.Store(ctx, name, rootDesc); err != nil {
				reporter.Complete(ctx, "store", err)
				return nil, nil, fmt.Errorf("store image: %w", err)
			}
		}
		if i.cfg.Unpack {
			if err := i.storer.Unpack(ctx, imageName); err != nil {
				reporter.Complete(ctx, "store", err)
				return nil, nil, fmt.Errorf("unpack image: %w", err)
			}
		}
		reporter.Complete(ctx, "store", nil)
	}

	// Build push finalize func.
	if !req.Push && !i.cfg.Push {
		return result, nil, nil
	}
	if i.pusher == nil {
		return nil, nil, fmt.Errorf("push requested but no Pusher configured")
	}
	names := splitNames(imageName)
	if req.PushByDigest || i.cfg.PushByDigest {
		names = append(names, "") // push by digest only
	}

	finalize := func(ctx context.Context) error {
		for _, name := range names {
			ref := name
			if ref == "" {
				ref = rootDesc.Digest.String()
			}
			reporter.Start(ctx, "push:"+name, "pushing "+ref)
			pushErr := i.pusher.Push(ctx, ref, rootDesc.Digest, i.store)
			reporter.Complete(ctx, "push:"+name, pushErr)
			if pushErr != nil {
				return fmt.Errorf("push %q: %w", ref, pushErr)
			}
		}
		return nil
	}

	return result, finalize, nil
}

// exportManifest builds a single-platform OCI manifest + config.
func (i *instance) exportManifest(
	ctx context.Context,
	req *core.ExportRequest,
	a *core.Artifact,
	imageName string,
	reporter core.ProgressReporter,
) (core.BlobDescriptor, error) {
	reporter.Start(ctx, "layers", "exporting layers")
	layerDescs, err := i.writer.commitLayers(ctx, a.Layers, i.cfg)
	reporter.Complete(ctx, "layers", err)
	if err != nil {
		return core.BlobDescriptor{}, err
	}

	reporter.Start(ctx, "config", "exporting config")
	configDesc, err := i.writer.commitConfig(ctx, a.Config, layerDescs, req.Epoch, i.cfg)
	reporter.Complete(ctx, "config", err)
	if err != nil {
		return core.BlobDescriptor{}, err
	}

	platform := resolvePlatform(a)
	annotations := mergeAnnotations(a.Annotations, req.Annotations)

	reporter.Start(ctx, "manifest", "exporting manifest")
	mfstDesc, err := i.writer.commitManifest(ctx, configDesc, layerDescs, annotations, i.cfg)
	reporter.Complete(ctx, "manifest", err)
	if err != nil {
		return core.BlobDescriptor{}, err
	}
	_ = platform

	// Attach attestation manifest if there are non-inline-only attestations.
	extAtts := filterExternalAttestations(a.Attestations, i.cfg.ForceInlineAttestations)
	if len(extAtts) > 0 {
		reporter.Start(ctx, "attestations", "exporting attestation manifest")
		_, attErr := i.writer.commitAttestationManifest(ctx, mfstDesc, extAtts, i.cfg)
		reporter.Complete(ctx, "attestations", attErr)
		if attErr != nil {
			return core.BlobDescriptor{}, attErr
		}
	}

	return mfstDesc, nil
}

// exportIndex builds a multi-platform OCI image index.
func (i *instance) exportIndex(
	ctx context.Context,
	req *core.ExportRequest,
	a *core.Artifact,
	imageName string,
	reporter core.ProgressReporter,
) (core.BlobDescriptor, error) {
	manifests := make([]core.BlobDescriptor, 0, len(a.Platforms))

	for idx, platform := range a.Platforms {
		id := fmt.Sprintf("manifest[%s]", platform.String())
		reporter.Start(ctx, id, "exporting "+platform.String())

		platformArtifact := platformSlice(a, idx)
		mfstDesc, err := i.exportManifest(ctx, req, platformArtifact, imageName, reporter)
		reporter.Complete(ctx, id, err)
		if err != nil {
			return core.BlobDescriptor{}, err
		}

		// Tag with platform in the descriptor.
		if mfstDesc.Annotations == nil {
			mfstDesc.Annotations = make(map[string]string)
		}
		mfstDesc.Annotations["platform"] = platform.String()
		manifests = append(manifests, mfstDesc)
	}

	reporter.Start(ctx, "index", "exporting manifest index")
	indexDesc, err := i.writer.commitIndex(ctx, manifests, a.Annotations, i.cfg)
	reporter.Complete(ctx, "index", err)
	return indexDesc, err
}

func (i *instance) validateDeps() error {
	if i.store == nil {
		return fmt.Errorf("containerimage: ContentStore dependency not injected")
	}
	return nil
}

// ─── helpers ───────────────────────────────────────────────────────────────

func splitNames(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func mergeAnnotations(base, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func resolvePlatform(a *core.Artifact) core.Platform {
	if len(a.Platforms) > 0 {
		return a.Platforms[0]
	}
	return core.Platform{OS: "linux", Architecture: "amd64"}
}

func filterExternalAttestations(atts []core.AttestationRecord, forceInline bool) []core.AttestationRecord {
	if forceInline {
		return nil // all attestations are inline; nothing to export separately
	}
	out := make([]core.AttestationRecord, 0, len(atts))
	for _, a := range atts {
		if !a.InlineOnly {
			out = append(out, a)
		}
	}
	return out
}

// platformSlice returns a single-platform Artifact view (layers and attestations
// for that platform index only). For simplicity in this framework the full
// layer set is kept; real production code would filter by platform key.
func platformSlice(a *core.Artifact, idx int) *core.Artifact {
	clone := a.Clone()
	if idx < len(a.Platforms) {
		clone.Platforms = []core.Platform{a.Platforms[idx]}
	}
	return clone
}

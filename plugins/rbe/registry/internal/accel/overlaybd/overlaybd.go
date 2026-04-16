// Package overlaybd implements the AccelHandler for OverlayBD image acceleration.
//
// OverlayBD converts container image layers into virtual block devices using
// a COW (copy-on-write) block-layer design. Each layer becomes an OverlayBD
// "diff" block device image stored as a gzip-compressed blob.
//
// Detection heuristics:
//  1. Any layer annotation contains OverlayBDAnnotationLayer.
//  2. Config annotation contains OverlayBDAnnotationVersion.
//  3. Any layer MediaType matches OverlayBDLayerMediaType.
package overlaybd

import (
	"context"
	"encoding/json"
	"fmt"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

const (
	LayerAnnotation   = types.OverlayBDAnnotationLayer
	VersionAnnotation = types.OverlayBDAnnotationVersion
	LayerMediaType    = types.OverlayBDLayerMediaType
)

// Handler implements types.AccelHandler for OverlayBD images.
type Handler struct{}

func New() *Handler { return &Handler{} }

func (h *Handler) Name() types.AccelType { return types.AccelOverlayBD }

// Detect returns true if the manifest represents an OverlayBD image.
func (h *Handler) Detect(_ context.Context, manifest ocispec.Manifest, configBlob []byte) (types.AccelType, bool, error) {
	// 1. Layer-level annotation
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[LayerAnnotation]; ok {
			return types.AccelOverlayBD, true, nil
		}
		if layer.MediaType == LayerMediaType {
			return types.AccelOverlayBD, true, nil
		}
	}
	// 2. Config blob annotation
	if len(configBlob) > 0 {
		var cfg struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"config"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if _, ok := cfg.Config.Labels[VersionAnnotation]; ok {
				return types.AccelOverlayBD, true, nil
			}
		}
	}
	// 3. Manifest annotations
	if _, ok := manifest.Annotations[types.AnnotationAccelType]; ok {
		if manifest.Annotations[types.AnnotationAccelType] == string(types.AccelOverlayBD) {
			return types.AccelOverlayBD, true, nil
		}
	}
	return types.AccelUnknown, false, nil
}

// ExtractSourceRefs extracts source digests from OverlayBD manifests.
// OverlayBD does not natively carry source digests, so we rely on:
//  1. The canonical org.accelregistry.source.digest annotation
//  2. The OCI 1.1 subject field
//  3. Per-layer annotations (OverlayBD base layers may carry original digests)
func (h *Handler) ExtractSourceRefs(
	_ context.Context,
	manifest ocispec.Manifest,
	_ []byte,
) ([]types.SourceRef, error) {
	seen := make(map[digest.Digest]struct{})
	var refs []types.SourceRef

	add := func(r types.SourceRef) {
		if r.Digest == "" {
			return
		}
		if _, dup := seen[r.Digest]; dup {
			return
		}
		seen[r.Digest] = struct{}{}
		refs = append(refs, r)
	}

	// 1. Canonical annotations
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceDigest]; ok {
		if dgst, err := digest.Parse(dgstStr); err == nil {
			add(types.SourceRef{Digest: dgst, Kind: types.SourceRefManifest})
		}
	}
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceIndexDigest]; ok {
		if dgst, err := digest.Parse(dgstStr); err == nil {
			add(types.SourceRef{Digest: dgst, Kind: types.SourceRefIndex})
		}
	}

	// 2. OCI 1.1 subject
	if manifest.Subject != nil {
		add(types.SourceRef{
			Digest:    manifest.Subject.Digest,
			MediaType: manifest.Subject.MediaType,
			Kind:      types.SourceRefManifest,
		})
	}

	// 3. Per-layer annotations
	for _, layer := range manifest.Layers {
		for _, key := range []string{
			"containerd.io/snapshot/overlaybd.source-layer-digest",
			"org.opencontainers.image.layer.original-digest",
		} {
			if dgstStr, ok := layer.Annotations[key]; ok {
				if dgst, err := digest.Parse(dgstStr); err == nil {
					add(types.SourceRef{
						Digest: dgst,
						Kind:   types.SourceRefLayer,
						Annotations: map[string]string{
							"origin":      "layer.annotation." + key,
							"layerDigest": layer.Digest.String(),
						},
					})
				}
			}
		}
	}

	return refs, nil
}

// Validate checks OverlayBD structural requirements.
func (h *Handler) Validate(_ context.Context, manifest ocispec.Manifest) error {
	hasOBDLayer := false
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[LayerAnnotation]; ok {
			hasOBDLayer = true
			break
		}
		if layer.MediaType == LayerMediaType {
			hasOBDLayer = true
			break
		}
	}
	if !hasOBDLayer {
		return fmt.Errorf("overlaybd: manifest has no OverlayBD layer annotation or media type")
	}
	_, hasCanonical := manifest.Annotations[types.AnnotationSourceDigest]
	hasSubject := manifest.Subject != nil
	if !hasCanonical && !hasSubject {
		return fmt.Errorf("overlaybd: manifest lacks source digest reference")
	}
	return nil
}

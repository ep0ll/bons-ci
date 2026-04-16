// Package nydus implements the AccelHandler for Nydus (Dragonfly) image acceleration.
//
// Nydus stores container layers as a Merkle-tree–based content-addressable
// filesystem (RAFS). Each OCI layer blob is a "nydus blob" and one additional
// layer is the "nydus bootstrap" which carries the filesystem metadata.
//
// Detection heuristics (any one is sufficient):
//  1. At least one layer has MediaType == NydusLayerMediaType or NydusBootstrapMediaType.
//  2. Manifest or config annotation contains NydusAnnotationSourceDigest.
//  3. Manifest annotation contains NydusAnnotationVersion.
//
// Source-digest extraction (in priority order):
//  1. Manifest annotation "containerd.io/snapshot/nydus-source-digest"
//  2. Config annotation of the same key
//  3. Canonical "org.accelregistry.source.digest"
//  4. OCI 1.1 subject field
package nydus

import (
	"context"
	"encoding/json"
	"fmt"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// NydusLayerMediaType is the OCI media type for nydus blob layers.
const (
	NydusLayerMediaType     = types.NydusLayerMediaType
	NydusBootstrapMediaType = types.NydusBootstrapMediaType
	NydusSourceAnnotation   = types.NydusAnnotationSourceDigest
	NydusVersionAnnotation  = types.NydusAnnotationVersion
)

// Handler implements types.AccelHandler for Nydus images.
type Handler struct{}

// New returns a ready-to-use Nydus AccelHandler.
func New() *Handler { return &Handler{} }

// Name implements types.AccelDetector.
func (h *Handler) Name() types.AccelType { return types.AccelNydus }

// Detect returns true if the manifest represents a Nydus-accelerated image.
func (h *Handler) Detect(_ context.Context, manifest ocispec.Manifest, configBlob []byte) (types.AccelType, bool, error) {
	// 1. Layer media type check (most definitive)
	for _, layer := range manifest.Layers {
		if layer.MediaType == NydusLayerMediaType ||
			layer.MediaType == NydusBootstrapMediaType {
			return types.AccelNydus, true, nil
		}
	}

	// 2. Manifest annotations
	if v, ok := manifest.Annotations[NydusSourceAnnotation]; ok && v != "" {
		return types.AccelNydus, true, nil
	}
	if v, ok := manifest.Annotations[NydusVersionAnnotation]; ok && v != "" {
		return types.AccelNydus, true, nil
	}

	// 3. Config blob annotations (if config is an OCI image config)
	if len(configBlob) > 0 {
		var cfg struct {
			Annotations map[string]string `json:"annotations"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if v, ok := cfg.Annotations[NydusSourceAnnotation]; ok && v != "" {
				return types.AccelNydus, true, nil
			}
		}
	}

	return types.AccelUnknown, false, nil
}

// ExtractSourceRefs returns all SourceRefs linking this manifest back to
// its non-accelerated origin. Nydus carries the source digest on the manifest
// annotations and optionally on each layer's annotations.
func (h *Handler) ExtractSourceRefs(
	_ context.Context,
	manifest ocispec.Manifest,
	configBlob []byte,
) ([]types.SourceRef, error) {
	seen := make(map[digest.Digest]struct{})
	var refs []types.SourceRef

	addRef := func(ref types.SourceRef) {
		if ref.Digest == "" {
			return
		}
		if _, dup := seen[ref.Digest]; dup {
			return
		}
		seen[ref.Digest] = struct{}{}
		refs = append(refs, ref)
	}

	// 1. Manifest annotation — source manifest digest
	if dgstStr, ok := manifest.Annotations[NydusSourceAnnotation]; ok && dgstStr != "" {
		dgst, err := digest.Parse(dgstStr)
		if err == nil {
			addRef(types.SourceRef{
				Digest: dgst,
				Kind:   types.SourceRefManifest,
				Annotations: map[string]string{
					"origin": "manifest.annotation." + NydusSourceAnnotation,
				},
			})
		}
	}

	// 2. Canonical accelregistry annotation
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceDigest]; ok && dgstStr != "" {
		dgst, err := digest.Parse(dgstStr)
		if err == nil {
			addRef(types.SourceRef{
				Digest: dgst,
				Kind:   types.SourceRefManifest,
				Annotations: map[string]string{
					"origin": "manifest.annotation." + types.AnnotationSourceDigest,
				},
			})
		}
	}
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceIndexDigest]; ok && dgstStr != "" {
		dgst, err := digest.Parse(dgstStr)
		if err == nil {
			addRef(types.SourceRef{
				Digest: dgst,
				Kind:   types.SourceRefIndex,
			})
		}
	}

	// 3. OCI 1.1 subject field
	if manifest.Subject != nil {
		addRef(types.SourceRef{
			Digest:    manifest.Subject.Digest,
			MediaType: manifest.Subject.MediaType,
			Kind:      types.SourceRefManifest,
			Annotations: map[string]string{
				"origin": "manifest.subject",
			},
		})
	}

	// 4. Per-layer source annotations (nydus stores per-blob source sha256)
	for _, layer := range manifest.Layers {
		if dgstStr, ok := layer.Annotations[NydusSourceAnnotation]; ok && dgstStr != "" {
			dgst, err := digest.Parse(dgstStr)
			if err == nil {
				addRef(types.SourceRef{
					Digest: dgst,
					Kind:   types.SourceRefLayer,
					Annotations: map[string]string{
						"origin":        "layer.annotation." + NydusSourceAnnotation,
						"layerDigest":   layer.Digest.String(),
						"layerSize":     fmt.Sprintf("%d", layer.Size),
						"layerMediaType": layer.MediaType,
					},
				})
			}
		}
	}

	// 5. Config blob — parse config to find source reference
	if len(configBlob) > 0 {
		var cfg struct {
			Annotations map[string]string `json:"annotations"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if dgstStr, ok := cfg.Annotations[NydusSourceAnnotation]; ok && dgstStr != "" {
				dgst, err := digest.Parse(dgstStr)
				if err == nil {
					addRef(types.SourceRef{
						Digest: dgst,
						Kind:   types.SourceRefConfig,
						Annotations: map[string]string{
							"origin": "config.annotation." + NydusSourceAnnotation,
						},
					})
				}
			}
		}
	}

	return refs, nil
}

// Validate checks structural requirements for a Nydus manifest.
// A valid Nydus manifest must have:
//   - At least one nydus blob layer
//   - Exactly one bootstrap layer (the filesystem metadata index)
//   - At least one annotation pointing back to a source digest
func (h *Handler) Validate(_ context.Context, manifest ocispec.Manifest) error {
	var blobCount, bootstrapCount int

	for _, layer := range manifest.Layers {
		switch layer.MediaType {
		case NydusLayerMediaType:
			blobCount++
		case NydusBootstrapMediaType:
			bootstrapCount++
		}
	}

	if blobCount == 0 && bootstrapCount == 0 {
		return fmt.Errorf("nydus: manifest has no nydus layers (blob or bootstrap)")
	}
	if bootstrapCount > 1 {
		return fmt.Errorf("nydus: manifest has %d bootstrap layers; expected at most 1", bootstrapCount)
	}
	if manifest.Annotations == nil {
		return fmt.Errorf("nydus: manifest has no annotations; missing source digest reference")
	}

	// Require at least one form of source reference
	_, hasSource := manifest.Annotations[NydusSourceAnnotation]
	_, hasCanonical := manifest.Annotations[types.AnnotationSourceDigest]
	hasSubject := manifest.Subject != nil

	if !hasSource && !hasCanonical && !hasSubject {
		return fmt.Errorf(
			"nydus: manifest lacks source digest annotation (%s or %s) and no subject field",
			NydusSourceAnnotation, types.AnnotationSourceDigest,
		)
	}

	return nil
}

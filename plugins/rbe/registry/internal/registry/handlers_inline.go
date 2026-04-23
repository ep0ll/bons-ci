package registry

// This file contains the inline detection/extraction logic for all accel
// types used by the handler adapters in registry.go.
// These are thin wrappers that delegate to the pkg-level logic; keeping
// them here avoids import cycles between internal/registry and
// internal/accel/{nydus,estargz,soci,overlaybd}.

import (
	"context"
	"encoding/json"
	"fmt"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Nydus inline implementation
// ────────────────────────────────────────────────────────────────────────────

func detectNydus(_ context.Context, manifest ocispec.Manifest, configBlob []byte) (types.AccelType, bool, error) {
	for _, layer := range manifest.Layers {
		if layer.MediaType == types.NydusLayerMediaType ||
			layer.MediaType == types.NydusBootstrapMediaType {
			return types.AccelNydus, true, nil
		}
	}
	if v, ok := manifest.Annotations[types.NydusAnnotationSourceDigest]; ok && v != "" {
		return types.AccelNydus, true, nil
	}
	if v, ok := manifest.Annotations[types.NydusAnnotationVersion]; ok && v != "" {
		return types.AccelNydus, true, nil
	}
	if len(configBlob) > 0 {
		var cfg struct {
			Annotations map[string]string `json:"annotations"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if v, ok := cfg.Annotations[types.NydusAnnotationSourceDigest]; ok && v != "" {
				return types.AccelNydus, true, nil
			}
		}
	}
	return types.AccelUnknown, false, nil
}

func extractNydusRefs(_ context.Context, manifest ocispec.Manifest, configBlob []byte) ([]types.SourceRef, error) {
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
	for _, key := range []string{types.NydusAnnotationSourceDigest, types.AnnotationSourceDigest} {
		if dgstStr, ok := manifest.Annotations[key]; ok && dgstStr != "" {
			if dgst, err := digest.Parse(dgstStr); err == nil {
				add(types.SourceRef{Digest: dgst, Kind: types.SourceRefManifest})
			}
		}
	}
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceIndexDigest]; ok && dgstStr != "" {
		if dgst, err := digest.Parse(dgstStr); err == nil {
			add(types.SourceRef{Digest: dgst, Kind: types.SourceRefIndex})
		}
	}
	if manifest.Subject != nil {
		add(types.SourceRef{Digest: manifest.Subject.Digest, MediaType: manifest.Subject.MediaType, Kind: types.SourceRefManifest})
	}
	for _, layer := range manifest.Layers {
		if dgstStr, ok := layer.Annotations[types.NydusAnnotationSourceDigest]; ok && dgstStr != "" {
			if dgst, err := digest.Parse(dgstStr); err == nil {
				add(types.SourceRef{
					Digest:      dgst,
					Kind:        types.SourceRefLayer,
					Annotations: map[string]string{"layerDigest": layer.Digest.String()},
				})
			}
		}
	}
	if len(configBlob) > 0 {
		var cfg struct {
			Annotations map[string]string `json:"annotations"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if dgstStr, ok := cfg.Annotations[types.NydusAnnotationSourceDigest]; ok && dgstStr != "" {
				if dgst, err := digest.Parse(dgstStr); err == nil {
					add(types.SourceRef{Digest: dgst, Kind: types.SourceRefConfig})
				}
			}
		}
	}
	return refs, nil
}

func validateNydus(_ context.Context, manifest ocispec.Manifest) error {
	var blobCount, bootstrapCount int
	for _, layer := range manifest.Layers {
		switch layer.MediaType {
		case types.NydusLayerMediaType:
			blobCount++
		case types.NydusBootstrapMediaType:
			bootstrapCount++
		}
	}
	if blobCount == 0 && bootstrapCount == 0 {
		return fmt.Errorf("nydus: no nydus layers found")
	}
	if bootstrapCount > 1 {
		return fmt.Errorf("nydus: multiple bootstrap layers (%d)", bootstrapCount)
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// SOCI inline implementation
// ────────────────────────────────────────────────────────────────────────────

func detectSOCI(_ context.Context, manifest ocispec.Manifest, _ []byte) (types.AccelType, bool, error) {
	if manifest.ArtifactType == types.SOCIArtifactType {
		return types.AccelSOCI, true, nil
	}
	if manifest.Config.MediaType == types.SOCIArtifactType {
		return types.AccelSOCI, true, nil
	}
	if _, ok := manifest.Annotations[types.SOCIAnnotationIndexDigest]; ok {
		return types.AccelSOCI, true, nil
	}
	return types.AccelUnknown, false, nil
}

func extractSOCIRefs(_ context.Context, manifest ocispec.Manifest, _ []byte) ([]types.SourceRef, error) {
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
	if manifest.Subject != nil {
		add(types.SourceRef{Digest: manifest.Subject.Digest, MediaType: manifest.Subject.MediaType, Kind: types.SourceRefManifest})
	}
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceDigest]; ok {
		if dgst, err := digest.Parse(dgstStr); err == nil {
			add(types.SourceRef{Digest: dgst, Kind: types.SourceRefManifest})
		}
	}
	for _, layer := range manifest.Layers {
		for _, key := range []string{"com.amazon.soci.layer-digest"} {
			if dgstStr, ok := layer.Annotations[key]; ok {
				if dgst, err := digest.Parse(dgstStr); err == nil {
					add(types.SourceRef{Digest: dgst, Kind: types.SourceRefLayer})
				}
			}
		}
	}
	return refs, nil
}

func validateSOCI(_ context.Context, manifest ocispec.Manifest) error {
	if manifest.Subject == nil {
		return fmt.Errorf("soci: missing subject field")
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// eStargz inline implementation
// ────────────────────────────────────────────────────────────────────────────

func detectEstargz(_ context.Context, manifest ocispec.Manifest, configBlob []byte) (types.AccelType, bool, error) {
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[types.StargzAnnotationTOCDigest]; ok {
			return types.AccelEstargz, true, nil
		}
	}
	if _, ok := manifest.Annotations[types.StargzAnnotationTOCDigest]; ok {
		return types.AccelEstargz, true, nil
	}
	if len(configBlob) > 0 {
		var cfg struct {
			Annotations map[string]string `json:"annotations"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if _, ok := cfg.Annotations[types.StargzAnnotationTOCDigest]; ok {
				return types.AccelEstargz, true, nil
			}
		}
	}
	return types.AccelUnknown, false, nil
}

func extractEstargzRefs(_ context.Context, manifest ocispec.Manifest, _ []byte) ([]types.SourceRef, error) {
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
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceDigest]; ok {
		if dgst, err := digest.Parse(dgstStr); err == nil {
			add(types.SourceRef{Digest: dgst, Kind: types.SourceRefManifest})
		}
	}
	if manifest.Subject != nil {
		add(types.SourceRef{Digest: manifest.Subject.Digest, Kind: types.SourceRefManifest})
	}
	for _, layer := range manifest.Layers {
		if dgstStr, ok := layer.Annotations[types.StargzAnnotationUncompressed]; ok {
			if dgst, err := digest.Parse(dgstStr); err == nil {
				add(types.SourceRef{Digest: dgst, Kind: types.SourceRefLayer})
			}
		}
	}
	return refs, nil
}

func validateEstargz(_ context.Context, manifest ocispec.Manifest) error {
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[types.StargzAnnotationTOCDigest]; ok {
			return nil
		}
	}
	return fmt.Errorf("estargz: no layer with TOC digest annotation")
}

// ────────────────────────────────────────────────────────────────────────────
// OverlayBD inline implementation
// ────────────────────────────────────────────────────────────────────────────

func detectOverlayBD(_ context.Context, manifest ocispec.Manifest, configBlob []byte) (types.AccelType, bool, error) {
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[types.OverlayBDAnnotationLayer]; ok {
			return types.AccelOverlayBD, true, nil
		}
		if layer.MediaType == types.OverlayBDLayerMediaType {
			return types.AccelOverlayBD, true, nil
		}
	}
	if len(configBlob) > 0 {
		var cfg struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"config"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if _, ok := cfg.Config.Labels[types.OverlayBDAnnotationVersion]; ok {
				return types.AccelOverlayBD, true, nil
			}
		}
	}
	return types.AccelUnknown, false, nil
}

func extractOverlayBDRefs(_ context.Context, manifest ocispec.Manifest, _ []byte) ([]types.SourceRef, error) {
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
	if dgstStr, ok := manifest.Annotations[types.AnnotationSourceDigest]; ok {
		if dgst, err := digest.Parse(dgstStr); err == nil {
			add(types.SourceRef{Digest: dgst, Kind: types.SourceRefManifest})
		}
	}
	if manifest.Subject != nil {
		add(types.SourceRef{Digest: manifest.Subject.Digest, Kind: types.SourceRefManifest})
	}
	return refs, nil
}

func validateOverlayBD(_ context.Context, manifest ocispec.Manifest) error {
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[types.OverlayBDAnnotationLayer]; ok {
			return nil
		}
		if layer.MediaType == types.OverlayBDLayerMediaType {
			return nil
		}
	}
	return fmt.Errorf("overlaybd: no OverlayBD layer annotation or media type found")
}

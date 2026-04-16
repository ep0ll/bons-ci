// Package soci implements the AccelHandler for Amazon SOCI (Seekable OCI)
// image acceleration.
//
// SOCI works differently from Nydus and eStargz: instead of converting layers,
// it creates a separate OCI *artifact* (a SOCI Index) that sits alongside the
// original image. The SOCI Index references the original manifest via the OCI
// 1.1 `subject` field and contains per-layer zTOC (Zstandard Table-of-Contents)
// entries.
//
// A SOCI image is therefore:
//   - An unmodified original OCI image
//   - A SOCI Index artifact (artifactType = "application/vnd.amazon.soci.index.v1+json")
//     with `subject` pointing at the original image manifest
//
// In this registry a SOCI entry is stored as the SOCI Index manifest. The
// source digest is always extracted from the `subject` field.
//
// Detection heuristics:
//  1. manifest.ArtifactType == SOCIArtifactType
//  2. manifest.Config.MediaType == SOCIArtifactType
//  3. manifest.Annotations contains SOCIAnnotationIndexDigest
package soci

import (
	"context"
	"fmt"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

const (
	SOCIArtifactType  = types.SOCIArtifactType
	SOCIIndexDigestAn = types.SOCIAnnotationIndexDigest
	SOCILayerMedia    = types.SOCILayerMediaType
)

// Handler implements types.AccelHandler for SOCI images.
type Handler struct{}

func New() *Handler { return &Handler{} }

func (h *Handler) Name() types.AccelType { return types.AccelSOCI }

// Detect returns true if the manifest is a SOCI Index artifact.
func (h *Handler) Detect(_ context.Context, manifest ocispec.Manifest, _ []byte) (types.AccelType, bool, error) {
	// Primary: artifactType field (OCI 1.1)
	if manifest.ArtifactType == SOCIArtifactType {
		return types.AccelSOCI, true, nil
	}
	// Fallback: config media type carries artifact type in older ORAS drafts
	if manifest.Config.MediaType == SOCIArtifactType {
		return types.AccelSOCI, true, nil
	}
	// Fallback: annotation
	if _, ok := manifest.Annotations[SOCIIndexDigestAn]; ok {
		return types.AccelSOCI, true, nil
	}
	return types.AccelUnknown, false, nil
}

// ExtractSourceRefs extracts source digests from a SOCI Index manifest.
// SOCI always uses the OCI 1.1 subject field; additionally the per-layer
// ztoc entries may carry original layer digests via annotations.
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

	// 1. Subject field (mandatory for SOCI)
	if manifest.Subject != nil {
		add(types.SourceRef{
			Digest:    manifest.Subject.Digest,
			MediaType: manifest.Subject.MediaType,
			Kind:      types.SourceRefManifest,
			Annotations: map[string]string{
				"origin": "manifest.subject",
			},
		})
	}

	// 2. Canonical annotation
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

	// 3. Per-layer (ztoc) annotations
	for _, layer := range manifest.Layers {
		// Each SOCI ztoc layer may carry the original layer digest
		for _, key := range []string{
			"com.amazon.soci.layer-digest",
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

// Validate checks that the SOCI manifest has a subject and artifact type.
func (h *Handler) Validate(_ context.Context, manifest ocispec.Manifest) error {
	if manifest.ArtifactType != SOCIArtifactType && manifest.Config.MediaType != SOCIArtifactType {
		return fmt.Errorf("soci: manifest artifactType must be %q (got %q)", SOCIArtifactType, manifest.ArtifactType)
	}
	if manifest.Subject == nil {
		return fmt.Errorf("soci: SOCI Index manifest must have a subject referencing the source image")
	}
	if manifest.Subject.Digest == "" {
		return fmt.Errorf("soci: subject digest is empty")
	}
	return nil
}

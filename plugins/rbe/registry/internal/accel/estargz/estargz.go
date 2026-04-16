// Package estargz implements the AccelHandler for eStargz (Stargz Snapshotter)
// image acceleration.
//
// eStargz (Extended Stargz) are gzip-compressed tarballs with an appended
// Stargz Table-of-Contents (TOC) that enables random access to individual
// files. The TOC digest is stored as a layer annotation.
//
// Detection heuristics:
//  1. Any layer annotation contains StargzAnnotationTOCDigest.
//  2. Manifest or config annotation contains stargz source image reference.
package estargz

import (
	"context"
	"encoding/json"
	"fmt"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

const (
	TOCDigestAnnotation     = types.StargzAnnotationTOCDigest
	UncompressedAnnotation  = types.StargzAnnotationUncompressed
	SourceImageAnnotation   = "containerd.io/snapshot/stargz/source.image"
)

// Handler implements types.AccelHandler for eStargz images.
type Handler struct{}

// New returns a ready-to-use eStargz AccelHandler.
func New() *Handler { return &Handler{} }

func (h *Handler) Name() types.AccelType { return types.AccelEstargz }

// Detect returns true if any layer carries the Stargz TOC digest annotation.
func (h *Handler) Detect(_ context.Context, manifest ocispec.Manifest, configBlob []byte) (types.AccelType, bool, error) {
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[TOCDigestAnnotation]; ok {
			return types.AccelEstargz, true, nil
		}
	}
	// Check manifest-level annotations
	if _, ok := manifest.Annotations[TOCDigestAnnotation]; ok {
		return types.AccelEstargz, true, nil
	}
	// Config annotation
	if len(configBlob) > 0 {
		var cfg struct {
			Annotations map[string]string `json:"annotations"`
		}
		if err := json.Unmarshal(configBlob, &cfg); err == nil {
			if _, ok := cfg.Annotations[TOCDigestAnnotation]; ok {
				return types.AccelEstargz, true, nil
			}
		}
	}
	return types.AccelUnknown, false, nil
}

// ExtractSourceRefs extracts source digests from eStargz manifests.
// eStargz does not carry per-layer original digests natively, so we rely on:
//  1. Manifest/config annotation "org.accelregistry.source.digest"
//  2. OCI 1.1 subject
//  3. Per-layer "containerd.io/uncompressed" annotation (original layer digest)
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

	// 1. Canonical source annotation on manifest
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

	// 3. Per-layer uncompressed digests (original layer content)
	for _, layer := range manifest.Layers {
		if dgstStr, ok := layer.Annotations[UncompressedAnnotation]; ok {
			if dgst, err := digest.Parse(dgstStr); err == nil {
				add(types.SourceRef{
					Digest: dgst,
					Kind:   types.SourceRefLayer,
					Annotations: map[string]string{
						"origin":      "layer.annotation." + UncompressedAnnotation,
						"layerDigest": layer.Digest.String(),
					},
				})
			}
		}
	}

	return refs, nil
}

// Validate checks that the eStargz manifest has at least one layer with
// a TOC digest annotation and a source reference.
func (h *Handler) Validate(_ context.Context, manifest ocispec.Manifest) error {
	hasTOC := false
	for _, layer := range manifest.Layers {
		if _, ok := layer.Annotations[TOCDigestAnnotation]; ok {
			hasTOC = true
			break
		}
	}
	if !hasTOC {
		return fmt.Errorf("estargz: manifest has no layer with TOC digest annotation (%s)", TOCDigestAnnotation)
	}
	_, hasCanonical := manifest.Annotations[types.AnnotationSourceDigest]
	hasSubject := manifest.Subject != nil
	if !hasCanonical && !hasSubject {
		return fmt.Errorf("estargz: manifest lacks source digest reference (annotation or subject)")
	}
	return nil
}

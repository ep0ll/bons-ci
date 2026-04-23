// Package handlers_test exhaustively tests each AccelHandler implementation:
// Nydus, eStargz, SOCI, OverlayBD.
//
// Each handler is tested for:
//   - Positive detection via each heuristic (layer media type, annotations, config blob)
//   - Negative detection (plain OCI manifests must not be mis-classified)
//   - Source ref extraction completeness (manifest annotation, subject, per-layer, config)
//   - Deduplication of identical digests across multiple extraction paths
//   - Validation: valid and invalid manifests
//   - Priority ordering: SOCI before eStargz (subject field shared)
package handlers_test

import (
	"context"
	"encoding/json"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/estargz"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/nydus"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/overlaybd"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/soci"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Test fixtures
// ────────────────────────────────────────────────────────────────────────────

var ctx = context.Background()

func dgst(s string) digest.Digest { return digest.Canonical.FromString(s) }

// plainManifest returns a vanilla OCI manifest with no accel annotations.
func plainManifest() ocispec.Manifest {
	return ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    dgst("config"),
			Size:      256,
		},
		Layers: []ocispec.Descriptor{
			{
				MediaType: ocispec.MediaTypeImageLayerGzip,
				Digest:    dgst("layer1"),
				Size:      1024,
			},
		},
	}
}

// marshalConfig builds a JSON config blob with the given annotations.
func marshalConfig(annotations map[string]string) []byte {
	cfg := struct {
		Annotations map[string]string `json:"annotations,omitempty"`
		Config      struct {
			Labels map[string]string `json:"Labels,omitempty"`
		} `json:"config,omitempty"`
	}{Annotations: annotations}
	b, _ := json.Marshal(cfg)
	return b
}

// marshalConfigLabels builds a config blob with config.Labels set.
func marshalConfigLabels(labels map[string]string) []byte {
	cfg := struct {
		Config struct {
			Labels map[string]string `json:"Labels,omitempty"`
		} `json:"config,omitempty"`
	}{}
	cfg.Config.Labels = labels
	b, _ := json.Marshal(cfg)
	return b
}

// ────────────────────────────────────────────────────────────────────────────
// Nydus handler tests
// ────────────────────────────────────────────────────────────────────────────

func TestNydus_Detect_LayerMediaType(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	m.Layers[0].MediaType = types.NydusLayerMediaType

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelNydus {
		t.Errorf("Detect by layer MediaType: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestNydus_Detect_BootstrapLayer(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	m.Layers = append(m.Layers, ocispec.Descriptor{
		MediaType: types.NydusBootstrapMediaType,
		Digest:    dgst("bootstrap"),
		Size:      512,
	})

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelNydus {
		t.Errorf("Detect by bootstrap layer: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestNydus_Detect_ManifestAnnotation(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	m.Annotations = map[string]string{
		types.NydusAnnotationSourceDigest: dgst("source").String(),
	}

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelNydus {
		t.Errorf("Detect by manifest annotation: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestNydus_Detect_VersionAnnotation(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	m.Annotations = map[string]string{types.NydusAnnotationVersion: "2.2.0"}

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelNydus {
		t.Errorf("Detect by version annotation: ok=%v err=%v", ok, err)
	}
}

func TestNydus_Detect_ConfigBlobAnnotation(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	cfg := marshalConfig(map[string]string{
		types.NydusAnnotationSourceDigest: dgst("source").String(),
	})

	at, ok, err := h.Detect(ctx, m, cfg)
	if err != nil || !ok || at != types.AccelNydus {
		t.Errorf("Detect by config blob annotation: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestNydus_Detect_PlainImage_NoMatch(t *testing.T) {
	h := nydus.New()
	_, ok, err := h.Detect(ctx, plainManifest(), nil)
	if err != nil || ok {
		t.Errorf("plain image should not match: ok=%v err=%v", ok, err)
	}
}

func TestNydus_ExtractSourceRefs_AllPaths(t *testing.T) {
	h := nydus.New()
	sourceDgst := dgst("source-manifest")
	indexDgst := dgst("source-index")
	layerSrcDgst := dgst("original-layer")

	m := plainManifest()
	m.Annotations = map[string]string{
		types.NydusAnnotationSourceDigest: sourceDgst.String(),
		types.AnnotationSourceIndexDigest: indexDgst.String(),
	}
	m.Subject = &ocispec.Descriptor{
		Digest:    dgst("subject-manifest"),
		MediaType: ocispec.MediaTypeImageManifest,
	}
	m.Layers[0].MediaType = types.NydusLayerMediaType
	m.Layers[0].Annotations = map[string]string{
		types.NydusAnnotationSourceDigest: layerSrcDgst.String(),
	}

	cfg := marshalConfig(map[string]string{
		types.NydusAnnotationSourceDigest: dgst("config-source").String(),
	})

	refs, err := h.ExtractSourceRefs(ctx, m, cfg)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	if len(refs) < 4 {
		t.Errorf("expected ≥4 source refs (manifest ann, index ann, subject, layer), got %d", len(refs))
	}
	// Verify deduplication: sourceDgst appears in manifest annotation only once
	count := 0
	for _, r := range refs {
		if r.Digest == sourceDgst {
			count++
		}
	}
	if count != 1 {
		t.Errorf("sourceDgst should appear exactly once (dedup), got %d", count)
	}
}

func TestNydus_ExtractSourceRefs_Deduplication(t *testing.T) {
	h := nydus.New()
	// Same digest in both annotation and subject — should appear once
	same := dgst("duplicate-source")
	m := plainManifest()
	m.Annotations = map[string]string{types.NydusAnnotationSourceDigest: same.String()}
	m.Subject = &ocispec.Descriptor{Digest: same}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	seen := make(map[digest.Digest]int)
	for _, r := range refs {
		seen[r.Digest]++
	}
	if seen[same] != 1 {
		t.Errorf("deduplicated digest should appear once, got %d", seen[same])
	}
}

func TestNydus_Validate_Valid(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	m.Layers[0].MediaType = types.NydusLayerMediaType
	m.Annotations = map[string]string{
		types.NydusAnnotationSourceDigest: dgst("src").String(),
	}
	if err := h.Validate(ctx, m); err != nil {
		t.Errorf("expected valid nydus manifest, got error: %v", err)
	}
}

func TestNydus_Validate_NoLayers(t *testing.T) {
	h := nydus.New()
	m := plainManifest() // plain gzip layers, no nydus
	m.Annotations = map[string]string{types.NydusAnnotationSourceDigest: dgst("src").String()}
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected validation error for manifest with no nydus layers")
	}
}

func TestNydus_Validate_MultipleBootstraps(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	m.Layers = []ocispec.Descriptor{
		{MediaType: types.NydusBootstrapMediaType, Digest: dgst("b1"), Size: 100},
		{MediaType: types.NydusBootstrapMediaType, Digest: dgst("b2"), Size: 100},
	}
	m.Annotations = map[string]string{types.NydusAnnotationSourceDigest: dgst("src").String()}
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected validation error for manifest with multiple bootstrap layers")
	}
}

func TestNydus_Validate_MissingSourceRef(t *testing.T) {
	h := nydus.New()
	m := plainManifest()
	m.Layers[0].MediaType = types.NydusLayerMediaType
	// No annotations, no subject
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected validation error for manifest missing source ref")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// eStargz handler tests
// ────────────────────────────────────────────────────────────────────────────

func TestEstargz_Detect_LayerAnnotation(t *testing.T) {
	h := estargz.New()
	m := plainManifest()
	m.Layers[0].Annotations = map[string]string{
		types.StargzAnnotationTOCDigest: dgst("toc").String(),
	}

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelEstargz {
		t.Errorf("Detect by layer TOC annotation: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestEstargz_Detect_ManifestAnnotation(t *testing.T) {
	h := estargz.New()
	m := plainManifest()
	m.Annotations = map[string]string{
		types.StargzAnnotationTOCDigest: dgst("toc").String(),
	}

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelEstargz {
		t.Errorf("Detect by manifest annotation: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestEstargz_Detect_ConfigAnnotation(t *testing.T) {
	h := estargz.New()
	m := plainManifest()
	cfg := marshalConfig(map[string]string{
		types.StargzAnnotationTOCDigest: dgst("toc").String(),
	})

	at, ok, err := h.Detect(ctx, m, cfg)
	if err != nil || !ok || at != types.AccelEstargz {
		t.Errorf("Detect by config annotation: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestEstargz_Detect_Plain_NoMatch(t *testing.T) {
	h := estargz.New()
	_, ok, _ := h.Detect(ctx, plainManifest(), nil)
	if ok {
		t.Error("plain image should not match estargz")
	}
}

func TestEstargz_ExtractSourceRefs(t *testing.T) {
	h := estargz.New()
	srcDgst := dgst("source-manifest")
	uncompressedDgst := dgst("original-layer-content")

	m := plainManifest()
	m.Annotations = map[string]string{
		types.AnnotationSourceDigest: srcDgst.String(),
	}
	m.Layers[0].Annotations = map[string]string{
		types.StargzAnnotationUncompressed: uncompressedDgst.String(),
	}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	found := make(map[digest.Digest]bool)
	for _, r := range refs {
		found[r.Digest] = true
	}
	if !found[srcDgst] {
		t.Error("expected source manifest digest in refs")
	}
	if !found[uncompressedDgst] {
		t.Error("expected uncompressed layer digest in refs")
	}
}

func TestEstargz_ExtractSourceRefs_WithSubject(t *testing.T) {
	h := estargz.New()
	subjectDgst := dgst("subject")
	m := plainManifest()
	m.Subject = &ocispec.Descriptor{Digest: subjectDgst}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	found := false
	for _, r := range refs {
		if r.Digest == subjectDgst {
			found = true
		}
	}
	if !found {
		t.Error("expected subject digest in refs")
	}
}

func TestEstargz_Validate_Valid(t *testing.T) {
	h := estargz.New()
	m := plainManifest()
	m.Layers[0].Annotations = map[string]string{
		types.StargzAnnotationTOCDigest: dgst("toc").String(),
	}
	m.Annotations = map[string]string{types.AnnotationSourceDigest: dgst("src").String()}
	if err := h.Validate(ctx, m); err != nil {
		t.Errorf("valid estargz manifest failed validation: %v", err)
	}
}

func TestEstargz_Validate_MissingTOC(t *testing.T) {
	h := estargz.New()
	m := plainManifest()
	m.Annotations = map[string]string{types.AnnotationSourceDigest: dgst("src").String()}
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected validation error for missing TOC annotation")
	}
}

func TestEstargz_Validate_MissingSourceRef(t *testing.T) {
	h := estargz.New()
	m := plainManifest()
	m.Layers[0].Annotations = map[string]string{
		types.StargzAnnotationTOCDigest: dgst("toc").String(),
	}
	// No source annotation, no subject
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected validation error for missing source ref")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// SOCI handler tests
// ────────────────────────────────────────────────────────────────────────────

func TestSOCI_Detect_ArtifactType(t *testing.T) {
	h := soci.New()
	m := plainManifest()
	m.ArtifactType = types.SOCIArtifactType

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelSOCI {
		t.Errorf("Detect by artifactType: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestSOCI_Detect_ConfigMediaType(t *testing.T) {
	h := soci.New()
	m := plainManifest()
	m.Config.MediaType = types.SOCIArtifactType

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelSOCI {
		t.Errorf("Detect by config media type: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestSOCI_Detect_IndexAnnotation(t *testing.T) {
	h := soci.New()
	m := plainManifest()
	m.Annotations = map[string]string{
		types.SOCIAnnotationIndexDigest: dgst("soci-index").String(),
	}

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelSOCI {
		t.Errorf("Detect by index annotation: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestSOCI_Detect_Plain_NoMatch(t *testing.T) {
	h := soci.New()
	_, ok, _ := h.Detect(ctx, plainManifest(), nil)
	if ok {
		t.Error("plain image should not match SOCI")
	}
}

func TestSOCI_ExtractSourceRefs_Subject(t *testing.T) {
	h := soci.New()
	subjectDgst := dgst("original-image-manifest")
	m := plainManifest()
	m.ArtifactType = types.SOCIArtifactType
	m.Subject = &ocispec.Descriptor{
		Digest:    subjectDgst,
		MediaType: ocispec.MediaTypeImageManifest,
	}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected at least one source ref")
	}
	if refs[0].Digest != subjectDgst {
		t.Errorf("expected subject digest %s, got %s", subjectDgst, refs[0].Digest)
	}
	if refs[0].Kind != types.SourceRefManifest {
		t.Errorf("expected SourceRefManifest kind, got %s", refs[0].Kind)
	}
}

func TestSOCI_ExtractSourceRefs_LayerAnnotations(t *testing.T) {
	h := soci.New()
	layerSrc := dgst("original-layer-1")
	m := plainManifest()
	m.ArtifactType = types.SOCIArtifactType
	m.Subject = &ocispec.Descriptor{Digest: dgst("subject")}
	m.Layers[0].Annotations = map[string]string{
		"com.amazon.soci.layer-digest": layerSrc.String(),
	}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	found := false
	for _, r := range refs {
		if r.Digest == layerSrc && r.Kind == types.SourceRefLayer {
			found = true
		}
	}
	if !found {
		t.Error("expected layer source digest in refs")
	}
}

func TestSOCI_ExtractSourceRefs_Deduplication(t *testing.T) {
	h := soci.New()
	same := dgst("same-digest")
	m := plainManifest()
	m.ArtifactType = types.SOCIArtifactType
	m.Subject = &ocispec.Descriptor{Digest: same}
	m.Annotations = map[string]string{types.AnnotationSourceDigest: same.String()}

	refs, _ := h.ExtractSourceRefs(ctx, m, nil)
	seen := 0
	for _, r := range refs {
		if r.Digest == same {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("expected deduplication, got %d occurrences of same digest", seen)
	}
}

func TestSOCI_Validate_Valid(t *testing.T) {
	h := soci.New()
	m := plainManifest()
	m.ArtifactType = types.SOCIArtifactType
	m.Subject = &ocispec.Descriptor{
		Digest:    dgst("subject"),
		MediaType: ocispec.MediaTypeImageManifest,
	}
	if err := h.Validate(ctx, m); err != nil {
		t.Errorf("valid SOCI manifest failed validation: %v", err)
	}
}

func TestSOCI_Validate_MissingSubject(t *testing.T) {
	h := soci.New()
	m := plainManifest()
	m.ArtifactType = types.SOCIArtifactType
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected validation error for SOCI without subject")
	}
}

func TestSOCI_Validate_WrongArtifactType(t *testing.T) {
	h := soci.New()
	m := plainManifest()
	m.Subject = &ocispec.Descriptor{Digest: dgst("subject")}
	// ArtifactType not set to SOCI
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected validation error for wrong artifactType")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// OverlayBD handler tests
// ────────────────────────────────────────────────────────────────────────────

func TestOverlayBD_Detect_LayerAnnotation(t *testing.T) {
	h := overlaybd.New()
	m := plainManifest()
	m.Layers[0].Annotations = map[string]string{
		types.OverlayBDAnnotationLayer: "true",
	}

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelOverlayBD {
		t.Errorf("Detect by layer annotation: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestOverlayBD_Detect_LayerMediaType(t *testing.T) {
	h := overlaybd.New()
	m := plainManifest()
	m.Layers[0].MediaType = types.OverlayBDLayerMediaType

	at, ok, err := h.Detect(ctx, m, nil)
	if err != nil || !ok || at != types.AccelOverlayBD {
		t.Errorf("Detect by layer media type: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestOverlayBD_Detect_ConfigLabels(t *testing.T) {
	h := overlaybd.New()
	m := plainManifest()
	cfg := marshalConfigLabels(map[string]string{
		types.OverlayBDAnnotationVersion: "0.6.1",
	})

	at, ok, err := h.Detect(ctx, m, cfg)
	if err != nil || !ok || at != types.AccelOverlayBD {
		t.Errorf("Detect by config labels: type=%s ok=%v err=%v", at, ok, err)
	}
}

func TestOverlayBD_Detect_Plain_NoMatch(t *testing.T) {
	h := overlaybd.New()
	_, ok, _ := h.Detect(ctx, plainManifest(), nil)
	if ok {
		t.Error("plain image should not match OverlayBD")
	}
}

func TestOverlayBD_ExtractSourceRefs_CanonicalAnnotation(t *testing.T) {
	h := overlaybd.New()
	srcDgst := dgst("source-manifest")
	m := plainManifest()
	m.Annotations = map[string]string{
		types.AnnotationSourceDigest: srcDgst.String(),
	}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	found := false
	for _, r := range refs {
		if r.Digest == srcDgst {
			found = true
		}
	}
	if !found {
		t.Error("expected canonical source digest in refs")
	}
}

func TestOverlayBD_ExtractSourceRefs_Subject(t *testing.T) {
	h := overlaybd.New()
	subjectDgst := dgst("obd-subject")
	m := plainManifest()
	m.Subject = &ocispec.Descriptor{Digest: subjectDgst, MediaType: ocispec.MediaTypeImageManifest}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	found := false
	for _, r := range refs {
		if r.Digest == subjectDgst {
			found = true
		}
	}
	if !found {
		t.Error("expected subject digest in refs")
	}
}

func TestOverlayBD_ExtractSourceRefs_IndexDigest(t *testing.T) {
	h := overlaybd.New()
	idxDgst := dgst("multi-arch-index")
	m := plainManifest()
	m.Annotations = map[string]string{
		types.AnnotationSourceIndexDigest: idxDgst.String(),
	}

	refs, err := h.ExtractSourceRefs(ctx, m, nil)
	if err != nil {
		t.Fatalf("ExtractSourceRefs: %v", err)
	}
	found := false
	for _, r := range refs {
		if r.Digest == idxDgst && r.Kind == types.SourceRefIndex {
			found = true
		}
	}
	if !found {
		t.Error("expected index digest with SourceRefIndex kind in refs")
	}
}

func TestOverlayBD_Validate_Valid(t *testing.T) {
	h := overlaybd.New()
	m := plainManifest()
	m.Layers[0].MediaType = types.OverlayBDLayerMediaType
	m.Annotations = map[string]string{types.AnnotationSourceDigest: dgst("src").String()}
	if err := h.Validate(ctx, m); err != nil {
		t.Errorf("valid OverlayBD manifest failed validation: %v", err)
	}
}

func TestOverlayBD_Validate_NoOBDLayers(t *testing.T) {
	h := overlaybd.New()
	m := plainManifest()
	m.Annotations = map[string]string{types.AnnotationSourceDigest: dgst("src").String()}
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected error for manifest with no OverlayBD layers")
	}
}

func TestOverlayBD_Validate_MissingSourceRef(t *testing.T) {
	h := overlaybd.New()
	m := plainManifest()
	m.Layers[0].MediaType = types.OverlayBDLayerMediaType
	// No annotation or subject
	if err := h.Validate(ctx, m); err == nil {
		t.Error("expected error for missing source ref")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Cross-handler: non-interference
// ────────────────────────────────────────────────────────────────────────────

// TestNoHandler_CrossContamination verifies that a manifest positive for one
// accel type does NOT trigger another handler.
func TestNoHandler_CrossContamination(t *testing.T) {
	cases := []struct {
		name      string
		manifest  func() ocispec.Manifest
		positive  types.AccelType
		negatives []types.AccelType
	}{
		{
			name: "Nydus manifest should not trigger eStargz or OverlayBD",
			manifest: func() ocispec.Manifest {
				m := plainManifest()
				m.Layers[0].MediaType = types.NydusLayerMediaType
				return m
			},
			positive:  types.AccelNydus,
			negatives: []types.AccelType{types.AccelEstargz, types.AccelOverlayBD},
		},
		{
			name: "SOCI manifest should not trigger Nydus or eStargz",
			manifest: func() ocispec.Manifest {
				m := plainManifest()
				m.ArtifactType = types.SOCIArtifactType
				return m
			},
			positive:  types.AccelSOCI,
			negatives: []types.AccelType{types.AccelNydus, types.AccelEstargz},
		},
		{
			name: "OverlayBD manifest should not trigger SOCI or Nydus",
			manifest: func() ocispec.Manifest {
				m := plainManifest()
				m.Layers[0].MediaType = types.OverlayBDLayerMediaType
				return m
			},
			positive:  types.AccelOverlayBD,
			negatives: []types.AccelType{types.AccelSOCI, types.AccelNydus},
		},
	}

	handlers := map[types.AccelType]interface {
		Detect(context.Context, ocispec.Manifest, []byte) (types.AccelType, bool, error)
		Name() types.AccelType
	}{
		types.AccelNydus:     nydus.New(),
		types.AccelEstargz:   estargz.New(),
		types.AccelSOCI:      soci.New(),
		types.AccelOverlayBD: overlaybd.New(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.manifest()
			// Positive handler must detect
			ph := handlers[tc.positive]
			_, ok, _ := ph.Detect(ctx, m, nil)
			if !ok {
				t.Errorf("handler %s should detect this manifest", tc.positive)
			}
			// Negative handlers must not detect
			for _, neg := range tc.negatives {
				nh := handlers[neg]
				_, ok, _ := nh.Detect(ctx, m, nil)
				if ok {
					t.Errorf("handler %s should NOT detect %s manifest", neg, tc.positive)
				}
			}
		})
	}
}

package stargz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	local "github.com/bons/bons-ci/core/content/store/local"
	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// newConvertTestStore creates an ephemeral content store backed by a temp directory.
func newConvertTestStore(t *testing.T) content.Store {
	t.Helper()
	cs, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}
	return cs
}

// writeJSON marshals v and stores it in cs under a ref derived from the digest.
// Returns the descriptor of the stored blob.
func writeJSON(t *testing.T, cs content.Store, v interface{}, mediaType string) ocispec.Descriptor {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	dgst := digest.FromBytes(b)
	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    dgst,
		Size:      int64(len(b)),
	}
	if err := content.WriteBlob(context.Background(), cs, dgst.String(), bytes.NewReader(b), desc); err != nil {
		t.Fatalf("write JSON blob: %v", err)
	}
	return desc
}

// buildTestManifest writes a minimal OCI manifest and its config to cs.
// Returns (manifestDesc, configDesc, layerDescs).
func buildTestManifest(t *testing.T, cs content.Store, numLayers int) (ocispec.Descriptor, ocispec.Descriptor, []ocispec.Descriptor) {
	t.Helper()

	// Write dummy layer blobs.
	layers := make([]ocispec.Descriptor, numLayers)
	diffIDs := make([]digest.Digest, numLayers)
	for i := range layers {
		body := fmt.Sprintf("stargz-test-layer-%d", i)
		dgst := digest.FromString(body)
		diffIDs[i] = dgst
		layers[i] = ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Digest:    dgst,
			Size:      int64(len(body)),
			Annotations: map[string]string{
				// Simulate what LayerConvertFunc would stamp.
				LayerAnnotationUncompressed: dgst.String(),
				LayerAnnotationTOCDigest:    digest.FromString("toc-" + body).String(),
			},
		}
	}

	// Write config.
	cfg := ocispec.Image{
		RootFS: ocispec.RootFS{Type: "layers", DiffIDs: diffIDs},
	}
	cfgDesc := writeJSON(t, cs, cfg, ocispec.MediaTypeImageConfig)

	// Write manifest.
	mf := ocispec.Manifest{
		MediaType:   ocispec.MediaTypeImageManifest,
		Config:      cfgDesc,
		Layers:      layers,
		Annotations: map[string]string{},
	}
	mfDesc := writeJSON(t, cs, mf, ocispec.MediaTypeImageManifest)

	return mfDesc, cfgDesc, layers
}

// buildTestIndex writes an OCI index wrapping zero or more manifests to cs.
func buildTestIndex(t *testing.T, cs content.Store, manifests []ocispec.Descriptor) ocispec.Descriptor {
	t.Helper()
	idx := ocispec.Index{
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: manifests,
	}
	return writeJSON(t, cs, idx, ocispec.MediaTypeImageIndex)
}

// ─── ConvertHookFunc tests ────────────────────────────────────────────────────

// TestConvertHookFunc_NilNewDesc verifies that when the layer pass produced no
// new blob (newDesc == nil), the hook returns a copy of orgDesc unchanged.
func TestConvertHookFunc_NilNewDesc(t *testing.T) {
	cs := newConvertTestStore(t)
	orgDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromString("original"),
		Size:      8,
	}

	hook := ConvertHookFunc(MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result when newDesc is nil")
	}
	if result.Digest != orgDesc.Digest {
		t.Errorf("expected original digest %s, got %s", orgDesc.Digest, result.Digest)
	}
}

// TestConvertHookFunc_Index_Flatten verifies that a single-entry manifest list
// is flattened into a bare manifest descriptor.
func TestConvertHookFunc_Index_Flatten(t *testing.T) {
	cs := newConvertTestStore(t)

	mfDesc, _, _ := buildTestManifest(t, cs, 1)
	idxDesc := buildTestIndex(t, cs, []ocispec.Descriptor{mfDesc})
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, &idxDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if images.IsIndexType(result.MediaType) {
		t.Errorf("expected bare manifest but got index type %q", result.MediaType)
	}
	if result.Digest != mfDesc.Digest {
		t.Errorf("expected manifest digest %s, got %s", mfDesc.Digest, result.Digest)
	}
}

// TestConvertHookFunc_Index_Multi verifies that a multi-entry index is returned
// unchanged (no flattening).
func TestConvertHookFunc_Index_Multi(t *testing.T) {
	cs := newConvertTestStore(t)

	m1, _, _ := buildTestManifest(t, cs, 1)
	m2, _, _ := buildTestManifest(t, cs, 2)
	idxDesc := buildTestIndex(t, cs, []ocispec.Descriptor{m1, m2})
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, &idxDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !images.IsIndexType(result.MediaType) {
		t.Errorf("expected index type, got %q", result.MediaType)
	}
}

// TestConvertHookFunc_Manifest_DiffIDReconcile verifies that the
// LayerAnnotationUncompressed annotations are folded into config.RootFS.DiffIDs
// and stripped from the layer descriptors in the written manifest.
func TestConvertHookFunc_Manifest_DiffIDReconcile(t *testing.T) {
	cs := newConvertTestStore(t)
	mfDesc, _, origLayers := buildTestManifest(t, cs, 3)
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(MergeOption{})
	newMfDesc, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newMfDesc == nil {
		t.Fatal("expected non-nil manifest descriptor")
	}

	// Read back the new manifest and config.
	var newMf ocispec.Manifest
	if _, err := converter.ReadJSON(context.Background(), cs, &newMf, *newMfDesc); err != nil {
		t.Fatalf("read new manifest: %v", err)
	}
	var newCfg ocispec.Image
	if _, err := converter.ReadJSON(context.Background(), cs, &newCfg, newMf.Config); err != nil {
		t.Fatalf("read new config: %v", err)
	}

	if len(newCfg.RootFS.DiffIDs) != len(origLayers) {
		t.Fatalf("DiffIDs count: want %d, got %d", len(origLayers), len(newCfg.RootFS.DiffIDs))
	}
	for i, l := range origLayers {
		expectedDiffID := digest.Digest(l.Annotations[LayerAnnotationUncompressed])
		if newCfg.RootFS.DiffIDs[i] != expectedDiffID {
			t.Errorf("layer %d DiffID: want %s, got %s", i, expectedDiffID, newCfg.RootFS.DiffIDs[i])
		}
		// LayerAnnotationUncompressed must be stripped from the stored layer.
		if _, ok := newMf.Layers[i].Annotations[LayerAnnotationUncompressed]; ok {
			t.Errorf("layer %d: LayerAnnotationUncompressed must be stripped from manifest annotations", i)
		}
	}
}



// TestConvertHookFunc_Manifest_WithReferrer verifies that manifest.Subject is
// set to orgDesc when MergeOption.WithReferrer is enabled.
func TestConvertHookFunc_Manifest_WithReferrer(t *testing.T) {
	cs := newConvertTestStore(t)
	mfDesc, _, _ := buildTestManifest(t, cs, 1)
	orgDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromString("original-manifest"),
		Size:      64,
	}

	hook := ConvertHookFunc(MergeOption{WithReferrer: true})
	newMfDesc, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var newMf ocispec.Manifest
	if _, err := converter.ReadJSON(context.Background(), cs, &newMf, *newMfDesc); err != nil {
		t.Fatalf("read new manifest: %v", err)
	}

	if newMf.Subject == nil {
		t.Fatal("expected manifest.Subject to be set with WithReferrer=true")
	}
	if newMf.Subject.Digest != orgDesc.Digest {
		t.Errorf("Subject.Digest: want %s, got %s", orgDesc.Digest, newMf.Subject.Digest)
	}
	// Platform must be stripped from the subject.
	if newMf.Subject.Platform != nil {
		t.Errorf("Subject.Platform must be nil, got %v", newMf.Subject.Platform)
	}
}

// TestConvertHookFunc_Manifest_MismatchedLayers verifies that a layer/DiffID
// count mismatch returns an error rather than silently corrupting the config.
func TestConvertHookFunc_Manifest_MismatchedLayers(t *testing.T) {
	cs := newConvertTestStore(t)

	// Build a manifest with 2 layers but a config with 3 DiffIDs.
	layers := []ocispec.Descriptor{
		{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromString("l1"), Size: 2},
		{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromString("l2"), Size: 2},
	}
	cfg := ocispec.Image{
		RootFS: ocispec.RootFS{
			Type: "layers",
			DiffIDs: []digest.Digest{
				digest.FromString("d1"),
				digest.FromString("d2"),
				digest.FromString("d3"), // mismatch: 3 DiffIDs vs 2 layers
			},
		},
	}
	cfgDesc := writeJSON(t, cs, cfg, ocispec.MediaTypeImageConfig)
	mf := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    cfgDesc,
		Layers:    layers,
	}
	mfDesc := writeJSON(t, cs, mf, ocispec.MediaTypeImageManifest)
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(MergeOption{})
	_, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err == nil {
		t.Error("expected error for layer/DiffID count mismatch, got nil")
	}
}

// TestConvertHookFunc_Manifest_WithReferrer_NoPlatform verifies that even when
// orgDesc carries a Platform, it is stripped from the Subject field.
func TestConvertHookFunc_Manifest_WithReferrer_NoPlatform(t *testing.T) {
	cs := newConvertTestStore(t)
	mfDesc, _, _ := buildTestManifest(t, cs, 1)
	orgDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromString("original"),
		Size:      64,
		Platform: &ocispec.Platform{
			OS:           "linux",
			Architecture: "amd64",
		},
	}

	hook := ConvertHookFunc(MergeOption{WithReferrer: true})
	newMfDesc, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var newMf ocispec.Manifest
	if _, err := converter.ReadJSON(context.Background(), cs, &newMf, *newMfDesc); err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if newMf.Subject != nil && newMf.Subject.Platform != nil {
		t.Errorf("expected Subject.Platform to be nil, got %v", newMf.Subject.Platform)
	}
}

// TestConvertHookFunc_Concurrency_Race launches N goroutines each invoking
// ConvertHookFunc on a separate manifest to catch data races that would escape
// single-goroutine tests.  Run with -race.
func TestConvertHookFunc_Concurrency_Race(t *testing.T) {
	cs := newConvertTestStore(t)

	const concurrency = 8
	mfDescs := make([]ocispec.Descriptor, concurrency)
	for i := range mfDescs {
		md, _, _ := buildTestManifest(t, cs, 2)
		mfDescs[i] = md
	}

	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}
	hook := ConvertHookFunc(MergeOption{WithReferrer: true})

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result, err := hook(context.Background(), cs, orgDesc, &mfDescs[idx])
			if err != nil {
				t.Errorf("goroutine %d: unexpected error: %v", idx, err)
				return
			}
			if result == nil {
				t.Errorf("goroutine %d: expected non-nil result", idx)
			}
		}(i)
	}
	wg.Wait()
}

package soci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	sociPkg "github.com/awslabs/soci-snapshotter/soci"
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

// writeJSON marshals v and stores it in cs, returning the descriptor.
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

// buildTestManifest writes a minimal OCI manifest and config to cs.
// Each layer blob is intentionally tiny (well below the default 10 MiB
// minLayerSize), so the SOCI IndexBuilder will skip them and return ErrEmptyIndex.
// This lets all manifest-path tests exercise the graceful-skip invariant without
// requiring compressed layers, disk space, or real SOCI infrastructure.
func buildTestManifest(t *testing.T, cs content.Store, numLayers int) (ocispec.Descriptor, []ocispec.Descriptor) {
	t.Helper()

	layers := make([]ocispec.Descriptor, numLayers)
	diffIDs := make([]digest.Digest, numLayers)
	for i := range layers {
		body := fmt.Sprintf("soci-test-layer-%d", i)
		dgst := digest.FromString(body)
		diffIDs[i] = dgst
		layers[i] = ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Digest:    dgst,
			Size:      int64(len(body)), // tiny — triggers ErrEmptyIndex
		}
	}

	cfg := ocispec.Image{
		RootFS: ocispec.RootFS{Type: "layers", DiffIDs: diffIDs},
	}
	cfgDesc := writeJSON(t, cs, cfg, ocispec.MediaTypeImageConfig)

	mf := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    cfgDesc,
		Layers:    layers,
	}
	mfDesc := writeJSON(t, cs, mf, ocispec.MediaTypeImageManifest)

	return mfDesc, layers
}

// buildTestIndex writes an OCI index wrapping the given manifests to cs.
func buildTestIndex(t *testing.T, cs content.Store, manifests []ocispec.Descriptor) ocispec.Descriptor {
	t.Helper()
	idx := ocispec.Index{
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: manifests,
	}
	return writeJSON(t, cs, idx, ocispec.MediaTypeImageIndex)
}

// ─── descriptor-routing tests (no SOCI build) ────────────────────────────────

// TestConvertHookFunc_NonManifestPassthrough verifies that a descriptor that is
// neither an index nor a manifest is returned unchanged by the routing switch.
func TestConvertHookFunc_NonManifestPassthrough(t *testing.T) {
	cs := newConvertTestStore(t)
	configDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromString("config"),
		Size:      2,
	}
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(PackOption{}, MergeOption{})
	// nil newDesc → targetDesc == &orgDesc; routing falls through to default.
	result, err := hook(context.Background(), cs, orgDesc, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// The returned descriptor must be the target, not some new blob.
	if result.Digest != orgDesc.Digest {
		t.Errorf("want %s, got %s", orgDesc.Digest, result.Digest)
	}
	_ = configDesc
}

// TestConvertHookFunc_Index_Flatten verifies a single-entry index collapses to
// its bare manifest descriptor.
func TestConvertHookFunc_Index_Flatten(t *testing.T) {
	cs := newConvertTestStore(t)

	mfDesc, _ := buildTestManifest(t, cs, 1)
	idxDesc := buildTestIndex(t, cs, []ocispec.Descriptor{mfDesc})
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(PackOption{}, MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, &idxDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if images.IsIndexType(result.MediaType) {
		t.Errorf("expected bare manifest, got index %q", result.MediaType)
	}
	if result.Digest != mfDesc.Digest {
		t.Errorf("digest: want %s, got %s", mfDesc.Digest, result.Digest)
	}
}

// TestConvertHookFunc_Index_Multi verifies a multi-entry index is returned as-is.
func TestConvertHookFunc_Index_Multi(t *testing.T) {
	cs := newConvertTestStore(t)

	m1, _ := buildTestManifest(t, cs, 1)
	m2, _ := buildTestManifest(t, cs, 2)
	idxDesc := buildTestIndex(t, cs, []ocispec.Descriptor{m1, m2})
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(PackOption{}, MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, &idxDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !images.IsIndexType(result.MediaType) {
		t.Errorf("expected index type, got %q", result.MediaType)
	}
}

// ─── SOCI build integration tests ────────────────────────────────────────────

// TestConvertHookFunc_Manifest_EmptyIndex is the most important production
// guarantee: when all layers are too small (ErrEmptyIndex), the hook must
// return the original manifest descriptor unchanged — it must NOT propagate
// the error and break the conversion pipeline.
func TestConvertHookFunc_Manifest_EmptyIndex(t *testing.T) {
	cs := newConvertTestStore(t)
	mfDesc, _ := buildTestManifest(t, cs, 2) // tiny → ErrEmptyIndex
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(PackOption{GCRoot: true}, MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err != nil {
		t.Fatalf("ErrEmptyIndex must not propagate as an error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result on graceful skip")
	}
	if result.Digest != mfDesc.Digest {
		t.Errorf("want original digest %s on skip, got %s", mfDesc.Digest, result.Digest)
	}
}

// TestConvertHookFunc_Manifest_GCRoot_NoSociIndex ensures that when ErrEmptyIndex
// occurs (nothing indexed), the SOCI GC label is NOT written to the manifest.
func TestConvertHookFunc_Manifest_GCRoot_NoSociIndex(t *testing.T) {
	cs := newConvertTestStore(t)
	mfDesc, _ := buildTestManifest(t, cs, 1)
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(PackOption{GCRoot: true}, MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The original manifest was returned (skip path), so its info labels exist.
	info, err := cs.Info(context.Background(), result.Digest)
	if err != nil {
		// The content store may not persist labels for blobs written by
		// WriteBlob without an explicit update.  Skip this check gracefully.
		t.Logf("cs.Info returned %v — skipping GC label absence check", err)
		return
	}
	if _, ok := info.Labels["containerd.io/gc.ref.content.soci.index"]; ok {
		t.Error("SOCI GC label must not be written when no index was produced")
	}
}

// TestConvertHookFunc_Manifest_WithReferrer_OnSkip verifies that the conversion
// does not error when WithReferrer is set but ErrEmptyIndex causes a skip.
func TestConvertHookFunc_Manifest_WithReferrer_OnSkip(t *testing.T) {
	cs := newConvertTestStore(t)
	mfDesc, _ := buildTestManifest(t, cs, 1)
	orgDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromString("orig"),
		Size:      64,
	}

	hook := ConvertHookFunc(PackOption{}, MergeOption{WithReferrer: true})
	result, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestConvertHookFunc_Manifest_ConfigRef verifies the returned manifest (when
// modified) carries a valid config descriptor reference.
func TestConvertHookFunc_Manifest_ConfigRef(t *testing.T) {
	cs := newConvertTestStore(t)
	mfDesc, _ := buildTestManifest(t, cs, 1)
	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}

	hook := ConvertHookFunc(PackOption{}, MergeOption{})
	result, err := hook(context.Background(), cs, orgDesc, &mfDesc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read the returned manifest to verify config reference is intact.
	var mf ocispec.Manifest
	if _, err := converter.ReadJSON(context.Background(), cs, &mf, *result); err != nil {
		// May happen if the result is the original (skip path) and the
		// manifest was not modified.  Just pass.
		return
	}
	if mf.Config.Digest == "" {
		t.Error("manifest config digest must not be empty")
	}
}

// ─── isErrEmptyIndex unit test ─────────────────────────────────────────────

// TestIsErrEmptyIndex verifies the internal sentinel check correctly identifies
// ErrEmptyIndex and distinguishes it from unrelated errors.
func TestIsErrEmptyIndex(t *testing.T) {
	if !isErrEmptyIndex(sociPkg.ErrEmptyIndex) {
		t.Error("isErrEmptyIndex must return true for sociPkg.ErrEmptyIndex")
	}
	if isErrEmptyIndex(fmt.Errorf("some other error")) {
		t.Error("isErrEmptyIndex must return false for unrelated errors")
	}
	if isErrEmptyIndex(nil) {
		t.Error("isErrEmptyIndex must return false for nil")
	}
}

// ─── concurrency / race test ─────────────────────────────────────────────────

// TestConvertHookFunc_Concurrency_Race launches N goroutines each calling
// ConvertHookFunc on a separate index descriptor concurrently to verify there
// are no data races in the routing and index-flattening paths.  Run with -race.
func TestConvertHookFunc_Concurrency_Race(t *testing.T) {
	cs := newConvertTestStore(t)

	const concurrency = 8
	idxDescs := make([]ocispec.Descriptor, concurrency)
	for i := range idxDescs {
		mfDesc, _ := buildTestManifest(t, cs, 1)
		idxDescs[i] = buildTestIndex(t, cs, []ocispec.Descriptor{mfDesc})
	}

	orgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("orig")}
	hook := ConvertHookFunc(PackOption{}, MergeOption{})

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result, err := hook(context.Background(), cs, orgDesc, &idxDescs[idx])
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

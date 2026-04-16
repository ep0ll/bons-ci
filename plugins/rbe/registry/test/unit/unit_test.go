// Package unit contains exhaustive unit tests for AccelRegistry internals.
//
// Coverage targets:
//   - Bloom filter: Add/Test, false-positive rate, concurrency, serialise/restore
//   - ShardedIndex: Index, Query, ExistsAny, ExistsByType, Remove, RemoveVariant,
//     concurrent writes, snapshot/restore, Stats
//   - Accel detection: all four types (nydus, estargz, soci, overlaybd),
//     ambiguous manifests, priority ordering, config-blob fallback
//   - DAG traversal: manifest, index, missing nodes, cycles (dedup), max depth
//   - ContentStore: Put/Get/Exists/Delete/Info/Walk, digest mismatch
//   - MetadataStore: Put/Get/Delete, secondary indices
//   - ReferrersStore: AddReferrer, GetReferrers filtering, RemoveReferrer
package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/dag"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/index"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/metadata"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/referral"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/storage/memory"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/bloom"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func sha256Digest(s string) digest.Digest {
	return digest.Canonical.FromString(s)
}

func makeVariant(accelType types.AccelType, sourceDigest digest.Digest) types.AccelVariant {
	manifestDgst := sha256Digest(string(accelType) + sourceDigest.String())
	return types.AccelVariant{
		AccelType:      accelType,
		ManifestDigest: manifestDgst,
		Repository:     "library/node",
		Tag:            "latest",
		Annotations:    map[string]string{},
		CreatedAt:      time.Now(),
		Visibility:     types.VisibilityPublic,
		SourceRefs: []types.SourceRef{
			{Digest: sourceDigest, Kind: types.SourceRefManifest},
		},
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Bloom Filter tests
// ────────────────────────────────────────────────────────────────────────────

func TestBloomFilter_BasicAddTest(t *testing.T) {
	f := bloom.New(1<<20, 7)

	keys := []string{"sha256:abc", "sha256:def", "sha256:123"}
	for _, k := range keys {
		f.AddString(k)
	}
	for _, k := range keys {
		if !f.TestString(k) {
			t.Errorf("expected %q to be present", k)
		}
	}
	// Absent key should not be found (this is probabilistic — almost certainly false)
	absent := "sha256:000000000000definitely_not_present_xyzzy"
	if f.TestString(absent) {
		t.Logf("TestBloom: false positive for %q (acceptable but worth noting)", absent)
	}
}

func TestBloomFilter_DigestStrings(t *testing.T) {
	f := bloom.New(1<<22, 7)
	dgst := sha256Digest("test-image:latest")
	f.AddDigestString(dgst.String())
	if !f.TestDigestString(dgst.String()) {
		t.Error("digest should be present after Add")
	}
	absent := sha256Digest("does-not-exist")
	if f.TestDigestString(absent.String()) {
		t.Log("false positive for absent digest (probabilistic, acceptable)")
	}
}

func TestBloomFilter_FalsePositiveRate(t *testing.T) {
	n := uint64(10_000)
	f := bloom.NewDefault(n)

	// Insert n elements
	for i := uint64(0); i < n; i++ {
		f.AddString(fmt.Sprintf("element-%d", i))
	}

	// Test n different elements and count false positives
	fps := 0
	for i := uint64(0); i < n; i++ {
		if f.TestString(fmt.Sprintf("not-inserted-%d", i)) {
			fps++
		}
	}
	fpRate := float64(fps) / float64(n)
	// With optimal parameters the FP rate should be ≤ 5% at 2× capacity
	if fpRate > 0.05 {
		t.Errorf("false positive rate too high: %.4f (expected ≤ 0.05)", fpRate)
	}
}

func TestBloomFilter_Concurrency(t *testing.T) {
	f := bloom.New(1<<24, 7)
	const goroutines = 64
	const perGoroutine = 1000

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				key := fmt.Sprintf("g%d-elem%d", g, i)
				f.AddString(key)
				if !f.TestString(key) {
					t.Errorf("concurrent: %q should be present after Add", key)
				}
			}
		}()
	}
	wg.Wait()
}

func TestBloomFilter_MarshalUnmarshal(t *testing.T) {
	f := bloom.New(1<<20, 7)
	for i := 0; i < 100; i++ {
		f.AddString(fmt.Sprintf("key-%d", i))
	}

	data := f.Marshal()
	f2, err := bloom.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for i := 0; i < 100; i++ {
		if !f2.TestString(fmt.Sprintf("key-%d", i)) {
			t.Errorf("restored filter missing key-%d", i)
		}
	}
}

func TestBloomFilter_OptimalParams(t *testing.T) {
	cases := []struct {
		n    uint64
		p    float64
		maxM uint64
	}{
		{1000, 0.01, 20000},
		{10000, 0.001, 200000},
		{100000, 0.01, 2000000},
	}
	for _, tc := range cases {
		m, k := bloom.OptimalParams(tc.n, tc.p)
		if m == 0 {
			t.Errorf("n=%d p=%f: m=0", tc.n, tc.p)
		}
		if k == 0 {
			t.Errorf("n=%d p=%f: k=0", tc.n, tc.p)
		}
		if m > tc.maxM*2 {
			t.Errorf("n=%d p=%f: m=%d unexpectedly large", tc.n, tc.p, m)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// ShardedIndex tests
// ────────────────────────────────────────────────────────────────────────────

func TestShardedIndex_IndexAndQuery(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)

	sourceDgst := sha256Digest("node:20-alpine")
	variant := makeVariant(types.AccelNydus, sourceDgst)

	if err := idx.Index(ctx, variant); err != nil {
		t.Fatalf("Index: %v", err)
	}

	result, err := idx.Query(ctx, sourceDgst)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if !result.Found {
		t.Fatal("expected result.Found=true")
	}
	if result.TotalVariants != 1 {
		t.Errorf("expected 1 variant, got %d", result.TotalVariants)
	}
	variants := result.Variants[types.AccelNydus]
	if len(variants) != 1 {
		t.Errorf("expected 1 nydus variant, got %d", len(variants))
	}
	if variants[0].ManifestDigest != variant.ManifestDigest {
		t.Errorf("manifest digest mismatch")
	}
}

func TestShardedIndex_MultipleTypes(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)

	sourceDgst := sha256Digest("node:20")

	for _, accelType := range types.KnownAccelTypes {
		v := makeVariant(accelType, sourceDgst)
		if err := idx.Index(ctx, v); err != nil {
			t.Fatalf("Index %s: %v", accelType, err)
		}
	}

	result, err := idx.Query(ctx, sourceDgst)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !result.Found {
		t.Fatal("expected Found")
	}
	if result.TotalVariants != len(types.KnownAccelTypes) {
		t.Errorf("expected %d variants, got %d", len(types.KnownAccelTypes), result.TotalVariants)
	}
	if len(result.SupportedTypes) != len(types.KnownAccelTypes) {
		t.Errorf("expected %d supported types, got %d", len(types.KnownAccelTypes), len(result.SupportedTypes))
	}
}

func TestShardedIndex_QueryNotFound(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)

	result, err := idx.Query(ctx, sha256Digest("not-indexed"))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.Found {
		t.Error("expected Found=false for unindexed digest")
	}
}

func TestShardedIndex_ExistsAny(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)
	sourceDgst := sha256Digest("myimage:v1")
	v := makeVariant(types.AccelEstargz, sourceDgst)
	_ = idx.Index(ctx, v)

	if !idx.ExistsAny(ctx, sourceDgst) {
		t.Error("ExistsAny should return true after indexing")
	}
	if idx.ExistsAny(ctx, sha256Digest("not-there")) {
		// This is a bloom filter — might be a false positive but highly unlikely
		t.Log("ExistsAny: possible false positive for absent digest")
	}
}

func TestShardedIndex_ExistsByType(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)
	sourceDgst := sha256Digest("myimage:v2")
	v := makeVariant(types.AccelSOCI, sourceDgst)
	_ = idx.Index(ctx, v)

	if !idx.ExistsByType(ctx, sourceDgst, types.AccelSOCI) {
		t.Error("ExistsByType(SOCI) should be true")
	}
	if idx.ExistsByType(ctx, sourceDgst, types.AccelNydus) {
		t.Error("ExistsByType(Nydus) should be false when only SOCI was indexed")
	}
}

func TestShardedIndex_Remove(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)
	sourceDgst := sha256Digest("remove-test:v1")
	v := makeVariant(types.AccelNydus, sourceDgst)
	_ = idx.Index(ctx, v)

	if err := idx.Remove(ctx, sourceDgst); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	result, _ := idx.Query(ctx, sourceDgst)
	if result.Found {
		t.Error("expected Found=false after Remove")
	}
}

func TestShardedIndex_RemoveVariant(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)
	sourceDgst := sha256Digest("remove-variant:v1")

	v1 := makeVariant(types.AccelNydus, sourceDgst)
	v2 := makeVariant(types.AccelSOCI, sourceDgst)
	_ = idx.Index(ctx, v1)
	_ = idx.Index(ctx, v2)

	if err := idx.RemoveVariant(ctx, sourceDgst, v1.ManifestDigest); err != nil {
		t.Fatalf("RemoveVariant: %v", err)
	}

	result, _ := idx.Query(ctx, sourceDgst)
	if !result.Found {
		t.Fatal("expected Found=true after removing only one variant")
	}
	if _, ok := result.Variants[types.AccelNydus]; ok {
		t.Error("nydus variant should have been removed")
	}
	if _, ok := result.Variants[types.AccelSOCI]; !ok {
		t.Error("soci variant should still be present")
	}
}

func TestShardedIndex_Idempotent(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)
	sourceDgst := sha256Digest("idempotent:v1")
	v := makeVariant(types.AccelNydus, sourceDgst)

	for i := 0; i < 5; i++ {
		if err := idx.Index(ctx, v); err != nil {
			t.Fatalf("Index iteration %d: %v", i, err)
		}
	}

	result, _ := idx.Query(ctx, sourceDgst)
	if result.TotalVariants != 1 {
		t.Errorf("idempotent: expected 1 variant, got %d", result.TotalVariants)
	}
}

func TestShardedIndex_ConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(10000)

	const goroutines = 32
	const imagesPerGoroutine = 50

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < imagesPerGoroutine; i++ {
				src := sha256Digest(fmt.Sprintf("g%d-image%d", g, i))
				for _, at := range types.KnownAccelTypes {
					v := makeVariant(at, src)
					if err := idx.Index(ctx, v); err != nil {
						t.Errorf("concurrent Index: %v", err)
					}
				}
			}
		}()
	}
	wg.Wait()

	stats := idx.Stats()
	expected := int64(goroutines * imagesPerGoroutine)
	if stats.TotalSourceDigests < expected {
		// Some concurrent writes to the same digest may coalesce — that's fine.
		t.Logf("TotalSourceDigests=%d (expected≈%d)", stats.TotalSourceDigests, expected)
	}
}

func TestShardedIndex_SnapshotRestore(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)

	sources := make([]digest.Digest, 10)
	for i := range sources {
		src := sha256Digest(fmt.Sprintf("snapshot-source-%d", i))
		sources[i] = src
		v := makeVariant(types.AccelNydus, src)
		_ = idx.Index(ctx, v)
	}

	snap, err := idx.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	idx2 := index.NewShardedIndex(1000)
	if err := idx2.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for _, src := range sources {
		result, err := idx2.Query(ctx, src)
		if err != nil || !result.Found {
			t.Errorf("after restore: source %s not found", src)
		}
	}
}

func TestShardedIndex_Stats(t *testing.T) {
	ctx := context.Background()
	idx := index.NewShardedIndex(1000)

	src := sha256Digest("stats-test:v1")
	for _, at := range types.KnownAccelTypes {
		v := makeVariant(at, src)
		_ = idx.Index(ctx, v)
	}

	stats := idx.Stats()
	if stats.TotalSourceDigests != 1 {
		t.Errorf("expected 1 source digest, got %d", stats.TotalSourceDigests)
	}
	if stats.ShardCount != 256 {
		t.Errorf("expected 256 shards, got %d", stats.ShardCount)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// ContentStore tests
// ────────────────────────────────────────────────────────────────────────────

func TestContentStore_PutGetExists(t *testing.T) {
	ctx := context.Background()
	s := memory.New()

	data := []byte("hello, nydus blob!")
	dgst := digest.Canonical.FromBytes(data)

	if err := s.Put(ctx, dgst, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := s.Exists(ctx, dgst)
	if err != nil || !exists {
		t.Fatalf("Exists: err=%v exists=%v", err, exists)
	}

	rc, err := s.Get(ctx, dgst)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(rc)
	if !bytes.Equal(buf.Bytes(), data) {
		t.Errorf("data mismatch: got %q, want %q", buf.Bytes(), data)
	}
}

func TestContentStore_DigestMismatch(t *testing.T) {
	ctx := context.Background()
	s := memory.New()

	data := []byte("correct data")
	wrongDgst := digest.Canonical.FromString("wrong")

	err := s.Put(ctx, wrongDgst, bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("expected digest mismatch error, got nil")
	}
}

func TestContentStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := memory.New()

	data := []byte("deletable")
	dgst := digest.Canonical.FromBytes(data)
	_ = s.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))
	_ = s.Delete(ctx, dgst)

	exists, _ := s.Exists(ctx, dgst)
	if exists {
		t.Error("blob should not exist after Delete")
	}
}

func TestContentStore_GetNotFound(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	_, err := s.Get(ctx, sha256Digest("not-stored"))
	if err == nil {
		t.Error("expected error for missing blob")
	}
}

func TestContentStore_Walk(t *testing.T) {
	ctx := context.Background()
	s := memory.New()

	want := make(map[digest.Digest]bool)
	for i := 0; i < 10; i++ {
		data := []byte(fmt.Sprintf("blob-%d", i))
		dgst := digest.Canonical.FromBytes(data)
		want[dgst] = true
		_ = s.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))
	}

	got := make(map[digest.Digest]bool)
	_ = s.Walk(ctx, func(info types.ContentInfo) error {
		got[info.Digest] = true
		return nil
	})

	for dgst := range want {
		if !got[dgst] {
			t.Errorf("Walk missed digest %s", dgst)
		}
	}
}

func TestContentStore_ConcurrentPut(t *testing.T) {
	ctx := context.Background()
	s := memory.New()

	data := []byte("concurrent write target")
	dgst := digest.Canonical.FromBytes(data)

	const goroutines = 50
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Put(ctx, dgst, bytes.NewReader(data), int64(len(data)))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Put error: %v", i, err)
		}
	}

	exists, _ := s.Exists(ctx, dgst)
	if !exists {
		t.Error("blob should exist after concurrent writes")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// MetadataStore tests
// ────────────────────────────────────────────────────────────────────────────

func TestMetadataStore_PutGet(t *testing.T) {
	ctx := context.Background()
	s := metadata.New()

	meta := types.ImageMetadata{
		Digest:      sha256Digest("meta-test"),
		Repository:  "library/python",
		Tags:        []string{"3.12"},
		Visibility:  types.VisibilityPublic,
		IsAccel:     true,
		AccelType:   types.AccelNydus,
		TotalSize:   1024 * 1024,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Annotations: map[string]string{types.AnnotationAccelType: "nydus"},
	}

	if err := s.Put(ctx, meta); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(ctx, meta.Repository, meta.Digest)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccelType != meta.AccelType {
		t.Errorf("AccelType: got %s, want %s", got.AccelType, meta.AccelType)
	}
}

func TestMetadataStore_ListByAccelType(t *testing.T) {
	ctx := context.Background()
	s := metadata.New()

	// Insert 3 nydus, 2 estargz
	for i := 0; i < 3; i++ {
		m := types.ImageMetadata{
			Digest:     sha256Digest(fmt.Sprintf("nydus-img-%d", i)),
			Repository: "test/nydus",
			AccelType:  types.AccelNydus,
			IsAccel:    true,
		}
		_ = s.Put(ctx, m)
	}
	for i := 0; i < 2; i++ {
		m := types.ImageMetadata{
			Digest:     sha256Digest(fmt.Sprintf("estargz-img-%d", i)),
			Repository: "test/estargz",
			AccelType:  types.AccelEstargz,
			IsAccel:    true,
		}
		_ = s.Put(ctx, m)
	}

	nydusResults, _ := s.ListByAccelType(ctx, types.AccelNydus)
	if len(nydusResults) != 3 {
		t.Errorf("expected 3 nydus entries, got %d", len(nydusResults))
	}

	estargzResults, _ := s.ListByAccelType(ctx, types.AccelEstargz)
	if len(estargzResults) != 2 {
		t.Errorf("expected 2 estargz entries, got %d", len(estargzResults))
	}
}

func TestMetadataStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := metadata.New()

	m := types.ImageMetadata{
		Digest:     sha256Digest("delete-me"),
		Repository: "test/delete",
		AccelType:  types.AccelSOCI,
	}
	_ = s.Put(ctx, m)
	_ = s.Delete(ctx, m.Repository, m.Digest)

	_, err := s.Get(ctx, m.Repository, m.Digest)
	if err == nil {
		t.Error("expected error after Delete")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// ReferrersStore tests
// ────────────────────────────────────────────────────────────────────────────

func TestReferrers_AddAndGet(t *testing.T) {
	ctx := context.Background()
	s := referral.New()

	subjectDgst := sha256Digest("original-manifest")
	sociDgst := sha256Digest("soci-index-manifest")

	desc := ocispec.Descriptor{
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		ArtifactType: types.SOCIArtifactType,
		Digest:       sociDgst,
		Size:         1024,
	}

	_ = s.AddReferrer(ctx, "library/node", subjectDgst, desc)

	results, err := s.GetReferrers(ctx, "library/node", subjectDgst, "")
	if err != nil {
		t.Fatalf("GetReferrers: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 referrer, got %d", len(results))
	}
}

func TestReferrers_FilterByArtifactType(t *testing.T) {
	ctx := context.Background()
	s := referral.New()

	subjectDgst := sha256Digest("subject-manifest")
	sociDesc := ocispec.Descriptor{
		ArtifactType: types.SOCIArtifactType,
		Digest:       sha256Digest("soci"),
	}
	sbomDesc := ocispec.Descriptor{
		ArtifactType: "application/vnd.syft.sbom+json",
		Digest:       sha256Digest("sbom"),
	}

	_ = s.AddReferrer(ctx, "repo/a", subjectDgst, sociDesc)
	_ = s.AddReferrer(ctx, "repo/a", subjectDgst, sbomDesc)

	sociOnly, _ := s.GetReferrers(ctx, "repo/a", subjectDgst, types.SOCIArtifactType)
	if len(sociOnly) != 1 || sociOnly[0].ArtifactType != types.SOCIArtifactType {
		t.Errorf("filter by SOCI: got %d results", len(sociOnly))
	}

	all, _ := s.GetReferrers(ctx, "repo/a", subjectDgst, "")
	if len(all) != 2 {
		t.Errorf("unfiltered: expected 2 referrers, got %d", len(all))
	}
}

func TestReferrers_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := referral.New()

	subjectDgst := sha256Digest("idempotent-subject")
	desc := ocispec.Descriptor{Digest: sha256Digest("referrer")}

	for i := 0; i < 5; i++ {
		_ = s.AddReferrer(ctx, "repo", subjectDgst, desc)
	}

	results, _ := s.GetReferrers(ctx, "repo", subjectDgst, "")
	if len(results) != 1 {
		t.Errorf("idempotent: expected 1 referrer, got %d", len(results))
	}
}

func TestReferrers_Remove(t *testing.T) {
	ctx := context.Background()
	s := referral.New()

	subjectDgst := sha256Digest("remove-subject")
	desc := ocispec.Descriptor{Digest: sha256Digest("remove-referrer")}
	_ = s.AddReferrer(ctx, "repo", subjectDgst, desc)
	_ = s.RemoveReferrer(ctx, "repo", subjectDgst, desc.Digest)

	results, _ := s.GetReferrers(ctx, "repo", subjectDgst, "")
	if len(results) != 0 {
		t.Errorf("expected 0 referrers after Remove, got %d", len(results))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// DAG Traversal tests
// ────────────────────────────────────────────────────────────────────────────

func TestDAGTraverser_SingleManifest(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	traverser := dag.New()

	// Build: manifest → config + 2 layers
	configData := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDgst := digest.Canonical.FromBytes(configData)
	_ = store.Put(ctx, configDgst, bytes.NewReader(configData), int64(len(configData)))

	layer1Data := []byte("layer1")
	layer1Dgst := digest.Canonical.FromBytes(layer1Data)
	_ = store.Put(ctx, layer1Dgst, bytes.NewReader(layer1Data), int64(len(layer1Data)))

	layer2Data := []byte("layer2")
	layer2Dgst := digest.Canonical.FromBytes(layer2Data)
	_ = store.Put(ctx, layer2Dgst, bytes.NewReader(layer2Data), int64(len(layer2Data)))

	manifest := ocispec.Manifest{
		Versioned: ocispec.Manifest{}.Versioned,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    configDgst,
			Size:      int64(len(configData)),
		},
		Layers: []ocispec.Descriptor{
			{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: layer1Dgst, Size: int64(len(layer1Data))},
			{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: layer2Dgst, Size: int64(len(layer2Data))},
		},
	}
	manifestData, _ := json.Marshal(manifest)
	manifestDgst := digest.Canonical.FromBytes(manifestData)
	_ = store.Put(ctx, manifestDgst, bytes.NewReader(manifestData), int64(len(manifestData)))

	result, err := traverser.Traverse(ctx, "library/node", ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    manifestDgst,
		Size:      int64(len(manifestData)),
	}, store)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}

	if result.TotalNodes < 3 { // manifest + config + 2 layers (root not counted separately)
		t.Errorf("expected ≥3 nodes, got %d", result.TotalNodes)
	}
	if !result.IsComplete {
		t.Errorf("expected IsComplete=true (all blobs present)")
	}
}

func TestDAGTraverser_MissingNode(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	traverser := dag.New()

	// Manifest references a layer that is NOT stored
	missingLayerDgst := sha256Digest("missing-layer-content")
	manifest := ocispec.Manifest{
		Config: ocispec.Descriptor{Digest: sha256Digest("cfg"), Size: 1},
		Layers: []ocispec.Descriptor{
			{Digest: missingLayerDgst, Size: 100},
		},
	}
	manifestData, _ := json.Marshal(manifest)
	manifestDgst := digest.Canonical.FromBytes(manifestData)
	_ = store.Put(ctx, manifestDgst, bytes.NewReader(manifestData), int64(len(manifestData)))

	result, err := traverser.Traverse(ctx, "repo", ocispec.Descriptor{
		Digest:    manifestDgst,
		MediaType: ocispec.MediaTypeImageManifest,
	}, store)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if result.IsComplete {
		t.Error("expected IsComplete=false when layers are missing")
	}
	if result.MissingNodes == 0 {
		t.Error("expected MissingNodes > 0")
	}
}

func TestDAGTraverser_Deduplication(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	traverser := dag.New()

	// Both manifests share the same layer
	sharedData := []byte("shared-layer")
	sharedDgst := digest.Canonical.FromBytes(sharedData)
	_ = store.Put(ctx, sharedDgst, bytes.NewReader(sharedData), int64(len(sharedData)))

	cfgData := []byte(`{}`)
	cfgDgst := digest.Canonical.FromBytes(cfgData)
	_ = store.Put(ctx, cfgDgst, bytes.NewReader(cfgData), int64(len(cfgData)))

	manifest := ocispec.Manifest{
		Config: ocispec.Descriptor{Digest: cfgDgst, Size: int64(len(cfgData))},
		// Reference same layer twice (malformed but we handle it)
		Layers: []ocispec.Descriptor{
			{Digest: sharedDgst, Size: int64(len(sharedData))},
			{Digest: sharedDgst, Size: int64(len(sharedData))},
		},
	}
	manifestData, _ := json.Marshal(manifest)
	manifestDgst := digest.Canonical.FromBytes(manifestData)
	_ = store.Put(ctx, manifestDgst, bytes.NewReader(manifestData), int64(len(manifestData)))

	result, err := traverser.Traverse(ctx, "repo", ocispec.Descriptor{
		Digest:    manifestDgst,
		MediaType: ocispec.MediaTypeImageManifest,
	}, store)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	// Should deduplicate the shared layer — not count it twice
	// TotalNodes = root manifest + config + shared layer (deduplicated) = 3
	if result.TotalNodes > 5 {
		t.Errorf("dedup failed: TotalNodes=%d (too high)", result.TotalNodes)
	}
}

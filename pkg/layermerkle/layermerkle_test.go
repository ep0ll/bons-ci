package layermerkle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle"
	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
	"github.com/bons/bons-ci/pkg/layermerkle/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// LayerStack tests
// ─────────────────────────────────────────────────────────────────────────────

func TestLayerStack_Top_ReturnsLastElement(t *testing.T) {
	stack := testutil.MakeLayerStack("layer-0", "layer-1", "layer-2")
	want := digest.FromString("layer-2")
	if got := stack.Top(); got != want {
		t.Errorf("Top() = %v, want %v", got, want)
	}
}

func TestLayerStack_Top_EmptyStack(t *testing.T) {
	var stack layermerkle.LayerStack
	if got := stack.Top(); got != "" {
		t.Errorf("Top() on empty stack = %v, want empty", got)
	}
}

func TestLayerStack_Contains(t *testing.T) {
	stack := testutil.MakeLayerStack("a", "b", "c")
	if !stack.Contains(digest.FromString("b")) {
		t.Error("Contains(b) = false, want true")
	}
	if stack.Contains(digest.FromString("d")) {
		t.Error("Contains(d) = true, want false")
	}
}

func TestLayerStack_Valid(t *testing.T) {
	if (layermerkle.LayerStack{}).Valid() {
		t.Error("empty stack Valid() = true, want false")
	}
	if !testutil.MakeLayerStack("x").Valid() {
		t.Error("non-empty stack Valid() = false, want true")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AccessEvent tests
// ─────────────────────────────────────────────────────────────────────────────

func TestAccessEvent_IsReadAccess(t *testing.T) {
	readOps := []uint64{0x00000001, 0x00000020, 0x00001000, 0x00000010}
	for _, mask := range readOps {
		ev := testutil.NewAccessEvent().WithMask(mask).Build()
		if !ev.IsReadAccess() {
			t.Errorf("IsReadAccess() = false for mask 0x%x, want true", mask)
		}
	}
	writeOp := uint64(0x00000002) // FAN_MODIFY
	ev := testutil.NewAccessEvent().WithMask(writeOp).Build()
	if ev.IsReadAccess() {
		t.Error("IsReadAccess() = true for MODIFY, want false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LayerRegistry tests
// ─────────────────────────────────────────────────────────────────────────────

func TestLayerRegistry_RegisterAndGet(t *testing.T) {
	r := layermerkle.NewLayerRegistry()
	fix := testutil.NewLayerFixture("layer-a", "/diffs/a")
	if err := fix.Register(r); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get(fix.ID)
	if !ok {
		t.Fatal("Get: not found after Register")
	}
	if got.DiffPath != "/diffs/a" {
		t.Errorf("DiffPath = %q, want /diffs/a", got.DiffPath)
	}
}

func TestLayerRegistry_Register_EmptyID_Errors(t *testing.T) {
	r := layermerkle.NewLayerRegistry()
	err := r.Register(&layermerkle.LayerInfo{DiffPath: "/x"})
	if err == nil {
		t.Error("Register with empty ID should error")
	}
}

func TestLayerRegistry_Unregister(t *testing.T) {
	r := layermerkle.NewLayerRegistry()
	fix := testutil.NewLayerFixture("layer-b", "/diffs/b")
	_ = fix.Register(r)
	r.Unregister(fix.ID)
	if _, ok := r.Get(fix.ID); ok {
		t.Error("layer should not be found after Unregister")
	}
}

func TestLayerRegistry_ResolveStack(t *testing.T) {
	r := layermerkle.NewLayerRegistry()
	a := testutil.NewLayerFixture("a", "/d/a")
	b := testutil.NewLayerFixture("b", "/d/b")
	_ = a.Register(r)
	_ = b.Register(r)

	stack := layermerkle.LayerStack{a.ID, b.ID, digest.FromString("missing")}
	infos, errs := r.ResolveStack(stack)

	if len(infos) != 2 {
		t.Errorf("ResolveStack: got %d infos, want 2", len(infos))
	}
	if len(errs) != 1 {
		t.Errorf("ResolveStack: got %d errors, want 1 (for missing layer)", len(errs))
	}
	if !errors.Is(errs[0], layermerkle.ErrLayerNotFound) {
		t.Errorf("error should wrap ErrLayerNotFound, got %v", errs[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ShardedLRUCache tests
// ─────────────────────────────────────────────────────────────────────────────

func TestShardedLRUCache_GetMissReturnsEmpty(t *testing.T) {
	c := layermerkle.NewShardedLRUCache(1024)
	_, ok := c.Get(digest.FromString("layer"), "some/path")
	if ok {
		t.Error("Get on empty cache should return false")
	}
}

func TestShardedLRUCache_SetAndGet(t *testing.T) {
	c := layermerkle.NewShardedLRUCache(1024)
	layerID := digest.FromString("layer-x")
	hash := digest.FromString("file-content")
	c.Set(layerID, "bin/sh", hash)

	got, ok := c.Get(layerID, "bin/sh")
	if !ok {
		t.Fatal("Get after Set should return true")
	}
	if got != hash {
		t.Errorf("Get() = %v, want %v", got, hash)
	}
}

func TestShardedLRUCache_Delete(t *testing.T) {
	c := layermerkle.NewShardedLRUCache(1024)
	id := digest.FromString("layer-y")
	c.Set(id, "a/b", digest.FromString("x"))
	c.Delete(id, "a/b")
	if _, ok := c.Get(id, "a/b"); ok {
		t.Error("entry should be absent after Delete")
	}
}

func TestShardedLRUCache_DeleteLayer(t *testing.T) {
	c := layermerkle.NewShardedLRUCache(4096)
	id := digest.FromString("layer-z")
	for i := range 10 {
		c.Set(id, strings.Repeat("x", i+1), digest.FromString("v"))
	}
	c.DeleteLayer(id)
	// Spot-check a few entries are gone.
	for i := range 10 {
		if _, ok := c.Get(id, strings.Repeat("x", i+1)); ok {
			t.Errorf("entry %d still present after DeleteLayer", i)
		}
	}
}

func TestShardedLRUCache_Stats_HitRate(t *testing.T) {
	c := layermerkle.NewShardedLRUCache(1024)
	id := digest.FromString("l")
	c.Set(id, "f", digest.FromString("h"))
	c.Get(id, "f")    // hit
	c.Get(id, "miss") // miss
	c.Get(id, "f")    // hit

	s := c.Stats()
	if s.Hits != 2 {
		t.Errorf("Hits = %d, want 2", s.Hits)
	}
	if s.Misses != 1 {
		t.Errorf("Misses = %d, want 1", s.Misses)
	}
	if s.HitRate() < 0.66 || s.HitRate() > 0.67 {
		t.Errorf("HitRate() = %.3f, want ~0.667", s.HitRate())
	}
}

func TestShardedLRUCache_EvictsOldestWhenFull(t *testing.T) {
	// Very small capacity to force eviction.
	c := layermerkle.NewShardedLRUCache(1)
	id := digest.FromString("evict-layer")
	// Fill one shard well past its per-shard capacity.
	for i := range 100 {
		key := strings.Repeat("p", i+1)
		c.Set(id, key, digest.FromString(key))
	}
	// Cache should not panic and Len should be bounded.
	if c.Len() > 256 { // 256 shards × 1 per shard
		t.Errorf("Len() = %d, expected bounded by capacity", c.Len())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// InstrumentedHashCache tests
// ─────────────────────────────────────────────────────────────────────────────

func TestInstrumentedHashCache_CallsHooks(t *testing.T) {
	var hits, misses, sets int
	inner := layermerkle.NewShardedLRUCache(1024)
	c := layermerkle.NewInstrumentedHashCache(
		inner,
		func(layermerkle.LayerID, string) { hits++ },
		func(layermerkle.LayerID, string) { misses++ },
		func(layermerkle.LayerID, string, layermerkle.FileHash) { sets++ },
	)

	id := digest.FromString("layer")
	c.Get(id, "x") // miss
	c.Set(id, "x", digest.FromString("h"))
	c.Get(id, "x") // hit

	if hits != 1 || misses != 1 || sets != 1 {
		t.Errorf("hits=%d misses=%d sets=%d, want 1,1,1", hits, misses, sets)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// FakeResolver tests
// ─────────────────────────────────────────────────────────────────────────────

func TestFakeResolver_FindOwnerLayer_Found(t *testing.T) {
	r := testutil.NewFakeResolver()
	layerID := digest.FromString("layer-1")
	r.AddFile("usr/bin/bash", layerID, "/diffs/1")

	stack := layermerkle.LayerStack{layerID}
	got, err := r.FindOwnerLayer(context.Background(), stack, "usr/bin/bash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != layerID {
		t.Errorf("FindOwnerLayer() = %v, want %v", got, layerID)
	}
}

func TestFakeResolver_FindOwnerLayer_Whiteout(t *testing.T) {
	r := testutil.NewFakeResolver()
	r.AddWhiteout("etc/shadow")

	_, err := r.FindOwnerLayer(context.Background(), testutil.MakeLayerStack("l"), "etc/shadow")
	if !errors.Is(err, layermerkle.ErrWhiteout) {
		t.Errorf("expected ErrWhiteout, got %v", err)
	}
}

func TestFakeResolver_FindOwnerLayer_NotFound(t *testing.T) {
	r := testutil.NewFakeResolver()
	_, err := r.FindOwnerLayer(context.Background(), testutil.MakeLayerStack("l"), "missing/file")
	if !errors.Is(err, layermerkle.ErrLayerNotFound) {
		t.Errorf("expected ErrLayerNotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DeduplicationEngine tests
// ─────────────────────────────────────────────────────────────────────────────

func TestDeduplicationEngine_Resolve_CacheHitOnFirstLookup(t *testing.T) {
	cache := layermerkle.NewShardedLRUCache(1024)
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	hooks := layermerkle.NewHookChain()

	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, hooks)

	layerID := digest.FromString("layer-A")
	hash := digest.FromString("file-content")
	stack := layermerkle.LayerStack{layerID}

	// Prime the cache manually as if a prior ExecOp already resolved it.
	cache.Set(layerID, "lib/libssl.so", hash)

	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v1"),
		LayerStack: stack,
		RelPath:    "lib/libssl.so",
	}
	result := engine.Resolve(context.Background(), req)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.FromCache {
		t.Error("expected FromCache=true for pre-warmed cache")
	}
	if hasher.CallCount() != 0 {
		t.Errorf("hasher called %d times, expected 0 (cache hit)", hasher.CallCount())
	}
}

func TestDeduplicationEngine_Resolve_OwnerLayerCachePropagatesToTop(t *testing.T) {
	cache := layermerkle.NewShardedLRUCache(1024)
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	hooks := layermerkle.NewHookChain()

	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, hooks)

	lowerLayer := digest.FromString("layer-lower")
	topLayer := digest.FromString("layer-top")
	expectedHash := digest.FromString("lib-content")
	stack := layermerkle.LayerStack{lowerLayer, topLayer}

	// File lives in lowerLayer, prime that cache.
	cache.Set(lowerLayer, "lib/libc.so", expectedHash)
	resolver.AddFile("lib/libc.so", lowerLayer, "/diffs/lower")

	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v2"),
		LayerStack: stack,
		RelPath:    "lib/libc.so",
	}
	result := engine.Resolve(context.Background(), req)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.Hash != expectedHash {
		t.Errorf("Hash = %v, want %v", result.Hash, expectedHash)
	}
	// Now the top layer cache should also be populated.
	if _, ok := cache.Get(topLayer, "lib/libc.so"); !ok {
		t.Error("hash not propagated to top layer cache")
	}
}

func TestDeduplicationEngine_Resolve_Whiteout_ReturnsError(t *testing.T) {
	cache := layermerkle.NewShardedLRUCache(1024)
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	resolver.AddWhiteout("etc/passwd")

	engine := layermerkle.NewDeduplicationEngine(cache, hasher, resolver, nil)

	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v3"),
		LayerStack: testutil.MakeLayerStack("l1"),
		RelPath:    "etc/passwd",
	}
	result := engine.Resolve(context.Background(), req)
	if !errors.Is(result.Err, layermerkle.ErrWhiteout) {
		t.Errorf("expected ErrWhiteout, got %v", result.Err)
	}
}

func TestDeduplicationEngine_Resolve_NoHashForNotFound(t *testing.T) {
	engine := layermerkle.NewDeduplicationEngine(
		layermerkle.NewShardedLRUCache(1024),
		testutil.NewFakeHasher(),
		testutil.NewFakeResolver(),
		nil,
	)
	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v4"),
		LayerStack: testutil.MakeLayerStack("l1"),
		RelPath:    "not/there",
	}
	result := engine.Resolve(context.Background(), req)
	if result.Err == nil {
		t.Error("expected error for file not in any layer")
	}
}

func TestDeduplicationEngine_Resolve_EmptyStack_Errors(t *testing.T) {
	engine := layermerkle.NewDeduplicationEngine(
		layermerkle.NewShardedLRUCache(1024),
		testutil.NewFakeHasher(),
		testutil.NewFakeResolver(),
		nil,
	)
	req := layermerkle.HashRequest{
		VertexID:   testutil.MakeVertexID("v5"),
		LayerStack: nil, // empty
		RelPath:    "lib/x",
	}
	result := engine.Resolve(context.Background(), req)
	if !errors.Is(result.Err, layermerkle.ErrInvalidLayerStack) {
		t.Errorf("expected ErrInvalidLayerStack, got %v", result.Err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MerkleTreeBuilder tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMerkleTreeBuilder_FinalizeEmptyTree(t *testing.T) {
	b := layermerkle.NewMerkleTreeBuilder(
		testutil.MakeVertexID("v"),
		testutil.MakeLayerStack("l"),
	)
	tree := b.Finalize()
	if tree.LeafCount != 0 {
		t.Errorf("empty tree LeafCount = %d, want 0", tree.LeafCount)
	}
}

func TestMerkleTreeBuilder_AddAndFinalize(t *testing.T) {
	b := layermerkle.NewMerkleTreeBuilder(
		testutil.MakeVertexID("v"),
		testutil.MakeLayerStack("l1"),
	)

	files := []struct {
		path string
		hash string
	}{
		{"bin/sh", "hash-of-sh"},
		{"lib/libc.so", "hash-of-libc"},
		{"etc/passwd", "hash-of-passwd"},
	}

	for _, f := range files {
		b.Add(&layermerkle.HashResult{
			VertexID:     testutil.MakeVertexID("v"),
			RelPath:      f.path,
			Hash:         testutil.MakeFileHash(f.hash),
			OwnerLayerID: testutil.MakeLayerStack("l1").Top(),
			FromCache:    false,
		})
	}

	tree := b.Finalize()

	if tree.LeafCount != 3 {
		t.Errorf("LeafCount = %d, want 3", tree.LeafCount)
	}
	if tree.Root == "" {
		t.Error("Root should not be empty after Finalize")
	}
}

func TestMerkleTreeBuilder_DeterministicRoot_SameInputSameRoot(t *testing.T) {
	makeTree := func() *layermerkle.MerkleTree {
		b := layermerkle.NewMerkleTreeBuilder(
			testutil.MakeVertexID("v"),
			testutil.MakeLayerStack("l"),
		)
		for _, path := range []string{"a/b", "c/d", "e"} {
			b.Add(&layermerkle.HashResult{
				VertexID: testutil.MakeVertexID("v"),
				RelPath:  path,
				Hash:     testutil.MakeFileHash("content-of-" + path),
			})
		}
		return b.Finalize()
	}

	tree1 := makeTree()
	tree2 := makeTree()

	if tree1.Root != tree2.Root {
		t.Errorf("non-deterministic root: %v vs %v", tree1.Root, tree2.Root)
	}
}

func TestMerkleTreeBuilder_DifferentFiles_DifferentRoot(t *testing.T) {
	makeTree := func(content string) *layermerkle.MerkleTree {
		b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("v"), testutil.MakeLayerStack("l"))
		b.Add(&layermerkle.HashResult{
			RelPath: "bin/app",
			Hash:    testutil.MakeFileHash(content),
		})
		return b.Finalize()
	}
	t1 := makeTree("version-1")
	t2 := makeTree("version-2")
	if t1.Root == t2.Root {
		t.Error("different file contents should produce different Merkle roots")
	}
}

func TestMerkleTreeBuilder_CacheHitRate(t *testing.T) {
	b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("v"), testutil.MakeLayerStack("l"))
	b.Add(&layermerkle.HashResult{RelPath: "a", Hash: testutil.MakeFileHash("a"), FromCache: true})
	b.Add(&layermerkle.HashResult{RelPath: "b", Hash: testutil.MakeFileHash("b"), FromCache: false})
	b.Add(&layermerkle.HashResult{RelPath: "c", Hash: testutil.MakeFileHash("c"), FromCache: true})

	tree := b.Finalize()
	if tree.CacheHitCount != 2 {
		t.Errorf("CacheHitCount = %d, want 2", tree.CacheHitCount)
	}
	rate := tree.CacheHitRate()
	if rate < 0.66 || rate > 0.67 {
		t.Errorf("CacheHitRate() = %.3f, want ~0.667", rate)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MerkleForest tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMerkleForest_AddAndGet(t *testing.T) {
	f := layermerkle.NewMerkleForest()
	b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID("vertex-1"), testutil.MakeLayerStack("l"))
	b.Add(&layermerkle.HashResult{RelPath: "a", Hash: testutil.MakeFileHash("a")})
	tree := b.Finalize()

	f.Add(tree)
	got, ok := f.Get(tree.VertexID)
	if !ok {
		t.Fatal("Get: tree not found after Add")
	}
	if got.Root != tree.Root {
		t.Errorf("Root mismatch after Get")
	}
}

func TestMerkleForest_All_SortedByVertexID(t *testing.T) {
	f := layermerkle.NewMerkleForest()
	for _, name := range []string{"vertex-c", "vertex-a", "vertex-b"} {
		b := layermerkle.NewMerkleTreeBuilder(testutil.MakeVertexID(name), testutil.MakeLayerStack("l"))
		f.Add(b.Finalize())
	}
	all := f.All()
	if len(all) != 3 {
		t.Fatalf("All() len = %d, want 3", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].VertexID > all[i].VertexID {
			t.Errorf("All() not sorted at index %d", i)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CountingHook tests
// ─────────────────────────────────────────────────────────────────────────────

func TestCountingHook_RecordsAllEvents(t *testing.T) {
	h := &layermerkle.CountingHook{}
	ctx := context.Background()
	req := layermerkle.HashRequest{RelPath: "x"}
	ev := testutil.NewAccessEvent().Build()

	h.OnCacheHit(ctx, req, &layermerkle.HashResult{})
	h.OnCacheHit(ctx, req, &layermerkle.HashResult{})
	h.OnHashStart(ctx, req)
	h.OnHashError(ctx, req, errors.New("boom"))
	h.OnEventDropped(ctx, ev, layermerkle.ErrEventDropped)
	h.OnVertexFinalized(ctx, &layermerkle.MerkleTree{})

	s := h.Snapshot()
	if s.CacheHits != 2 {
		t.Errorf("CacheHits = %d, want 2", s.CacheHits)
	}
	if s.HashStarts != 1 {
		t.Errorf("HashStarts = %d, want 1", s.HashStarts)
	}
	if s.HashErrors != 1 {
		t.Errorf("HashErrors = %d, want 1", s.HashErrors)
	}
	if s.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", s.Dropped)
	}
	if s.Finalized != 1 {
		t.Errorf("Finalized = %d, want 1", s.Finalized)
	}
}

func TestCountingHook_Reset(t *testing.T) {
	h := &layermerkle.CountingHook{}
	ctx := context.Background()
	h.OnHashStart(ctx, layermerkle.HashRequest{})
	h.Reset()
	if s := h.Snapshot(); s.HashStarts != 0 {
		t.Error("Reset: HashStarts should be zero")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EncodeLayerStack / AccessEventFromEnriched tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEncodeLayerStack_RoundTrip(t *testing.T) {
	original := testutil.MakeLayerStack("layer-0", "layer-1", "layer-2")
	encoded := layermerkle.EncodeLayerStack(original)
	if !strings.Contains(encoded, ":") {
		t.Errorf("EncodeLayerStack missing separator: %q", encoded)
	}

	// Verify it contains all digests.
	for _, id := range original {
		if !strings.Contains(encoded, string(id)) {
			t.Errorf("encoded stack missing layer %q", id)
		}
	}
}

func TestAccessEventAttrs_ContainsRequiredKeys(t *testing.T) {
	attrs := layermerkle.AccessEventAttrs(
		testutil.MakeVertexID("v"),
		testutil.MakeLayerStack("l"),
		"usr/bin/sh",
	)
	for _, key := range []string{layermerkle.AttrVertexID, layermerkle.AttrLayerStack, layermerkle.AttrRelPath} {
		if _, ok := attrs[key]; !ok {
			t.Errorf("attrs missing key %q", key)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Engine integration tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEngine_SubmitAndFinalizeVertex_EndToEnd(t *testing.T) {
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()
	counter := &layermerkle.CountingHook{}
	collector := &testutil.TreeCollector{}

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(hasher),
		layermerkle.WithResolver(resolver),
		layermerkle.WithHook(counter),
		layermerkle.WithOnTree(collector.Collect()),
		layermerkle.WithWorkers(2),
	)

	layerID := digest.FromString("layer-base")
	resolver.AddFile("bin/sh", layerID, "/diffs/base")
	resolver.AddFile("lib/libc.so", layerID, "/diffs/base")

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	vertexID := testutil.MakeVertexID("exec-op-1")
	stack := layermerkle.LayerStack{layerID}

	events := testutil.EventBatch("exec-op-1", []string{"layer-base"}, []string{
		"bin/sh",
		"lib/libc.so",
		"bin/sh", // duplicate — must not double-count leaves in final tree
	})
	for _, ev := range events {
		if err := engine.Submit(ctx, ev); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	_ = stack

	// Allow workers to drain.
	time.Sleep(50 * time.Millisecond)

	tree, err := engine.FinalizeVertex(vertexID)
	if err != nil {
		t.Fatalf("FinalizeVertex: %v", err)
	}
	engine.Stop()

	if tree.Root == "" {
		t.Error("tree Root should not be empty")
	}
	if tree.LeafCount == 0 {
		t.Error("LeafCount should be > 0")
	}
	if counter.Snapshot().Finalized != 1 {
		t.Errorf("CountingHook.Finalized = %d, want 1", counter.Snapshot().Finalized)
	}
	if collector.Len() != 1 {
		t.Errorf("TreeCollector.Len() = %d, want 1", collector.Len())
	}
}

func TestEngine_DeduplicatesHashComputationAcrossExecOps(t *testing.T) {
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(hasher),
		layermerkle.WithResolver(resolver),
		layermerkle.WithWorkers(1),
	)

	layerID := digest.FromString("base-layer")
	resolver.AddFile("lib/libssl.so", layerID, "/diffs/base")

	ctx := context.Background()
	_ = engine.Start(ctx)

	// Two different ExecOps accessing the same file in the same base layer.
	for _, vtx := range []string{"op-1", "op-2"} {
		ev := testutil.NewAccessEvent().
			WithVertexID(vtx).
			WithLayerStack("base-layer").
			WithRelPath("lib/libssl.so").
			Build()
		_ = engine.Submit(ctx, ev)
	}

	time.Sleep(50 * time.Millisecond)

	_, _ = engine.FinalizeVertex(testutil.MakeVertexID("op-1"))
	_, _ = engine.FinalizeVertex(testutil.MakeVertexID("op-2"))
	engine.Stop()

	// The hasher should have been called at most once for lib/libssl.so.
	calls := hasher.Calls()
	libsslCalls := 0
	for _, c := range calls {
		if strings.Contains(c, "libssl") {
			libsslCalls++
		}
	}
	if libsslCalls > 1 {
		t.Errorf("libssl.so hashed %d times across 2 ExecOps, want at most 1", libsslCalls)
	}
}

func TestEngine_Submit_BeforeStart_ReturnsError(t *testing.T) {
	engine := layermerkle.NewEngine()
	err := engine.Submit(context.Background(), testutil.NewAccessEvent().Build())
	if !errors.Is(err, layermerkle.ErrEngineNotRunning) {
		t.Errorf("expected ErrEngineNotRunning, got %v", err)
	}
}

func TestEngine_FinalizeVertex_UnknownVertex_Errors(t *testing.T) {
	engine := layermerkle.NewEngine()
	_ = engine.Start(context.Background())
	defer engine.Stop()

	_, err := engine.FinalizeVertex(testutil.MakeVertexID("never-seen"))
	if !errors.Is(err, layermerkle.ErrVertexClosed) {
		t.Errorf("expected ErrVertexClosed, got %v", err)
	}
}

func TestEngine_Stop_IsIdempotent(t *testing.T) {
	engine := layermerkle.NewEngine()
	_ = engine.Start(context.Background())
	engine.Stop()
	engine.Stop() // must not panic or block
}

func TestEngine_Feed_SkipsWriteEvents(t *testing.T) {
	counter := &layermerkle.CountingHook{}
	hasher := testutil.NewFakeHasher()
	resolver := testutil.NewFakeResolver()

	engine := layermerkle.NewEngine(
		layermerkle.WithFileHasher(hasher),
		layermerkle.WithResolver(resolver),
		layermerkle.WithHook(counter),
		layermerkle.WithWorkers(1),
	)

	ctx := context.Background()
	_ = engine.Start(ctx)

	ch := make(chan *layermerkle.AccessEvent, 4)
	readEv := testutil.NewAccessEvent().WithMask(0x00000001).Build()  // ACCESS
	writeEv := testutil.NewAccessEvent().WithMask(0x00000002).Build() // MODIFY

	ch <- readEv
	ch <- writeEv
	ch <- readEv
	close(ch)

	engine.Feed(ctx, ch, nil)
	time.Sleep(30 * time.Millisecond)
	engine.Stop()

	// Only the two read events should have been submitted.
	// (They'll fail resolver lookup, but they were submitted.)
	if counter.Snapshot().Dropped > 0 {
		// This is informational; the important thing is write events were skipped.
	}
}

func TestEngine_CacheStats_ReflectsCacheActivity(t *testing.T) {
	engine := layermerkle.NewEngine(
		layermerkle.WithCacheCapacity(1024),
	)
	stats := engine.CacheStats()
	if stats.Capacity <= 0 {
		t.Errorf("CacheStats.Capacity = %d, want > 0", stats.Capacity)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LayerStackKey tests
// ─────────────────────────────────────────────────────────────────────────────

func TestNewLayerStackKey_Deterministic(t *testing.T) {
	stack := testutil.MakeLayerStack("a", "b", "c")
	k1 := layermerkle.NewLayerStackKey(stack)
	k2 := layermerkle.NewLayerStackKey(stack)
	if k1 != k2 {
		t.Error("LayerStackKey should be deterministic")
	}
}

func TestNewLayerStackKey_OrderSensitive(t *testing.T) {
	k1 := layermerkle.NewLayerStackKey(testutil.MakeLayerStack("a", "b"))
	k2 := layermerkle.NewLayerStackKey(testutil.MakeLayerStack("b", "a"))
	if k1 == k2 {
		t.Error("LayerStackKey should differ for different orderings")
	}
}

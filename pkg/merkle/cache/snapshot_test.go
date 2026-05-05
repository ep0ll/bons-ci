package cache_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/user/layermerkle/cache"
	"github.com/user/layermerkle/layer"
)

// ─── Snapshot / Restore ───────────────────────────────────────────────────────

func TestSnapshot_Restore_RoundTrip(t *testing.T) {
	src := cache.NewShardedCache()
	l := layer.Digest("snap-layer")

	// Populate with regular entries and one tombstone.
	src.Set(makeKey("/a", l), makeEntry([]byte{0xAA}))
	src.Set(makeKey("/b", l), makeEntry([]byte{0xBB}))
	src.Set(makeKey("/del", l), cache.CacheEntry{
		Tombstone: true, SourceLayer: l, CachedAt: time.Now(),
	})

	// Snapshot to buffer.
	var buf bytes.Buffer
	if err := src.Snapshot(&buf); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("Snapshot produced empty output")
	}

	// Restore into a fresh cache.
	dst := cache.NewShardedCache()
	restored, skipped, err := dst.Restore(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored != 3 {
		t.Fatalf("expected 3 restored, got %d (skipped=%d)", restored, skipped)
	}
	if skipped != 0 {
		t.Fatalf("expected 0 skipped, got %d", skipped)
	}

	// Verify entries in dst.
	if e, ok := dst.Get(makeKey("/a", l)); !ok || e.Hash[0] != 0xAA {
		t.Fatal("Restore: /a not found or wrong hash")
	}
	if e, ok := dst.Get(makeKey("/b", l)); !ok || e.Hash[0] != 0xBB {
		t.Fatal("Restore: /b not found or wrong hash")
	}
	if e, ok := dst.Get(makeKey("/del", l)); !ok || !e.Tombstone {
		t.Fatal("Restore: tombstone not preserved")
	}
}

func TestRestore_SkipsExisting(t *testing.T) {
	src := cache.NewShardedCache()
	l := layer.Digest("skip-layer")
	src.Set(makeKey("/f", l), makeEntry([]byte{0x11}))

	var buf bytes.Buffer
	src.Snapshot(&buf) //nolint:errcheck

	// Pre-fill dst with a different entry for the same key.
	dst := cache.NewShardedCache()
	dst.Set(makeKey("/f", l), makeEntry([]byte{0xFF}))

	_, skipped, err := dst.Restore(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", skipped)
	}

	// The existing entry must not be overwritten.
	e, _ := dst.Get(makeKey("/f", l))
	if e.Hash[0] != 0xFF {
		t.Fatal("Restore must not overwrite existing entry (SetIfAbsent semantics)")
	}
}

func TestSnapshot_EmptyCache(t *testing.T) {
	c := cache.NewShardedCache()
	var buf bytes.Buffer
	if err := c.Snapshot(&buf); err != nil {
		t.Fatalf("Snapshot empty cache: %v", err)
	}
	// Empty cache → empty output.
	if buf.Len() != 0 {
		t.Fatalf("expected empty output, got %d bytes", buf.Len())
	}
}

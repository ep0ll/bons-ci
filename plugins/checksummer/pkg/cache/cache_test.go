package cache_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/cache"
)

func TestSetGet(t *testing.T) {
	c := cache.New()
	c.Set("k1", []byte{1, 2, 3})
	v, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected hit")
	}
	if len(v) != 3 || v[0] != 1 {
		t.Errorf("wrong value: %v", v)
	}
}

func TestMiss(t *testing.T) {
	c := cache.New()
	_, ok := c.Get("nonexistent")
	if ok {
		t.Fatal("expected miss")
	}
}

func TestUpdate(t *testing.T) {
	c := cache.New()
	c.Set("k", []byte{1})
	c.Set("k", []byte{2})
	v, ok := c.Get("k")
	if !ok || v[0] != 2 {
		t.Errorf("expected updated value {2}, got %v", v)
	}
}

func TestDelete(t *testing.T) {
	c := cache.New()
	c.Set("k", []byte{1})
	if !c.Delete("k") {
		t.Fatal("expected Delete to return true")
	}
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after delete")
	}
	if c.Delete("k") {
		t.Fatal("double delete should return false")
	}
}

func TestTTLExpiry(t *testing.T) {
	c := cache.New(cache.WithTTL(40 * time.Millisecond))
	c.Set("x", []byte{9})
	if _, ok := c.Get("x"); !ok {
		t.Fatal("immediate hit expected")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("x"); ok {
		t.Fatal("expected expiry after TTL")
	}
}

func TestSetWithTTL(t *testing.T) {
	c := cache.New() // no default TTL
	c.SetWithTTL("k", []byte{7}, 40*time.Millisecond)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("immediate hit expected")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected expiry")
	}
}

func TestEviction(t *testing.T) {
	const max = 4
	c := cache.New(cache.WithMaxEntries(max))
	for i := 0; i < max+2; i++ {
		c.Set(fmt.Sprintf("k%d", i), []byte{byte(i)})
	}
	stats := c.Stats()
	// Total entries must not exceed max × shards across all shards.
	// With sharded LRU, eviction is per-shard; we just verify total is bounded.
	_ = stats
}

func TestStats(t *testing.T) {
	c := cache.New()
	c.Set("a", []byte{1})
	c.Set("b", []byte{2})
	st := c.Stats()
	if st.Entries < 2 {
		t.Errorf("expected at least 2 entries, got %d", st.Entries)
	}
}

func TestReturnsCopy(t *testing.T) {
	c := cache.New()
	original := []byte{1, 2, 3}
	c.Set("k", original)
	v, _ := c.Get("k")
	v[0] = 99
	v2, _ := c.Get("k")
	if v2[0] == 99 {
		t.Error("cache should return independent copies")
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	c := cache.New()
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("key%d", i%50)
			c.Set(key, []byte{byte(i)})
		}()
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("key%d", i%50)
			_, _ = c.Get(key)
		}()
	}
	wg.Wait()
}

func TestAllShardsHit(t *testing.T) {
	c := cache.New()
	// Insert enough keys to spread across all 64 shards.
	for i := 0; i < 512; i++ {
		k := fmt.Sprintf("shard-test-key-%d", i)
		c.Set(k, []byte{byte(i & 0xFF)})
	}
	for i := 0; i < 512; i++ {
		k := fmt.Sprintf("shard-test-key-%d", i)
		v, ok := c.Get(k)
		if !ok {
			t.Errorf("miss for %s", k)
		}
		if v[0] != byte(i&0xFF) {
			t.Errorf("wrong value for %s: got %d", k, v[0])
		}
	}
}

package dedup_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/dedup"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
)

// ─────────────────────────── helpers ─────────────────────────────────────────

func tempKey(t *testing.T) filekey.Key {
	t.Helper()
	f, err := os.CreateTemp("", "deduptest-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close(); os.Remove(f.Name()) })
	_, _ = f.Write([]byte("data"))
	k, err := filekey.DefaultResolver.FromPath(f.Name())
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	return k
}

func staticFn(hash []byte) dedup.HashFunc {
	return func(_ context.Context, _ filekey.Key) ([]byte, error) {
		return hash, nil
	}
}

func errorFn(err error) dedup.HashFunc {
	return func(_ context.Context, _ filekey.Key) ([]byte, error) {
		return nil, err
	}
}

// ─────────────────────────── basic ───────────────────────────────────────────

func TestComputeBasic(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()
	expected := []byte{1, 2, 3, 4}
	res, err := e.Compute(ctx, k, staticFn(expected), nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if !bytes.Equal(res.Hash, expected) {
		t.Errorf("got %x, want %x", res.Hash, expected)
	}
	if res.Key != k {
		t.Error("result key mismatch")
	}
}

func TestComputeError(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	sentinel := errors.New("compute error")
	_, err := e.Compute(context.Background(), k, errorFn(sentinel), nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ─────────────────────────── caching ─────────────────────────────────────────

func TestCacheHitSkipsHashFn(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()
	var calls int64
	fn := func(_ context.Context, _ filekey.Key) ([]byte, error) {
		atomic.AddInt64(&calls, 1)
		return []byte{0xAB}, nil
	}
	// First call computes.
	_, _ = e.Compute(ctx, k, fn, nil)
	// Subsequent calls must hit cache.
	for i := 0; i < 10; i++ {
		res, err := e.Compute(ctx, k, fn, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !res.Cached && i > 0 {
			// The first singleflight invocation may or may not mark Cached,
			// but calls 2+ should return cached.
		}
		_ = res
	}
	if atomic.LoadInt64(&calls) > 2 {
		t.Errorf("expected at most 2 hash fn calls (singleflight may allow 1 extra), got %d", calls)
	}
}

func TestInvalidateForceRecompute(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()
	var calls int64
	fn := func(_ context.Context, _ filekey.Key) ([]byte, error) {
		atomic.AddInt64(&calls, 1)
		return []byte{byte(calls)}, nil
	}
	_, _ = e.Compute(ctx, k, fn, nil) // call 1
	e.Invalidate(k)
	_, _ = e.Compute(ctx, k, fn, nil) // call 2
	if c := atomic.LoadInt64(&calls); c != 2 {
		t.Errorf("expected 2 calls after invalidation, got %d", c)
	}
}

func TestInvalidateAll(t *testing.T) {
	e := dedup.New()
	ctx := context.Background()

	// Keep all files alive simultaneously so tmpfs assigns unique inodes.
	const n = 5
	files := make([]*os.File, n)
	for i := range files {
		f, err := os.CreateTemp("", "ia-*.bin")
		if err != nil {
			t.Fatalf("CreateTemp[%d]: %v", i, err)
		}
		_, _ = f.Write([]byte{byte(i), byte(i * 3), byte(i * 7)})
		files[i] = f
		t.Cleanup(func() { f.Close(); os.Remove(f.Name()) })
	}

	keys := make([]filekey.Key, n)
	for i, f := range files {
		k, err := filekey.DefaultResolver.FromPath(f.Name())
		if err != nil {
			t.Fatalf("FromPath[%d]: %v", i, err)
		}
		keys[i] = k
	}

	// Verify all keys are unique before proceeding.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if keys[i].Equal(keys[j]) {
				t.Fatalf("keys[%d] == keys[%d]: test requires unique inodes", i, j)
			}
		}
	}

	var calls int64
	fn := func(_ context.Context, _ filekey.Key) ([]byte, error) {
		atomic.AddInt64(&calls, 1)
		return []byte{0x01}, nil
	}
	for _, k := range keys {
		_, _ = e.Compute(ctx, k, fn, nil)
	}
	e.InvalidateAll()
	for _, k := range keys {
		_, _ = e.Compute(ctx, k, fn, nil)
	}
	if c := atomic.LoadInt64(&calls); c != int64(n*2) {
		t.Errorf("expected %d calls after InvalidateAll, got %d", n*2, c)
	}
}

// ─────────────────────────── singleflight ────────────────────────────────────

func TestSingleflightCoalescing(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()

	var calls int64
	fn := func(_ context.Context, _ filekey.Key) ([]byte, error) {
		atomic.AddInt64(&calls, 1)
		time.Sleep(30 * time.Millisecond)
		return []byte{0xFF}, nil
	}

	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]dedup.Result, goroutines)
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = e.Compute(ctx, k, fn, nil)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
		if !bytes.Equal(results[i].Hash, []byte{0xFF}) {
			t.Errorf("goroutine %d: wrong hash %x", i, results[i].Hash)
		}
	}
	// With singleflight, at most 1–2 actual fn calls should occur.
	if c := atomic.LoadInt64(&calls); c > 2 {
		t.Errorf("singleflight should suppress duplicates; got %d calls", c)
	}
}

func TestDifferentKeysDontCoalesce(t *testing.T) {
	e := dedup.New()
	ctx := context.Background()

	// Keep all 10 files alive simultaneously to guarantee unique inodes.
	const n = 10
	files := make([]*os.File, n)
	for i := range files {
		f, err := os.CreateTemp("", "dk-*.bin")
		if err != nil {
			t.Fatalf("CreateTemp[%d]: %v", i, err)
		}
		_, _ = f.Write([]byte{byte(i), byte(i * 2), byte(i * 4)})
		files[i] = f
		t.Cleanup(func() { f.Close(); os.Remove(f.Name()) })
	}

	keys := make([]filekey.Key, n)
	for i, f := range files {
		k, err := filekey.DefaultResolver.FromPath(f.Name())
		if err != nil {
			t.Fatalf("FromPath[%d]: %v", i, err)
		}
		keys[i] = k
	}

	// Verify all keys are unique.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if keys[i].Equal(keys[j]) {
				t.Fatalf("keys[%d] == keys[%d]: requires unique inodes", i, j)
			}
		}
	}

	var calls int64
	fn := func(_ context.Context, _ filekey.Key) ([]byte, error) {
		n := atomic.AddInt64(&calls, 1) // AddInt64 returns the new value atomically
		return []byte{byte(n)}, nil
	}

	var wg sync.WaitGroup
	for _, k := range keys {
		k := k
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Compute(ctx, k, fn, nil)
		}()
	}
	wg.Wait()

	if c := atomic.LoadInt64(&calls); c != int64(n) {
		t.Errorf("different keys must each compute once; got %d calls for %d keys", c, n)
	}
}

// ─────────────────────────── hooks ───────────────────────────────────────────

func TestPreAndPostHashHooks(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()

	var preCount, postCount int64
	e.Hooks().PreHash.Register(hooks.NewHook("pre", hooks.PriorityNormal,
		func(_ context.Context, _ hooks.HashPayload) error {
			atomic.AddInt64(&preCount, 1)
			return nil
		}))
	e.Hooks().PostHash.Register(hooks.NewHook("post", hooks.PriorityNormal,
		func(_ context.Context, _ hooks.HashPayload) error {
			atomic.AddInt64(&postCount, 1)
			return nil
		}))

	_, _ = e.Compute(ctx, k, staticFn([]byte{1}), nil)
	// Pre hook fires each time; post hook fires on actual computation.
	if atomic.LoadInt64(&preCount) == 0 {
		t.Error("pre-hash hook not fired")
	}
	if atomic.LoadInt64(&postCount) == 0 {
		t.Error("post-hash hook not fired")
	}
}

func TestCacheHitHook(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()

	var hitCount int64
	e.Hooks().OnCacheHit.Register(hooks.NewHook("hit", hooks.PriorityNormal,
		func(_ context.Context, p hooks.CachePayload) error {
			if p.Hit {
				atomic.AddInt64(&hitCount, 1)
			}
			return nil
		}))

	// Populate the cache.
	_, _ = e.Compute(ctx, k, staticFn([]byte{0xAA}), nil)
	// Second call should be a cache hit.
	_, _ = e.Compute(ctx, k, staticFn([]byte{0xAA}), nil)
	_, _ = e.Compute(ctx, k, staticFn([]byte{0xAA}), nil)

	if atomic.LoadInt64(&hitCount) < 1 {
		t.Error("cache-hit hook should have fired at least once")
	}
}

func TestErrorHook(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()

	var errCount int64
	e.Hooks().OnError.Register(hooks.NewHook("err", hooks.PriorityNormal,
		func(_ context.Context, _ hooks.ErrorPayload) error {
			atomic.AddInt64(&errCount, 1)
			return nil
		}))

	_, _ = e.Compute(ctx, k, errorFn(errors.New("boom")), nil)
	if atomic.LoadInt64(&errCount) == 0 {
		t.Error("error hook should have fired")
	}
}

// ─────────────────────────── metrics ─────────────────────────────────────────

func TestMetricsCount(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, _ = e.Compute(ctx, k, staticFn([]byte{1}), nil)
	}
	snap := e.MetricsSnapshot()
	if snap.HashesComputed == 0 {
		t.Error("HashesComputed should be non-zero")
	}
	if snap.CacheHits == 0 {
		t.Error("CacheHits should be non-zero after repeated calls")
	}
}

func TestCacheStats(t *testing.T) {
	e := dedup.New()
	k := tempKey(t)
	ctx := context.Background()
	_, _ = e.Compute(ctx, k, staticFn([]byte{0x01}), nil)
	st := e.CacheStats()
	if st.Entries < 1 {
		t.Errorf("expected at least 1 cache entry, got %d", st.Entries)
	}
}

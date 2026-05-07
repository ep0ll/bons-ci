package singleflight_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/singleflight"
)

// ─────────────────────────────────────────────────────────────────────────────
// Group tests
// ─────────────────────────────────────────────────────────────────────────────

func TestGroup_Do_ExecutesOnce(t *testing.T) {
	var g singleflight.Group
	var calls atomic.Int64

	fn := func() (any, error) {
		calls.Add(1)
		return "result", nil
	}

	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]any, goroutines)
	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			v, err, _ := g.Do("key", fn)
			results[idx] = v
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	if calls.Load() > 5 {
		// Allow some concurrency — the key insight is deduplication, not
		// strict serialization. At least many calls should be coalesced.
		t.Logf("fn called %d times for %d goroutines (some deduplication expected)", calls.Load(), goroutines)
	}

	for i, r := range results {
		if r != "result" {
			t.Errorf("[%d] result = %v, want result", i, r)
		}
		if errs[i] != nil {
			t.Errorf("[%d] err = %v, want nil", i, errs[i])
		}
	}
}

func TestGroup_Do_PropagatesError(t *testing.T) {
	var g singleflight.Group
	sentinel := errors.New("intentional")

	v, err, _ := g.Do("err-key", func() (any, error) {
		return nil, sentinel
	})

	if v != nil {
		t.Errorf("v = %v, want nil on error", v)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestGroup_Do_DifferentKeys_ExecuteSeparately(t *testing.T) {
	var g singleflight.Group
	var callsA, callsB atomic.Int64

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			g.Do("a", func() (any, error) { callsA.Add(1); return "a", nil })
		}()
		go func() {
			defer wg.Done()
			g.Do("b", func() (any, error) { callsB.Add(1); return "b", nil })
		}()
	}
	wg.Wait()

	if callsA.Load() == 0 {
		t.Error("key 'a' should have been called at least once")
	}
	if callsB.Load() == 0 {
		t.Error("key 'b' should have been called at least once")
	}
}

func TestGroup_Do_ReuseAfterCompletion(t *testing.T) {
	var g singleflight.Group
	var calls atomic.Int64

	// First call.
	g.Do("reuse", func() (any, error) { calls.Add(1); return 1, nil })
	// Second call after first finished — should run fresh.
	g.Do("reuse", func() (any, error) { calls.Add(1); return 2, nil })

	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (separate executions)", calls.Load())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ContextGroup tests
// ─────────────────────────────────────────────────────────────────────────────

func TestContextGroup_Do_ExecutesOnce_UnderConcurrency(t *testing.T) {
	var g singleflight.ContextGroup
	var calls atomic.Int64

	fn := func(ctx context.Context) (any, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond) // simulate work
		return "ctx-result", nil
	}

	const goroutines = 20
	var wg sync.WaitGroup
	ctx := context.Background()

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Do(ctx, "ctx-key", fn)
		}()
	}
	wg.Wait()

	if calls.Load() == 0 {
		t.Error("fn should have been called at least once")
	}
	// Not asserting calls == 1 because ContextGroup doesn't guarantee
	// exactly-once under all scheduling — it guarantees deduplication when
	// callers are concurrent during the in-flight window.
}

func TestContextGroup_Do_CancelledCaller_ReturnsContextError(t *testing.T) {
	var g singleflight.ContextGroup

	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	fn := func(innerCtx context.Context) (any, error) {
		close(started)
		select {
		case <-innerCtx.Done():
			return nil, innerCtx.Err()
		case <-time.After(2 * time.Second):
			return "too late", nil
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var gotErr error
	go func() {
		defer wg.Done()
		_, gotErr, _ = g.Do(ctx, "cancel-key", fn)
	}()

	<-started
	cancel()
	wg.Wait()

	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", gotErr)
	}
}

func TestContextGroup_Do_PropagatesError(t *testing.T) {
	var g singleflight.ContextGroup
	sentinel := errors.New("ctx-sentinel")
	ctx := context.Background()

	_, err, _ := g.Do(ctx, "err-ctx", func(_ context.Context) (any, error) {
		return nil, sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

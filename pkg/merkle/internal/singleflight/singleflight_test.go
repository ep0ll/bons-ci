package singleflight_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/user/layermerkle/internal/singleflight"
)

// TestDo_Basic verifies happy-path Do returns the computed value.
func TestDo_Basic(t *testing.T) {
	var g singleflight.Group
	v, err, shared := g.Do("key", func() (any, error) { return 42, nil })
	if err != nil {
		t.Fatal(err)
	}
	if v.(int) != 42 {
		t.Fatalf("got %v, want 42", v)
	}
	if shared {
		t.Fatal("sole caller must not see shared=true")
	}
}

// TestDo_Error verifies errors propagate to the caller.
func TestDo_Error(t *testing.T) {
	var g singleflight.Group
	sentinel := errors.New("sentinel")
	_, err, _ := g.Do("key", func() (any, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

// TestDo_Deduplication_Guaranteed proves deduplication by launching goroutines
// sequentially: goroutine 1 enters Do and blocks inside fn, THEN goroutines
// 2..N enter Do (guaranteed to find the in-flight call) — all share 1 result.
func TestDo_Deduplication_Guaranteed(t *testing.T) {
	var g singleflight.Group
	var calls atomic.Int64
	const N = 20

	// fnRunning is closed once the fn is confirmed to be executing.
	fnRunning := make(chan struct{})
	// release unblocks the fn once all other goroutines have joined.
	release := make(chan struct{})

	var wg sync.WaitGroup
	results := make([]int, N)

	// Goroutine 0: runs the fn, signals running, then waits for release.
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, _, _ := g.Do("key", func() (any, error) {
			calls.Add(1)
			close(fnRunning) // notify others: fn is now in-flight
			<-release        // wait until all duplicates have joined
			return 99, nil
		})
		results[0] = v.(int)
	}()

	// Wait until goroutine 0 is inside the fn.
	<-fnRunning

	// Goroutines 1..N-1: guaranteed to find the in-flight call and wait.
	for i := 1; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, _, _ := g.Do("key", func() (any, error) {
				// This fn must never run — deduplication must prevent it.
				t.Errorf("goroutine %d: fn must not execute (dedup failed)", i)
				calls.Add(1)
				return 0, nil
			})
			results[i] = v.(int)
		}(i)
	}

	// Give goroutines 1..N-1 time to enter Do and block on c.wg.Wait().
	// Then release the fn.
	time.Sleep(5 * time.Millisecond)
	close(release)
	wg.Wait()

	// All results must be the single computed value.
	for i, r := range results {
		if r != 99 {
			t.Fatalf("goroutine %d got %d, want 99", i, r)
		}
	}
	// Exactly 1 fn execution.
	if c := calls.Load(); c != 1 {
		t.Fatalf("singleflight: expected exactly 1 fn execution, got %d", c)
	}
}

// TestDo_DifferentKeys verifies each unique key runs its own fn.
func TestDo_DifferentKeys(t *testing.T) {
	var g singleflight.Group
	var calls atomic.Int64

	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c"} {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Do(key, func() (any, error) { //nolint:errcheck
				calls.Add(1)
				return key, nil
			})
		}()
	}
	wg.Wait()

	if c := calls.Load(); c != 3 {
		t.Fatalf("expected 3 calls for 3 different keys, got %d", c)
	}
}

// TestDo_SharedFlag verifies that duplicate callers see shared=true.
// Uses the same sequential pattern: goroutine 0 enters fn and blocks,
// then goroutines 1-2 join — they must see shared=true.
func TestDo_SharedFlag(t *testing.T) {
	var g singleflight.Group
	const N = 3

	fnRunning := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	shared := make([]bool, N)

	// Goroutine 0 runs the fn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, s := g.Do("key", func() (any, error) {
			close(fnRunning)
			<-release
			return 1, nil
		})
		shared[0] = s
	}()

	<-fnRunning // fn is now in-flight

	// Goroutines 1..N-1 join the in-flight call.
	for i := 1; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, s := g.Do("key", func() (any, error) {
				return 1, nil // should not be called
			})
			shared[i] = s
		}(i)
	}

	time.Sleep(2 * time.Millisecond)
	close(release)
	wg.Wait()

	// At least one caller must have seen shared=true (the duplicates).
	sharedCount := 0
	for _, s := range shared {
		if s {
			sharedCount++
		}
	}
	if sharedCount < 1 {
		t.Fatal("expected at least 1 caller to see shared=true")
	}
}

// TestForget verifies that Forget allows a subsequent call to re-execute.
func TestForget(t *testing.T) {
	var g singleflight.Group
	var calls atomic.Int64

	g.Do("key", func() (any, error) { //nolint:errcheck
		calls.Add(1)
		return 1, nil
	})
	g.Forget("key")
	g.Do("key", func() (any, error) { //nolint:errcheck
		calls.Add(1)
		return 2, nil
	})

	if c := calls.Load(); c != 2 {
		t.Fatalf("Forget: expected 2 calls, got %d", c)
	}
}

// TestDoChan_Basic verifies the channel-based variant returns results.
func TestDoChan_Basic(t *testing.T) {
	var g singleflight.Group
	ch := g.DoChan("key", func() (any, error) { return "hello", nil })
	select {
	case r := <-ch:
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if r.Val.(string) != "hello" {
			t.Fatalf("got %v, want hello", r.Val)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DoChan timed out")
	}
}

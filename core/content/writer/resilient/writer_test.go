package resilient_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/core/content/testutil"
	"github.com/bons/bons-ci/core/content/writer/resilient"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

func TestResilientWriter_Write_AbsorbsError(t *testing.T) {
	t.Parallel()

	inner := &testutil.MockWriter{
		WriteFn: func(p []byte) (int, error) {
			return 0, errors.New("disk full")
		},
	}

	w := resilient.Wrap(inner)
	n, err := w.Write([]byte("data"))
	if err != nil {
		t.Errorf("Write() should not return error after wrapping, got: %v", err)
	}
	if n != 4 {
		t.Errorf("Write() should report %d bytes written, got %d", 4, n)
	}
}

func TestResilientWriter_Write_SubsequentCallsAreNoOps(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	inner := &testutil.MockWriter{
		WriteFn: func(p []byte) (int, error) {
			n := calls.Add(1)
			if n == 1 {
				return 0, errors.New("first write fails")
			}
			return len(p), nil
		},
	}

	w := resilient.Wrap(inner)
	w.Write([]byte("first"))  //nolint:errcheck — deliberately ignored
	w.Write([]byte("second")) //nolint:errcheck

	// Only the first call should have reached the inner writer.
	if calls.Load() != 1 {
		t.Errorf("inner writer called %d times, want 1", calls.Load())
	}
}

func TestResilientWriter_Commit_AbsorbsError(t *testing.T) {
	t.Parallel()

	inner := &testutil.MockWriter{
		CommitFn: func(_ context.Context, _ int64, _ digest.Digest, _ ...content.Opt) error {
			return errors.New("commit failed")
		},
	}

	w := resilient.Wrap(inner)
	if err := w.Commit(context.Background(), 0, ""); err != nil {
		t.Errorf("Commit() should not return error, got: %v", err)
	}
}

func TestResilientWriter_Truncate_AbsorbsError(t *testing.T) {
	t.Parallel()

	inner := &testutil.MockWriter{
		TruncateFn: func(_ int64) error { return errors.New("truncate failed") },
	}

	w := resilient.Wrap(inner)
	if err := w.Truncate(0); err != nil {
		t.Errorf("Truncate() should not return error, got: %v", err)
	}
}

func TestResilientWriter_Close_CalledOnceOnFailure(t *testing.T) {
	t.Parallel()

	var closeCount atomic.Int64
	inner := &testutil.MockWriter{
		WriteFn: func(p []byte) (int, error) { return 0, errors.New("error") },
		CloseFn: func() error { closeCount.Add(1); return nil },
	}

	w := resilient.Wrap(inner)
	w.Write([]byte("x")) //nolint:errcheck — triggers failure and markFailed→Close

	// Explicit Close after degraded state should be a no-op (already closed).
	w.Close() //nolint:errcheck

	if closeCount.Load() != 1 {
		t.Errorf("inner.Close() called %d times, want exactly 1", closeCount.Load())
	}
}

func TestResilientWriter_SuccessfulWrite_ClosesDelegated(t *testing.T) {
	t.Parallel()

	var closed bool
	inner := &testutil.MockWriter{
		CloseFn: func() error { closed = true; return nil },
	}

	w := resilient.Wrap(inner)
	w.Write([]byte("ok")) //nolint:errcheck
	w.Close()             //nolint:errcheck

	if !closed {
		t.Error("inner.Close() not called on successful writer")
	}
}

func TestResilientWriter_ConcurrentWrites_NoDataRace(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	callN := 0
	inner := &testutil.MockWriter{
		WriteFn: func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			callN++
			if callN == 5 {
				return 0, errors.New("transient error")
			}
			return len(p), nil
		},
	}

	w := resilient.Wrap(inner)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Write([]byte("concurrent")) //nolint:errcheck
		}()
	}
	wg.Wait()
}

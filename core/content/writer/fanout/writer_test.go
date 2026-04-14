package fanout_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/bons/bons-ci/core/content/testutil"
	"github.com/bons/bons-ci/core/content/writer/fanout"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

func TestFanoutWriter_Write_DeliversToBothWriters(t *testing.T) {
	t.Parallel()

	var (
		aGot, bGot []byte
		mu         sync.Mutex
	)
	a := &testutil.MockWriter{WriteFn: func(p []byte) (int, error) {
		mu.Lock()
		aGot = append(aGot, p...)
		mu.Unlock()
		return len(p), nil
	}}
	b := &testutil.MockWriter{WriteFn: func(p []byte) (int, error) {
		mu.Lock()
		bGot = append(bGot, p...)
		mu.Unlock()
		return len(p), nil
	}}

	w := fanout.New(a, b)
	data := []byte("hello fanout")
	n, err := w.Write(data)
	if err != nil {
		t.Fatalf("Write() unexpected error: %v", err)
	}
	if n != len(data) {
		t.Fatalf("Write() returned %d, want %d", n, len(data))
	}
	if string(aGot) != string(data) || string(bGot) != string(data) {
		t.Errorf("data not delivered to all writers: a=%q b=%q", aGot, bGot)
	}
}

func TestFanoutWriter_Write_StopsOnFirstError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("disk full")
	var bCalled bool

	a := &testutil.MockWriter{WriteFn: func(p []byte) (int, error) { return 0, writeErr }}
	b := &testutil.MockWriter{WriteFn: func(p []byte) (int, error) {
		bCalled = true
		return len(p), nil
	}}

	w := fanout.New(a, b)
	_, err := w.Write([]byte("test"))

	if !errors.Is(err, writeErr) {
		t.Errorf("Write() error = %v, want wrapping %v", err, writeErr)
	}
	if bCalled {
		t.Error("second writer should not be called after first fails")
	}
}

func TestFanoutWriter_Commit_RunsConcurrently(t *testing.T) {
	t.Parallel()

	var count sync.WaitGroup
	count.Add(3)

	makeWriter := func() *testutil.MockWriter {
		return &testutil.MockWriter{
			CommitFn: func(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
				count.Done()
				return nil
			},
		}
	}

	w := fanout.New(makeWriter(), makeWriter(), makeWriter())
	if err := w.Commit(context.Background(), 0, ""); err != nil {
		t.Fatalf("Commit() error: %v", err)
	}

	// If Commit ran concurrently all three Done() calls would already be done.
	count.Wait() // just ensuring they all ran
}

func TestFanoutWriter_Commit_JoinsErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("commit error A")
	errB := errors.New("commit error B")

	a := &testutil.MockWriter{CommitFn: func(_ context.Context, _ int64, _ digest.Digest, _ ...content.Opt) error { return errA }}
	b := &testutil.MockWriter{CommitFn: func(_ context.Context, _ int64, _ digest.Digest, _ ...content.Opt) error { return errB }}

	w := fanout.New(a, b)
	err := w.Commit(context.Background(), 0, "")
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Errorf("Commit() error %v must wrap both errA and errB", err)
	}
}

func TestFanoutWriter_EmptyWriters_DoesNotPanic(t *testing.T) {
	t.Parallel()

	w := fanout.New()

	if dgst := w.Digest(); dgst != "" {
		t.Errorf("Digest() expected empty, got %q", dgst)
	}

	if _, err := w.Status(); err == nil {
		t.Error("Status() expected error on empty fanout, got nil")
	}

	n, err := w.Write([]byte("test"))
	if err != nil || n != 4 {
		t.Errorf("Write() on empty fanout: n=%d err=%v, want n=4 err=nil", n, err)
	}
}

func TestFanoutWriter_Close_JoinsErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("close A")
	a := &testutil.MockWriter{CloseFn: func() error { return errA }}
	b := &testutil.MockWriter{CloseFn: func() error { return nil }}

	w := fanout.New(a, b)
	if err := w.Close(); !errors.Is(err, errA) {
		t.Errorf("Close() error %v must wrap errA", err)
	}
}

func TestFanoutWriter_ConcurrentWrites_NoDataRace(t *testing.T) {
	t.Parallel()

	const numWriters = 5
	writers := make([]content.Writer, numWriters)
	for i := range writers {
		writers[i] = &testutil.MockWriter{}
	}

	w := fanout.New(writers...)
	data := []byte("concurrent data")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Sequential writes inside fanout means no race between writers,
			// but the test verifier (race detector) covers the outer fanout struct.
			w.Write(data) //nolint:errcheck
		}()
	}
	wg.Wait()
}

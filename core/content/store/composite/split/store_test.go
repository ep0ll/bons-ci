package split_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bons/bons-ci/core/content/store/composite/split"
	"github.com/bons/bons-ci/core/content/testutil"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestNew_RequiresAtLeastOneWriteStore(t *testing.T) {
	t.Parallel()

	_, err := split.New(&testutil.MockStore{})
	if err == nil {
		t.Fatal("New() expected error when no write stores provided")
	}
}

func TestMustNew_PanicsOnMisconfiguration(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("MustNew() expected panic, got none")
		}
	}()
	split.MustNew(&testutil.MockStore{}) // no write stores → should panic
}

// ── ReaderAt ──────────────────────────────────────────────────────────────────

func TestStore_ReaderAt_PrefersReadStore(t *testing.T) {
	t.Parallel()

	var readCalled bool
	readStore := &testutil.MockStore{
		ReaderAtFn: func(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
			readCalled = true
			return nil, nil
		},
	}
	writeStore := &testutil.MockStore{}

	s := split.MustNew(readStore, writeStore)
	s.ReaderAt(context.Background(), v1.Descriptor{}) //nolint:errcheck

	if !readCalled {
		t.Error("ReaderAt should prefer the read store")
	}
}

func TestStore_ReaderAt_FallsBackToWriteStore(t *testing.T) {
	t.Parallel()

	var writeCalled bool
	readStore := &testutil.MockStore{
		ReaderAtFn: func(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
			return nil, errors.New("not in read store")
		},
	}
	writeStore := &testutil.MockStore{
		ReaderAtFn: func(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
			writeCalled = true
			return nil, nil
		},
	}

	s := split.MustNew(readStore, writeStore)
	s.ReaderAt(context.Background(), v1.Descriptor{}) //nolint:errcheck

	if !writeCalled {
		t.Error("ReaderAt should fall back to write store when read store misses")
	}
}

// ── Writer ────────────────────────────────────────────────────────────────────

func TestStore_Writer_FansOutToAllWriteStores(t *testing.T) {
	t.Parallel()

	var aOpened, bOpened bool
	a := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			aOpened = true
			return &testutil.MockWriter{}, nil
		},
	}
	b := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			bOpened = true
			return &testutil.MockWriter{}, nil
		},
	}

	s := split.MustNew(&testutil.MockStore{}, a, b)
	w, err := s.Writer(context.Background())
	if err != nil {
		t.Fatalf("Writer() error: %v", err)
	}
	if w == nil {
		t.Fatal("Writer() returned nil")
	}
	w.Close() //nolint:errcheck

	if !aOpened || !bOpened {
		t.Errorf("Writer should open writers on all write stores: a=%v b=%v", aOpened, bOpened)
	}
}

func TestStore_Writer_AllFailReturnsError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("write store unavailable")
	a := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			return nil, writeErr
		},
	}

	s := split.MustNew(&testutil.MockStore{}, a)
	_, err := s.Writer(context.Background())
	if err == nil {
		t.Fatal("Writer() expected error when all write stores fail")
	}
}

// ── Walk ──────────────────────────────────────────────────────────────────────

func TestStore_Walk_UsesReadStore(t *testing.T) {
	t.Parallel()

	walked := content.Info{Digest: "sha256:from-read"}
	readStore := &testutil.MockStore{
		WalkFn: func(_ context.Context, fn content.WalkFunc, _ ...string) error {
			return fn(walked)
		},
	}
	writeStore := &testutil.MockStore{
		WalkFn: func(_ context.Context, fn content.WalkFunc, _ ...string) error {
			return fn(content.Info{Digest: "sha256:from-write"})
		},
	}

	s := split.MustNew(readStore, writeStore)
	var seen []digest.Digest
	s.Walk(context.Background(), func(info content.Info) error { //nolint:errcheck
		seen = append(seen, info.Digest)
		return nil
	})

	if len(seen) != 1 || seen[0] != "sha256:from-read" {
		t.Errorf("Walk should use read store exclusively, got: %v", seen)
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestStore_Delete_DeletesFromBothReadAndWriteStores(t *testing.T) {
	t.Parallel()

	var readDeleted, writeDeleted bool
	readStore := &testutil.MockStore{
		DeleteFn: func(_ context.Context, _ digest.Digest) error { readDeleted = true; return nil },
	}
	writeStore := &testutil.MockStore{
		DeleteFn: func(_ context.Context, _ digest.Digest) error { writeDeleted = true; return nil },
	}

	s := split.MustNew(readStore, writeStore)
	if err := s.Delete(context.Background(), "sha256:x"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if !readDeleted || !writeDeleted {
		t.Errorf("Delete: readDeleted=%v writeDeleted=%v, both must be true", readDeleted, writeDeleted)
	}
}

// ── Abort ─────────────────────────────────────────────────────────────────────

func TestStore_Abort_AttemptsAllWriteStores(t *testing.T) {
	t.Parallel()

	var aAborted, bAborted bool
	a := &testutil.MockStore{
		AbortFn: func(_ context.Context, _ string) error { aAborted = true; return nil },
	}
	b := &testutil.MockStore{
		AbortFn: func(_ context.Context, _ string) error { bAborted = true; return nil },
	}

	s := split.MustNew(&testutil.MockStore{}, a, b)
	s.Abort(context.Background(), "ref") //nolint:errcheck

	if !aAborted || !bAborted {
		t.Errorf("Abort should run on all write stores: a=%v b=%v", aAborted, bAborted)
	}
}

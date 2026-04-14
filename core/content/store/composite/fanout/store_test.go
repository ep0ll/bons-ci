package fanout_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/core/content/store/composite/fanout"
	"github.com/bons/bons-ci/core/content/testutil"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
)

// ── ListStatuses ──────────────────────────────────────────────────────────────

func TestStore_ListStatuses_DeduplicatesByRef(t *testing.T) {
	t.Parallel()

	now := time.Now()
	a := &testutil.MockStore{
		ListStatusesFn: func(_ context.Context, _ ...string) ([]content.Status, error) {
			return []content.Status{
				{Ref: "ref1", UpdatedAt: now},
				{Ref: "ref2", UpdatedAt: now},
			}, nil
		},
	}
	b := &testutil.MockStore{
		ListStatusesFn: func(_ context.Context, _ ...string) ([]content.Status, error) {
			return []content.Status{
				{Ref: "ref2", UpdatedAt: now}, // duplicate
				{Ref: "ref3", UpdatedAt: now},
			}, nil
		},
	}

	s := fanout.New(a, b)
	statuses, err := s.ListStatuses(context.Background())
	if err != nil {
		t.Fatalf("ListStatuses() error: %v", err)
	}

	refs := make(map[string]bool, len(statuses))
	for _, st := range statuses {
		refs[st.Ref] = true
	}

	for _, ref := range []string{"ref1", "ref2", "ref3"} {
		if !refs[ref] {
			t.Errorf("ref %q missing from results", ref)
		}
	}
	if len(statuses) != 3 {
		t.Errorf("got %d statuses, want 3", len(statuses))
	}
}

func TestStore_ListStatuses_AllStoresErrorReturnsError(t *testing.T) {
	t.Parallel()

	errA, errB := errors.New("err A"), errors.New("err B")
	a := &testutil.MockStore{
		ListStatusesFn: func(_ context.Context, _ ...string) ([]content.Status, error) { return nil, errA },
	}
	b := &testutil.MockStore{
		ListStatusesFn: func(_ context.Context, _ ...string) ([]content.Status, error) { return nil, errB },
	}

	s := fanout.New(a, b)
	_, err := s.ListStatuses(context.Background())
	if err == nil {
		t.Fatal("ListStatuses() expected error when all stores fail")
	}
}

// ── Walk ──────────────────────────────────────────────────────────────────────

func TestStore_Walk_DeduplicatesByDigest(t *testing.T) {
	t.Parallel()

	shared := content.Info{Digest: "sha256:shared"}
	onlyA := content.Info{Digest: "sha256:only-a"}
	onlyB := content.Info{Digest: "sha256:only-b"}

	a := &testutil.MockStore{
		WalkFn: func(_ context.Context, fn content.WalkFunc, _ ...string) error {
			_ = fn(shared)
			return fn(onlyA)
		},
	}
	b := &testutil.MockStore{
		WalkFn: func(_ context.Context, fn content.WalkFunc, _ ...string) error {
			_ = fn(shared) // duplicate
			return fn(onlyB)
		},
	}

	s := fanout.New(a, b)
	seen := make(map[digest.Digest]int)
	err := s.Walk(context.Background(), func(info content.Info) error {
		seen[info.Digest]++
		return nil
	})
	if err != nil {
		t.Fatalf("Walk() error: %v", err)
	}
	for dgst, count := range seen {
		if count != 1 {
			t.Errorf("digest %q seen %d times, want 1", dgst, count)
		}
	}
	if len(seen) != 3 {
		t.Errorf("got %d unique digests, want 3", len(seen))
	}
}

// ── Info ──────────────────────────────────────────────────────────────────────

func TestStore_Info_FirstSuccessWins(t *testing.T) {
	t.Parallel()

	wantInfo := content.Info{Digest: "sha256:hit"}
	a := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			return wantInfo, nil
		},
	}
	var bCalled bool
	b := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			bCalled = true
			return content.Info{}, nil
		},
	}

	s := fanout.New(a, b)
	info, err := s.Info(context.Background(), "sha256:hit")
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if info.Digest != wantInfo.Digest {
		t.Errorf("Info().Digest = %q, want %q", info.Digest, wantInfo.Digest)
	}
	if bCalled {
		t.Error("second store should not be queried when first succeeds")
	}
}

func TestStore_Info_ReturnsNotFoundWhenAllMiss(t *testing.T) {
	t.Parallel()

	a := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			return content.Info{}, errdefs.ErrNotFound
		},
	}

	s := fanout.New(a)
	_, err := s.Info(context.Background(), "sha256:missing")
	if !errdefs.IsNotFound(err) {
		t.Errorf("Info() error = %v, want not-found", err)
	}
}

// ── Writer ────────────────────────────────────────────────────────────────────

func TestStore_Writer_NoneAvailableReturnsError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("no writer")
	a := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			return nil, writeErr
		},
	}

	s := fanout.New(a)
	_, err := s.Writer(context.Background())
	if err == nil {
		t.Fatal("Writer() expected error when all stores fail")
	}
}

func TestStore_Writer_PartialSuccessReturnsFanoutWriter(t *testing.T) {
	t.Parallel()

	a := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			return nil, errors.New("a unavailable")
		},
	}
	b := &testutil.MockStore{} // succeeds with noop writer

	s := fanout.New(a, b)
	w, err := s.Writer(context.Background())
	if err != nil {
		t.Fatalf("Writer() unexpected error: %v", err)
	}
	if w == nil {
		t.Fatal("Writer() returned nil")
	}
	w.Close() //nolint:errcheck
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestStore_Delete_RunsOnAllStores(t *testing.T) {
	t.Parallel()

	var aDeleted, bDeleted atomic.Bool
	a := &testutil.MockStore{
		DeleteFn: func(_ context.Context, _ digest.Digest) error { aDeleted.Store(true); return nil },
	}
	b := &testutil.MockStore{
		DeleteFn: func(_ context.Context, _ digest.Digest) error { bDeleted.Store(true); return nil },
	}

	s := fanout.New(a, b)
	if err := s.Delete(context.Background(), "sha256:x"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if !aDeleted.Load() || !bDeleted.Load() {
		t.Error("Delete() must be called on all stores")
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestStore_ConcurrentOperations_NoDataRace(t *testing.T) {
	t.Parallel()

	s := fanout.New(
		&testutil.MockStore{},
		&testutil.MockStore{},
		&testutil.MockStore{},
	)
	ctx := context.Background()

	var wg atomic.Int64
	const n = 30
	wg.Store(n)
	done := make(chan struct{})

	for i := 0; i < n; i++ {
		go func() {
			s.Info(ctx, "sha256:x")                              //nolint:errcheck
			s.ListStatuses(ctx)                                  //nolint:errcheck
			s.Walk(ctx, func(content.Info) error { return nil }) //nolint:errcheck
			if wg.Add(-1) == 0 {
				close(done)
			}
		}()
	}
	<-done
}

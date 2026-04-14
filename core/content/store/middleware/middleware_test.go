package middleware_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/core/content/event"
	"github.com/bons/bons-ci/core/content/store/middleware"
	"github.com/bons/bons-ci/core/content/testutil"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// ── Chain ─────────────────────────────────────────────────────────────────────

func TestChain_NoMiddlewares_ReturnsBase(t *testing.T) {
	t.Parallel()

	base := &testutil.MockStore{}
	got := middleware.Chain(base)
	if got != base {
		t.Error("Chain() with no middlewares should return base unchanged")
	}
}

func TestChain_AppliesInOrder(t *testing.T) {
	t.Parallel()

	// Track the call order using a shared slice protected by a mutex.
	var mu sync.Mutex
	var order []string

	makeMiddleware := func(name string) middleware.Middleware {
		return func(next content.Store) content.Store {
			mu.Lock()
			order = append(order, name+"-wrap")
			mu.Unlock()
			return next
		}
	}

	base := &testutil.MockStore{}
	middleware.Chain(base, makeMiddleware("A"), makeMiddleware("B"), makeMiddleware("C"))

	// Chain applies in reverse so B wraps C(base), A wraps B(C(base)).
	// The wrap calls thus happen in reverse: C first, then B, then A.
	mu.Lock()
	defer mu.Unlock()
	want := []string{"C-wrap", "B-wrap", "A-wrap"}
	for i, got := range order {
		if got != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, got, want[i])
		}
	}
}

// ── ReadOnly ──────────────────────────────────────────────────────────────────

func TestReadOnly_BlocksMutations(t *testing.T) {
	t.Parallel()

	base := &testutil.MockStore{}
	s := middleware.Chain(base, middleware.ReadOnly())
	ctx := context.Background()

	t.Run("Writer", func(t *testing.T) {
		_, err := s.Writer(ctx)
		if !errors.Is(err, middleware.ErrReadOnly) {
			t.Errorf("Writer() error = %v, want ErrReadOnly", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		err := s.Delete(ctx, digest.Digest(""))
		if !errors.Is(err, middleware.ErrReadOnly) {
			t.Errorf("Delete() error = %v, want ErrReadOnly", err)
		}
	})

	t.Run("Update", func(t *testing.T) {
		_, err := s.Update(ctx, content.Info{})
		if !errors.Is(err, middleware.ErrReadOnly) {
			t.Errorf("Update() error = %v, want ErrReadOnly", err)
		}
	})

	t.Run("Abort", func(t *testing.T) {
		err := s.Abort(ctx, "ref")
		if !errors.Is(err, middleware.ErrReadOnly) {
			t.Errorf("Abort() error = %v, want ErrReadOnly", err)
		}
	})
}

func TestReadOnly_PassesThroughReads(t *testing.T) {
	t.Parallel()

	var infoCalled bool
	base := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			infoCalled = true
			return content.Info{}, nil
		},
	}

	s := middleware.Chain(base, middleware.ReadOnly())
	s.Info(context.Background(), digest.Digest("")) //nolint:errcheck

	if !infoCalled {
		t.Error("ReadOnly should pass Info() through to the base store")
	}
}

// ── Observable ────────────────────────────────────────────────────────────────

func TestObservable_EmitsReadHitEvent(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	events := make(chan event.Event, 1)
	bus.Subscribe(func(_ context.Context, e event.Event) { events <- e })

	base := &testutil.MockStore{
		ReaderAtFn: func(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
			return nil, nil
		},
	}
	s := middleware.Chain(base, middleware.Observable(bus, "test-store"))
	s.ReaderAt(ctx, v1.Descriptor{Digest: "sha256:abc"}) //nolint:errcheck

	select {
	case e := <-events:
		if e.Kind != event.KindReadHit {
			t.Errorf("event Kind = %q, want %q", e.Kind, event.KindReadHit)
		}
		if e.Source != "test-store" {
			t.Errorf("event Source = %q, want %q", e.Source, "test-store")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read hit event not received within timeout")
	}
}

func TestObservable_EmitsReadMissEvent(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	events := make(chan event.Event, 1)
	bus.Subscribe(func(_ context.Context, e event.Event) { events <- e })

	base := &testutil.MockStore{
		ReaderAtFn: func(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
			return nil, errors.New("not found")
		},
	}
	s := middleware.Chain(base, middleware.Observable(bus, "test-store"))
	s.ReaderAt(ctx, v1.Descriptor{}) //nolint:errcheck

	select {
	case e := <-events:
		if e.Kind != event.KindReadMiss {
			t.Errorf("event Kind = %q, want %q", e.Kind, event.KindReadMiss)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read miss event not received within timeout")
	}
}

func TestObservable_EmitsWriteStartedAndCommittedEvents(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	var received []event.Kind
	var mu sync.Mutex
	done := make(chan struct{})
	bus.Subscribe(func(_ context.Context, e event.Event) {
		mu.Lock()
		received = append(received, e.Kind)
		if len(received) == 2 {
			close(done)
		}
		mu.Unlock()
	})

	base := &testutil.MockStore{}
	s := middleware.Chain(base, middleware.Observable(bus, "obs-store"))

	w, err := s.Writer(ctx)
	if err != nil {
		t.Fatalf("Writer() error: %v", err)
	}
	w.Commit(ctx, 0, digest.Digest("")) //nolint:errcheck

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected 2 events (started + committed), timed out")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) < 2 {
		t.Fatalf("received %d events, want at least 2", len(received))
	}
	if received[0] != event.KindWriteStarted {
		t.Errorf("first event = %q, want %q", received[0], event.KindWriteStarted)
	}
	if received[1] != event.KindWriteCommitted {
		t.Errorf("second event = %q, want %q", received[1], event.KindWriteCommitted)
	}
}

func TestObservable_EmitsWriteFailedEvent_OnWriterError(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	events := make(chan event.Event, 1)
	bus.Subscribe(func(_ context.Context, e event.Event) { events <- e })

	base := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			return nil, errors.New("writer unavailable")
		},
	}
	s := middleware.Chain(base, middleware.Observable(bus, "obs-store"))
	s.Writer(ctx) //nolint:errcheck

	select {
	case e := <-events:
		if e.Kind != event.KindWriteFailed {
			t.Errorf("event Kind = %q, want %q", e.Kind, event.KindWriteFailed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write failed event not received")
	}
}

func TestObservable_EmitsDeletedEvent(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	ctx := context.Background()

	events := make(chan event.Event, 1)
	bus.Subscribe(func(_ context.Context, e event.Event) { events <- e })

	base := &testutil.MockStore{}
	s := middleware.Chain(base, middleware.Observable(bus, "obs-store"))
	s.Delete(ctx, "sha256:todelete") //nolint:errcheck

	select {
	case e := <-events:
		if e.Kind != event.KindDeleted {
			t.Errorf("event Kind = %q, want %q", e.Kind, event.KindDeleted)
		}
		if e.Digest != "sha256:todelete" {
			t.Errorf("event Digest = %q, want sha256:todelete", e.Digest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deleted event not received")
	}
}

// ── Composition ───────────────────────────────────────────────────────────────

func TestChain_ObservableAndReadOnly_Compose(t *testing.T) {
	t.Parallel()

	bus := event.NewBus()
	base := &testutil.MockStore{}

	s := middleware.Chain(
		base,
		middleware.Observable(bus, "composed"),
		middleware.ReadOnly(),
	)

	_, err := s.Writer(context.Background())
	if !errors.Is(err, middleware.ErrReadOnly) {
		t.Errorf("Composed store Writer() error = %v, want ErrReadOnly", err)
	}
}

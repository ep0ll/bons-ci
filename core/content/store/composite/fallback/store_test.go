package fallback_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bons/bons-ci/core/content/store/composite/fallback"
	"github.com/bons/bons-ci/core/content/testutil"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// ── Writer ────────────────────────────────────────────────────────────────────

func TestStore_Writer_PrimaryRequiredSecondaryBestEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		primaryErr   error
		secondaryErr error
		wantErr      bool
		wantErrIs    error
	}{
		{
			name:    "both succeed → fanout writer returned",
			wantErr: false,
		},
		{
			name:       "primary fails → error propagated",
			primaryErr: errors.New("primary unavailable"),
			wantErr:    true,
			wantErrIs:  errors.New("primary unavailable"),
		},
		{
			name:         "secondary fails → primary-only writer returned",
			secondaryErr: errors.New("secondary unavailable"),
			wantErr:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			primary := &testutil.MockStore{
				WriterFn: writerFnOrErr(tc.primaryErr),
			}
			secondary := &testutil.MockStore{
				WriterFn: writerFnOrErr(tc.secondaryErr),
			}

			store := fallback.New(primary, secondary)
			w, err := store.Writer(context.Background())

			if tc.wantErr {
				if err == nil {
					t.Fatal("Writer() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Writer() unexpected error: %v", err)
			}
			if w == nil {
				t.Fatal("Writer() returned nil writer")
			}
			w.Close() //nolint:errcheck
		})
	}
}

func TestStore_Writer_SecondaryFailureDoesNotAbortWrite(t *testing.T) {
	t.Parallel()

	var primaryWritten []byte
	primary := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			return &testutil.MockWriter{
				WriteFn: func(p []byte) (int, error) {
					primaryWritten = append(primaryWritten, p...)
					return len(p), nil
				},
			}, nil
		},
	}
	secondary := &testutil.MockStore{
		WriterFn: func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
			return &testutil.MockWriter{
				WriteFn: func(p []byte) (int, error) {
					return 0, errors.New("secondary write error")
				},
			}, nil
		},
	}

	store := fallback.New(primary, secondary)
	w, err := store.Writer(context.Background())
	if err != nil {
		t.Fatalf("Writer() error: %v", err)
	}

	data := []byte("important data")
	if _, err := w.Write(data); err != nil {
		t.Errorf("Write() returned error even though secondary should be absorbed: %v", err)
	}
	if string(primaryWritten) != string(data) {
		t.Errorf("primary received %q, want %q", primaryWritten, data)
	}
}

// ── Info ──────────────────────────────────────────────────────────────────────

func TestStore_Info_FallsBackOnNotFound(t *testing.T) {
	t.Parallel()

	wantInfo := content.Info{Digest: "sha256:abc"}

	primary := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			return content.Info{}, errdefs.ErrNotFound
		},
	}
	secondary := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			return wantInfo, nil
		},
	}

	store := fallback.New(primary, secondary)
	got, err := store.Info(context.Background(), "sha256:abc")
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if got.Digest != wantInfo.Digest {
		t.Errorf("Info() digest = %q, want %q", got.Digest, wantInfo.Digest)
	}
}

func TestStore_Info_PropagatesNonNotFoundErrors(t *testing.T) {
	t.Parallel()

	internalErr := errors.New("internal store error")
	var secondaryCalled bool

	primary := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			return content.Info{}, internalErr
		},
	}
	secondary := &testutil.MockStore{
		InfoFn: func(_ context.Context, _ digest.Digest) (content.Info, error) {
			secondaryCalled = true
			return content.Info{}, nil
		},
	}

	store := fallback.New(primary, secondary)
	_, err := store.Info(context.Background(), "sha256:abc")

	if !errors.Is(err, internalErr) {
		t.Errorf("Info() error = %v, want %v", err, internalErr)
	}
	if secondaryCalled {
		t.Error("secondary should not be queried on non-not-found primary error")
	}
}

// ── ReaderAt ──────────────────────────────────────────────────────────────────

func TestStore_ReaderAt_FallsBackOnNotFound(t *testing.T) {
	t.Parallel()

	primary := &testutil.MockStore{
		ReaderAtFn: func(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
			return nil, errdefs.ErrNotFound
		},
	}
	var secondaryCalled bool
	secondary := &testutil.MockStore{
		ReaderAtFn: func(_ context.Context, _ v1.Descriptor) (content.ReaderAt, error) {
			secondaryCalled = true
			return nil, nil
		},
	}

	store := fallback.New(primary, secondary)
	store.ReaderAt(context.Background(), v1.Descriptor{}) //nolint:errcheck

	if !secondaryCalled {
		t.Error("secondary ReaderAt should be called on primary not-found")
	}
}

// ── Abort ─────────────────────────────────────────────────────────────────────

func TestStore_Abort_AlwaysAttemptsSecondary(t *testing.T) {
	t.Parallel()

	primaryErr := errors.New("primary abort failed")
	var secondaryCalled bool

	primary := &testutil.MockStore{
		AbortFn: func(_ context.Context, _ string) error { return primaryErr },
	}
	secondary := &testutil.MockStore{
		AbortFn: func(_ context.Context, _ string) error {
			secondaryCalled = true
			return nil
		},
	}

	store := fallback.New(primary, secondary)
	err := store.Abort(context.Background(), "ref1")

	if !errors.Is(err, primaryErr) {
		t.Errorf("Abort() error = %v, want %v", err, primaryErr)
	}
	if !secondaryCalled {
		t.Error("secondary Abort should always be attempted")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writerFnOrErr(err error) func(context.Context, ...content.WriterOpt) (content.Writer, error) {
	return func(_ context.Context, _ ...content.WriterOpt) (content.Writer, error) {
		if err != nil {
			return nil, err
		}
		return &testutil.MockWriter{}, nil
	}
}

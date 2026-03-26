package content

import (
	"context"
	"errors"
	"testing"

	"github.com/bons/bons-ci/core/content/noop"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

type mockStore struct {
	content.Store
	writeErr   error
	mockWriter content.Writer
}

func (m *mockStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	if m.writeErr != nil {
		return nil, m.writeErr
	}
	if m.mockWriter != nil {
		return m.mockWriter, nil
	}
	return noop.NoopWriter(), nil
}

type mockFailingWriter struct {
	content.Writer
	writeErr  error
	commitErr error
}

func (m *mockFailingWriter) Write(p []byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return len(p), nil
}

func (m *mockFailingWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if m.commitErr != nil {
		return m.commitErr
	}
	return nil
}

func (m *mockFailingWriter) Close() error {
	return nil
}

func TestLocalFirstStoreWriter(t *testing.T) {
	ctx := context.Background()
	localErr := errors.New("local writer error")

	tests := []struct {
		name      string
		local     content.Store
		registry  content.Store
		wantErr   bool
		wantErrIs error
		wantNil   bool
	}{
		{
			name:     "both stores return writer",
			local:    &mockStore{},
			registry: &mockStore{},
			wantErr:  false,
			wantNil:  false,
		},
		{
			name:      "local fails",
			local:     &mockStore{writeErr: localErr},
			registry:  &mockStore{},
			wantErr:   true,
			wantErrIs: localErr,
			wantNil:   true,
		},
		{
			name:     "registry fails, local succeeds",
			local:    &mockStore{},
			registry: &mockStore{writeErr: errors.New("registry error")},
			wantErr:  false,
			wantNil:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewLocalFirstStore(tt.local, tt.registry)
			w, err := store.Writer(ctx)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Writer() expected error, got nil")
				} else if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Errorf("Writer() error = %v, wantErrIs = %v", err, tt.wantErrIs)
				}
			} else if err != nil {
				t.Errorf("Writer() unexpected error: %v", err)
			}

			if tt.wantNil && w != nil {
				t.Errorf("Writer() expected nil writer, got %v", w)
			}
			if !tt.wantNil && w == nil {
				t.Errorf("Writer() expected non-nil writer, got nil")
			}
		})
	}
}

func TestLocalFirstStoreWriterFailures(t *testing.T) {
	ctx := context.Background()

	// 1. Registry fails on Write
	t.Run("RegistryFailsOnWrite", func(t *testing.T) {
		regWriter := &mockFailingWriter{writeErr: errors.New("write failed")}
		store := NewLocalFirstStore(&mockStore{}, &mockStore{mockWriter: regWriter})

		w, err := store.Writer(ctx)
		if err != nil {
			t.Fatalf("Writer() failed: %v", err)
		}

		n, err := w.Write([]byte("test"))
		if err != nil {
			t.Errorf("Write() returned error, but shouldn't have: %v", err)
		}
		if n != 4 {
			t.Errorf("Write() returned %d bytes, want 4", n)
		}

		err = w.Commit(ctx, 4, "")
		if err != nil {
			t.Errorf("Commit() returned error: %v", err)
		}
	})

	// 2. Registry fails on Commit
	t.Run("RegistryFailsOnCommit", func(t *testing.T) {
		regWriter := &mockFailingWriter{commitErr: errors.New("commit failed")}
		store := NewLocalFirstStore(&mockStore{}, &mockStore{mockWriter: regWriter})

		w, err := store.Writer(ctx)
		if err != nil {
			t.Fatalf("Writer() failed: %v", err)
		}

		_, err = w.Write([]byte("test"))
		if err != nil {
			t.Errorf("Write() returned error: %v", err)
		}

		err = w.Commit(ctx, 4, "")
		if err != nil {
			t.Errorf("Commit() returned error, but shouldn't have: %v", err)
		}
	})
}

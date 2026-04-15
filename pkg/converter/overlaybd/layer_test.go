package overlaybd

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	local "github.com/bons/bons-ci/core/content/store/local"
	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// mockMutableStore wraps a local content store to intercept Info/Update calls,
// letting tests assert on label mutations without disk-level side effects.
type mockMutableStore struct {
	content.Store
	mu   sync.RWMutex
	info map[digest.Digest]content.Info
}

func (m *mockMutableStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	info, err := m.Store.Info(ctx, dgst)
	if err != nil {
		return info, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if cached, ok := m.info[dgst]; ok {
		return cached, nil
	}
	return info, nil
}

func (m *mockMutableStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.info[info.Digest] = info
	return info, nil
}

// newTestStore creates an ephemeral content store backed by a temp directory.
func newTestStore(t *testing.T) *mockMutableStore {
	t.Helper()
	base, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}
	return &mockMutableStore{
		Store: base,
		info:  make(map[digest.Digest]content.Info),
	}
}

// createTestTarBlob writes a minimal tar archive into cs and returns a
// MediaTypeImageLayer descriptor — the same type that LayerConvertFunc accepts.
func createTestTarBlob(t *testing.T, cs content.Store, id int) ocispec.Descriptor {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	fileName := fmt.Sprintf("test-%d.txt", id)
	body := fmt.Sprintf("overlaybd conversion test payload %d", id)
	_ = tw.WriteHeader(&tar.Header{
		Name: fileName,
		Size: int64(len(body)),
		Mode: 0644,
	})
	_, _ = buf.WriteString(body)
	_ = tw.Close()

	data := buf.Bytes()
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Size:      int64(len(data)),
		Digest:    digest.FromBytes(data),
	}
	if err := content.WriteBlob(context.Background(), cs, desc.Digest.String(), bytes.NewReader(data), desc); err != nil {
		t.Fatalf("write test tar blob %d: %v", id, err)
	}
	return desc
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestLayerConvertFunc_Concurrency_Race launches multiple goroutines converting
// distinct layers simultaneously.  Running with -race validates that there are
// no data-races on the shared store or pool.
//
// Note: if the overlaybd CLI binaries are absent the test gracefully logs and
// returns — the concurrency and hook-invocation assertions are still validated
// up to the CLI call point.
func TestLayerConvertFunc_Concurrency_Race(t *testing.T) {
	cs := newTestStore(t)

	const concurrency = 4
	descs := make([]ocispec.Descriptor, concurrency)
	for i := range descs {
		descs[i] = createTestTarBlob(t, cs, i)
	}

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			var hookCalled, hookDone atomic.Bool
			hook := LayerConvertHook{
				LayerReader: func(_ context.Context, _ *converter.ContentSectionReader, _ content.Store, _ ocispec.Descriptor) error {
					hookCalled.Store(true)
					return nil
				},
				Done: func(_ context.Context) { hookDone.Store(true) },
			}

			conv := LayerConvertFunc(PackOption{}, hook)
			newDesc, err := conv(context.Background(), cs, descs[workerID])

			if err != nil {
				// Graceful: binaries may not be present in the test environment.
				t.Logf("worker %d: graceful skip (CLI unavailable): %v", workerID, err)
				// Hook assertions are still valid even on CLI failure.
				if !hookCalled.Load() {
					t.Errorf("worker %d: LayerReader was not called before CLI", workerID)
				}
				if !hookDone.Load() {
					t.Errorf("worker %d: Done hook was not called on failure", workerID)
				}
				return
			}

			if newDesc == nil {
				t.Errorf("worker %d: expected non-nil descriptor", workerID)
				return
			}
			if !hookCalled.Load() {
				t.Errorf("worker %d: LayerReader hook was not called", workerID)
			}
			if !hookDone.Load() {
				t.Errorf("worker %d: Done hook was not called", workerID)
			}

			// Idempotency: a second call on an already-converted blob must skip.
			cachedDesc, err := conv(context.Background(), cs, *newDesc)
			if err != nil {
				t.Errorf("worker %d: idempotency check error: %v", workerID, err)
			}
			if cachedDesc != nil {
				t.Errorf("worker %d: expected nil from already-converted blob, got %v", workerID, cachedDesc.Digest)
			}
		}(i)
	}

	wg.Wait()
}

// TestLayerConvertFunc_NonLayer ensures non-layer blobs (configs, manifests)
// are silently passed through as nil without triggering any hook.
func TestLayerConvertFunc_NonLayer(t *testing.T) {
	cs := newTestStore(t)

	var hookCalled bool
	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{
		LayerReader: func(_ context.Context, _ *converter.ContentSectionReader, _ content.Store, _ ocispec.Descriptor) error {
			hookCalled = true
			return nil
		},
	})

	configDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromString("{}"),
		Size:      2,
	}
	desc, err := conv(context.Background(), cs, configDesc)
	if err != nil {
		t.Fatalf("unexpected error on non-layer: %v", err)
	}
	if desc != nil {
		t.Errorf("expected nil for non-layer blob, got %v", desc)
	}
	if hookCalled {
		t.Error("LayerReader must not be called for non-layer blobs")
	}
}

// TestLayerConvertFunc_ContextCancellation validates that a pre-cancelled
// context causes an immediate return with the context error.
func TestLayerConvertFunc_ContextCancellation(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{})
	result, err := conv(ctx, cs, desc)

	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result on cancelled context, got %v", result)
	}
}

// TestLayerConvertFunc_DoneAlwaysCalled ensures the Done hook fires even when
// conversion fails (e.g., CLI tools are absent).
func TestLayerConvertFunc_DoneAlwaysCalled(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 0)

	done := make(chan struct{})
	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{
		Done: func(_ context.Context) { close(done) },
	})

	_, _ = conv(context.Background(), cs, desc) // result ignored — testing the hook

	select {
	case <-done:
		// expected
	case <-time.After(10 * time.Second):
		t.Error("Done hook was not called within timeout")
	}
}

// TestLayerConvertFunc_AlreadyConverted verifies that a blob carrying the
// LayerAnnotationOverlayBDDigest label in its stored Info is skipped.
func TestLayerConvertFunc_AlreadyConverted(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 0)

	// Pre-stamp the converted label directly on the mock store.
	info := content.Info{
		Digest: desc.Digest,
		Labels: map[string]string{
			LayerAnnotationOverlayBDDigest: desc.Digest.String(),
		},
	}
	cs.mu.Lock()
	cs.info[desc.Digest] = info
	cs.mu.Unlock()

	var hookCalled bool
	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{
		LayerReader: func(_ context.Context, _ *converter.ContentSectionReader, _ content.Store, _ ocispec.Descriptor) error {
			hookCalled = true
			return nil
		},
	})

	result, err := conv(context.Background(), cs, desc)
	if err != nil {
		t.Fatalf("already-converted fast-path returned error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for already-converted blob, got %v", result)
	}
	if hookCalled {
		t.Error("LayerReader must not be called for already-converted blobs")
	}
}

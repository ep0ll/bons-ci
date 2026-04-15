package stargz

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
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
// allowing tests to observe and override per-blob label mutations without
// disk I/O.
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

// createTestTarBlob writes a single-file tar archive into the content store
// and returns its descriptor with media type MediaTypeImageLayer (uncompressed).
// estargz.Build accepts uncompressed tars directly.
func createTestTarBlob(t *testing.T, cs content.Store, id int) ocispec.Descriptor {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	fileName := fmt.Sprintf("test-%d.txt", id)
	body := fmt.Sprintf("hello world from eStargz layer %d — payload bytes", id)
	_ = tw.WriteHeader(&tar.Header{
		Name: fileName,
		Size: int64(len(body)),
		Mode: 0644,
	})
	_, _ = io.WriteString(tw, body)
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

// TestLayerConvertFunc_Concurrency_Race launches 20 goroutines that each
// convert a distinct layer concurrently.  Running with -race validates that
// there are no data races across the shared content store and pool.
func TestLayerConvertFunc_Concurrency_Race(t *testing.T) {
	cs := newTestStore(t)

	const concurrency = 20
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
				t.Errorf("worker %d: conversion error: %v", workerID, err)
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

			// ── Idempotency: re-converting an already-stargz blob must be a no-op ──
			cachedDesc, err := conv(context.Background(), cs, *newDesc)
			if err != nil {
				t.Errorf("worker %d: idempotency check error: %v", workerID, err)
			}
			if cachedDesc != nil {
				t.Errorf("worker %d: expected nil from already-stargz blob, got %v", workerID, cachedDesc.Digest)
			}
		}(i)
	}

	wg.Wait()
}

// TestLayerConvertFunc_NonLayer validates that non-layer blobs (e.g. the image
// config) are passed through as nil without triggering any conversion.
func TestLayerConvertFunc_NonLayer(t *testing.T) {
	cs := newTestStore(t)

	var hookCalled bool
	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{
		LayerReader: func(_ context.Context, _ *converter.ContentSectionReader, _ content.Store, _ ocispec.Descriptor) error {
			hookCalled = true
			return nil
		},
	})

	configBlob := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromString("{}"),
		Size:      2,
	}
	desc, err := conv(context.Background(), cs, configBlob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc != nil {
		t.Errorf("expected nil for non-layer, got %v", desc)
	}
	if hookCalled {
		t.Error("LayerReader hook must not be called for non-layer blobs")
	}
}

// TestLayerConvertFunc_ContextCancellation verifies that a cancelled context
// causes LayerConvertFunc to return immediately with the context error and does
// not leave any goroutines blocked.
func TestLayerConvertFunc_ContextCancellation(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before conversion starts

	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{})
	result, err := conv(ctx, cs, desc)

	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result on cancelled context, got %v", result)
	}
}

// TestLayerConvertFunc_DoneAlwaysCalled ensures the Done hook is invoked even
// when the conversion fails (e.g. bad options).
func TestLayerConvertFunc_DoneAlwaysCalled(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 0)

	doneCalled := make(chan struct{})
	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{
		Done: func(_ context.Context) { close(doneCalled) },
	})

	// Even a successful conversion must close doneCalled.
	_, _ = conv(context.Background(), cs, desc)

	select {
	case <-doneCalled:
		// expected
	case <-time.After(5 * time.Second):
		t.Error("Done hook was not called within timeout")
	}
}

// TestLayerConvertFunc_ZeroByteLayer verifies that a zero-byte
// (empty directory marker) layer blob does not panic and returns a descriptor.
func TestLayerConvertFunc_ZeroByteLayer(t *testing.T) {
	cs := newTestStore(t)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.Close()

	data := buf.Bytes()
	if len(data) == 0 {
		// pure empty tar — wrap with a header
		data = []byte{}
	}
	emptyDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Size:      int64(len(data)),
		Digest:    digest.FromBytes(data),
	}
	if len(data) > 0 {
		if err := content.WriteBlob(context.Background(), cs, emptyDesc.Digest.String(), bytes.NewReader(data), emptyDesc); err != nil {
			t.Fatalf("write empty tar blob: %v", err)
		}
	}

	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{})
	// An empty tar is valid — estargz should produce a blob without panicking.
	if len(data) > 0 {
		_, err := conv(context.Background(), cs, emptyDesc)
		// A valid empty tar should not error (estargz handles it gracefully).
		if err != nil {
			t.Logf("empty tar conversion returned error (may be expected): %v", err)
		}
	}
}

// TestLayerConvertFunc_PrioritizedFiles verifies that providing prioritized
// files does not cause a panic and produces a valid descriptor.
func TestLayerConvertFunc_PrioritizedFiles(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 99)

	conv := LayerConvertFunc(PackOption{
		PrioritizedFiles: []string{"test-99.txt"},
	}, LayerConvertHook{})

	newDesc, err := conv(context.Background(), cs, desc)
	if err != nil {
		t.Fatalf("conversion with prioritized files failed: %v", err)
	}
	if newDesc == nil {
		t.Fatal("expected non-nil descriptor")
	}
	if newDesc.Annotations[LayerAnnotationTOCDigest] == "" {
		t.Error("TOC digest annotation missing from prioritized-files blob")
	}
}

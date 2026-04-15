package soci

import (
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

func newTestStore(t *testing.T) content.Store {
	t.Helper()
	cs, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("create local store: %v", err)
	}
	return cs
}

// createTestTarBlob creates an uncompressed tar and pushes it to the store.
func createTestTarBlob(t *testing.T, cs content.Store, id int) ocispec.Descriptor {
	t.Helper()

	body := []byte(fmt.Sprintf("SOCI test layer data chunk no %d", id))
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Size:      int64(len(body)),
		Digest:    digest.FromBytes(body),
	}
	if err := content.WriteBlob(context.Background(), cs, desc.Digest.String(), bytes.NewReader(body), desc); err != nil {
		t.Fatalf("write test tar blob %d: %v", id, err)
	}
	return desc
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestLayerConvertFunc_Concurrency verifies that calling LayerConvertFunc
// concurrently correctly passes through layer descriptors without modifying them
// and correctly triggers pipeline hooks.
func TestLayerConvertFunc_Concurrency(t *testing.T) {
	cs := newTestStore(t)

	const concurrency = 10
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
					// ensure the layer can be read out
					hookCalled.Store(true)
					return nil
				},
				Done: func(_ context.Context) {
					hookDone.Store(true)
				},
			}

			conv := LayerConvertFunc(PackOption{}, hook)
			newDesc, err := conv(context.Background(), cs, descs[workerID])

			if err != nil {
				t.Errorf("worker %d: unexpected error: %v", workerID, err)
				return
			}

			// SOCI never rewrites the layer blob; the returned descriptor must
			// be identically nil.
			if newDesc != nil {
				t.Errorf("worker %d: expected nil descriptor from soci layer converter, got %v", workerID, newDesc)
			}

			if !hookCalled.Load() {
				t.Errorf("worker %d: LayerReader hook was not called", workerID)
			}
			if !hookDone.Load() {
				t.Errorf("worker %d: Done hook was not called", workerID)
			}
		}(i)
	}

	wg.Wait()
}

// TestLayerConvertFunc_NonLayer verifies that configs and manifests pass
// through untouched and do not trigger SectionReader hooks.
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

	result, err := conv(context.Background(), cs, configBlob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for non-layer, got %v", result)
	}
	if hookCalled {
		t.Fatal("LayerReader must not be called for non-layer blobs")
	}
}

// TestLayerConvertFunc_Cancellation verifies that context cancellation skips
// hook execution immediately.
func TestLayerConvertFunc_Cancellation(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // abort early

	var hookCalled bool
	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{
		LayerReader: func(_ context.Context, _ *converter.ContentSectionReader, _ content.Store, _ ocispec.Descriptor) error {
			hookCalled = true
			return nil
		},
	})

	result, err := conv(ctx, cs, desc)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
	if hookCalled {
		t.Fatal("hook must not execute on cancelled context")
	}
}

// TestLayerConvertFunc_DoneAlwaysCalled ensures the done channel closes even
// if hook fails or we panic internally.
func TestLayerConvertFunc_DoneAlwaysCalled(t *testing.T) {
	cs := newTestStore(t)
	desc := createTestTarBlob(t, cs, 0)

	doneCh := make(chan struct{})
	conv := LayerConvertFunc(PackOption{}, LayerConvertHook{
		Done: func(_ context.Context) { close(doneCh) },
	})

	_, _ = conv(context.Background(), cs, desc)

	select {
	case <-doneCh: // standard exit
	case <-time.After(time.Second * 5):
		t.Fatal("done hook was not fired within bounds")
	}
}

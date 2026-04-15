package stargz

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	local "github.com/bons/bons-ci/core/content/store/local"
	"github.com/bons/bons-ci/core/images/converter"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// createTestTarBlob creates a valid mock tar layer for tests
func createTestTarBlob(t *testing.T, cs content.Store, id int) ocispec.Descriptor {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	fileName := fmt.Sprintf("test-%d.txt", id)
	body := fmt.Sprintf("hello world from layer %d", id)

	tw.WriteHeader(&tar.Header{
		Name: fileName,
		Size: int64(len(body)),
		Mode: 0644,
	})
	tw.Write([]byte(body))
	tw.Close()

	data := buf.Bytes()
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Size:      int64(len(data)),
		Digest:    digest.FromBytes(data),
	}

	err := content.WriteBlob(context.Background(), cs, desc.Digest.String(), bytes.NewReader(data), desc)
	if err != nil {
		t.Fatal(err)
	}

	return desc
}

// mockMutableStore wraps the local store to support metadata updates
type mockMutableStore struct {
	content.Store
	mu   sync.Mutex
	info map[digest.Digest]content.Info
}

func (m *mockMutableStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	info, err := m.Store.Info(ctx, dgst)
	if err != nil {
		return info, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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

// TestLayerConvertFunc_Concurrency_Race validates decoupling, race conditions, and deadlocks.
func TestLayerConvertFunc_Concurrency_Race(t *testing.T) {
	tmp := t.TempDir()
	baseCs, err := local.NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	cs := &mockMutableStore{
		Store: baseCs,
		info:  make(map[digest.Digest]content.Info),
	}
	concurrency := 20
	descs := make([]ocispec.Descriptor, concurrency)
	for i := 0; i < concurrency; i++ {
		descs[i] = createTestTarBlob(t, cs, i)
	}

	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			hookCalled := false
			hookDone := false

			hook := LayerConvertHook{
				LayerReader: func(ctx context.Context, sr *converter.ContentSectionReader, c content.Store, d ocispec.Descriptor) error {
					hookCalled = true
					return nil
				},
				Done: func(ctx context.Context) {
					hookDone = true
				},
			}

			conv := LayerConvertFunc(PackOption{}, hook)

			// Process distinct descriptors fully decoupled and concurrently using waitgroups
			newDesc, err := conv(context.Background(), cs, descs[workerID])
			if err != nil {
				t.Errorf("worker %d failed during layer conversion: %v", workerID, err)
				return
			}

			if newDesc == nil {
				t.Errorf("worker %d expected descriptor, got nil", workerID)
				return
			}

			if !hookCalled {
				t.Errorf("worker %d layer reader hook was not executed", workerID)
			}

			if !hookDone {
				t.Errorf("worker %d defer done hook was not executed", workerID)
			}

			// Re-run the same descriptor to verify memoization / fast paths
			cachedDesc, err := conv(context.Background(), cs, *newDesc)
			if err != nil {
				t.Errorf("worker %d failed during cache verification: %v", workerID, err)
			}

			// Expecting nil indicating skip due to already being mapped + stargz blob
			if cachedDesc != nil {
				t.Errorf("worker %d expected cached/skipped desc to return nil, got %v", workerID, cachedDesc.Digest)
			}

		}(i)
	}

	wg.Wait()
}

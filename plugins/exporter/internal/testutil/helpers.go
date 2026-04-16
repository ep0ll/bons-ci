// Package testutil provides shared test helpers.
package testutil

import (
	"context"
	"fmt"
	"sync"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// ─── MemoryContentStore ────────────────────────────────────────────────────

// MemoryContentStore is a thread-safe, in-memory ContentStore for tests.
type MemoryContentStore struct {
	mu    sync.RWMutex
	blobs map[string]blobEntry
}

type blobEntry struct {
	data      []byte
	mediaType string
}

// NewMemoryContentStore creates an empty store.
func NewMemoryContentStore() *MemoryContentStore {
	return &MemoryContentStore{blobs: make(map[string]blobEntry)}
}

func (m *MemoryContentStore) WriteBlob(_ context.Context, data []byte, mediaType string) (core.Digest, error) {
	dgst := core.NewDigest(data)
	m.mu.Lock()
	m.blobs[dgst.String()] = blobEntry{data: data, mediaType: mediaType}
	m.mu.Unlock()
	return dgst, nil
}

func (m *MemoryContentStore) Has(_ context.Context, d core.Digest) (bool, error) {
	m.mu.RLock()
	_, ok := m.blobs[d.String()]
	m.mu.RUnlock()
	return ok, nil
}

func (m *MemoryContentStore) ReadBlob(_ context.Context, d core.Digest) ([]byte, error) {
	m.mu.RLock()
	entry, ok := m.blobs[d.String()]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", core.ErrContentNotFound, d)
	}
	return entry.data, nil
}

// Count returns the number of stored blobs (for assertions).
func (m *MemoryContentStore) Count() int {
	m.mu.RLock()
	n := len(m.blobs)
	m.mu.RUnlock()
	return n
}

// ─── SpyPusher ─────────────────────────────────────────────────────────────

// SpyPusher records push calls without performing real network I/O.
type SpyPusher struct {
	mu    sync.Mutex
	calls []PushCall
	err   error // if set, returned on every Push call
}

// PushCall records one invocation of SpyPusher.Push.
type PushCall struct {
	Ref    string
	Digest core.Digest
}

// NewSpyPusher creates a SpyPusher that returns nil errors by default.
func NewSpyPusher() *SpyPusher { return &SpyPusher{} }

// FailWith causes all subsequent Push calls to return err.
func (s *SpyPusher) FailWith(err error) { s.mu.Lock(); s.err = err; s.mu.Unlock() }

func (s *SpyPusher) Push(_ context.Context, ref string, dgst core.Digest, _ core.ContentStore) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, PushCall{Ref: ref, Digest: dgst})
	return s.err
}

// Calls returns a snapshot of recorded push calls.
func (s *SpyPusher) Calls() []PushCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]PushCall, len(s.calls))
	copy(cp, s.calls)
	return cp
}

// ─── SpyImageStorer ────────────────────────────────────────────────────────

// SpyImageStorer records Store/Unpack calls.
type SpyImageStorer struct {
	mu          sync.Mutex
	StoreCalls  []StoreCall
	UnpackCalls []string
	storeErr    error
}

// StoreCall records one invocation of SpyImageStorer.Store.
type StoreCall struct {
	Name string
	Desc core.BlobDescriptor
}

// NewSpyImageStorer creates a SpyImageStorer.
func NewSpyImageStorer() *SpyImageStorer { return &SpyImageStorer{} }

// FailStorWith causes all subsequent Store calls to return err.
func (s *SpyImageStorer) FailStoreWith(err error) {
	s.mu.Lock()
	s.storeErr = err
	s.mu.Unlock()
}

func (s *SpyImageStorer) Store(_ context.Context, name string, desc core.BlobDescriptor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StoreCalls = append(s.StoreCalls, StoreCall{Name: name, Desc: desc})
	return s.storeErr
}

func (s *SpyImageStorer) Unpack(_ context.Context, name string) error {
	s.mu.Lock()
	s.UnpackCalls = append(s.UnpackCalls, name)
	s.mu.Unlock()
	return nil
}

// ─── Artifact builders ─────────────────────────────────────────────────────

// MinimalArtifact creates the smallest valid Artifact for testing.
func MinimalArtifact() *core.Artifact {
	layerData := []byte("fake-layer-data")
	dgst := core.NewDigest(layerData)
	return &core.Artifact{
		Kind:      core.ArtifactKindContainerImage,
		Platforms: []core.Platform{{OS: "linux", Architecture: "amd64"}},
		Layers: []core.Layer{
			{
				Descriptor: core.BlobDescriptor{
					Digest:    dgst,
					Size:      int64(len(layerData)),
					MediaType: core.MediaTypeOCILayer,
				},
				DiffID: dgst,
				History: &core.LayerHistory{
					CreatedBy: "test",
				},
			},
		},
		Config:   []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers"}}`),
		Metadata: make(map[string][]byte),
	}
}

// MultiPlatformArtifact creates a two-platform Artifact for testing indexes.
func MultiPlatformArtifact() *core.Artifact {
	a := MinimalArtifact()
	a.Platforms = []core.Platform{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "arm64", Variant: "v8"},
	}
	return a
}

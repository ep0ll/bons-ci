package layer

import (
	"context"
	"fmt"
	"sync"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// Store manages the registry of known layers and their file modification manifests.
type Store interface {
	Register(ctx context.Context, id core.LayerID, parentID core.LayerID) error
	MarkModified(id core.LayerID, path string) error
	IsModified(id core.LayerID, path string) bool
	OwnerOf(chain []core.LayerID, path string) (core.LayerID, bool)
	ModifiedPaths(id core.LayerID) []string
	Parent(id core.LayerID) (core.LayerID, error)
	Exists(id core.LayerID) bool
}

type manifest struct {
	parentID core.LayerID
	modified map[string]struct{}
	mu       sync.RWMutex
}

type memStore struct {
	mu     sync.RWMutex
	layers map[string]*manifest
}

// NewMemoryStore creates a new in-memory layer store.
func NewMemoryStore() Store {
	return &memStore{
		layers: make(map[string]*manifest),
	}
}

func (s *memStore) Register(_ context.Context, id, parentID core.LayerID) error {
	if id.IsZero() {
		return fmt.Errorf("%w: layer ID must not be empty", core.ErrInvalidEvent)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := id.String()
	if _, exists := s.layers[key]; exists {
		return fmt.Errorf("%w: %s", core.ErrLayerExists, key)
	}

	if !parentID.IsZero() {
		if _, ok := s.layers[parentID.String()]; !ok {
			return fmt.Errorf("%w: parent %s", core.ErrLayerNotFound, parentID)
		}
	}

	s.layers[key] = &manifest{
		parentID: parentID,
		modified: make(map[string]struct{}),
	}
	return nil
}

func (s *memStore) MarkModified(id core.LayerID, path string) error {
	m, err := s.getManifest(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.modified[path] = struct{}{}
	m.mu.Unlock()
	return nil
}

func (s *memStore) IsModified(id core.LayerID, path string) bool {
	m, err := s.getManifest(id)
	if err != nil {
		return false
	}
	m.mu.RLock()
	_, ok := m.modified[path]
	m.mu.RUnlock()
	return ok
}

func (s *memStore) OwnerOf(chain []core.LayerID, path string) (core.LayerID, bool) {
	for i := len(chain) - 1; i >= 0; i-- {
		if s.IsModified(chain[i], path) {
			return chain[i], true
		}
	}
	if len(chain) > 0 {
		return chain[0], false
	}
	return core.LayerID{}, false
}

func (s *memStore) ModifiedPaths(id core.LayerID) []string {
	m, err := s.getManifest(id)
	if err != nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	paths := make([]string, 0, len(m.modified))
	for p := range m.modified {
		paths = append(paths, p)
	}
	return paths
}

func (s *memStore) Parent(id core.LayerID) (core.LayerID, error) {
	m, err := s.getManifest(id)
	if err != nil {
		return core.LayerID{}, err
	}
	return m.parentID, nil
}

func (s *memStore) Exists(id core.LayerID) bool {
	s.mu.RLock()
	_, ok := s.layers[id.String()]
	s.mu.RUnlock()
	return ok
}

func (s *memStore) getManifest(id core.LayerID) (*manifest, error) {
	s.mu.RLock()
	m, ok := s.layers[id.String()]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", core.ErrLayerNotFound, id)
	}
	return m, nil
}

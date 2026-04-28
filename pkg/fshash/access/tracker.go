package access

import (
	"sync"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// Tracker maintains per-layer manifests of accessed files and their hashes.
// Thread-safe for concurrent use.
type Tracker struct {
	mu     sync.RWMutex
	layers map[string]*layerManifest
}

type layerManifest struct {
	mu    sync.Mutex
	files map[string]core.FileHash
}

// NewTracker creates a new access tracker.
func NewTracker() *Tracker {
	return &Tracker{layers: make(map[string]*layerManifest)}
}

// Record stores a file hash for a given layer. Latest hash wins on collision.
func (t *Tracker) Record(layerID core.LayerID, hash core.FileHash) {
	m := t.getOrCreateManifest(layerID)
	m.mu.Lock()
	m.files[hash.Path] = hash
	m.mu.Unlock()
}

// FileHashes returns all recorded file hashes for a layer.
func (t *Tracker) FileHashes(layerID core.LayerID) []core.FileHash {
	t.mu.RLock()
	m, ok := t.layers[layerID.String()]
	t.mu.RUnlock()
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	hashes := make([]core.FileHash, 0, len(m.files))
	for _, h := range m.files {
		hashes = append(hashes, h)
	}
	return hashes
}

// Count returns the number of unique files tracked for a layer.
func (t *Tracker) Count(layerID core.LayerID) int {
	t.mu.RLock()
	m, ok := t.layers[layerID.String()]
	t.mu.RUnlock()
	if !ok {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.files)
}

// HasFile reports whether a file has been tracked for the given layer.
func (t *Tracker) HasFile(layerID core.LayerID, path string) bool {
	t.mu.RLock()
	m, ok := t.layers[layerID.String()]
	t.mu.RUnlock()
	if !ok {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.files[path]
	return exists
}

// Clear removes all tracked data for a layer.
func (t *Tracker) Clear(layerID core.LayerID) {
	t.mu.Lock()
	delete(t.layers, layerID.String())
	t.mu.Unlock()
}

// ClearAll resets the tracker entirely.
func (t *Tracker) ClearAll() {
	t.mu.Lock()
	t.layers = make(map[string]*layerManifest)
	t.mu.Unlock()
}

func (t *Tracker) getOrCreateManifest(layerID core.LayerID) *layerManifest {
	key := layerID.String()
	t.mu.RLock()
	m, ok := t.layers[key]
	t.mu.RUnlock()
	if ok {
		return m
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok = t.layers[key]
	if ok {
		return m
	}
	m = &layerManifest{files: make(map[string]core.FileHash)}
	t.layers[key] = m
	return m
}

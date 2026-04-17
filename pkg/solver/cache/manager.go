package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// Manager implements CacheManager-style operations on top of KeyStorage
// and ResultStorage. It provides dependency-aware cache key queries,
// result loading, and saving with persistent key management.
//
// This is equivalent to BuildKit's cacheManager in cachemanager.go.
type Manager struct {
	mu      sync.RWMutex
	id      string
	keys    KeyStorage
	results ResultStorage
}

// NewManager creates a new cache manager.
func NewManager(id string, keys KeyStorage, results ResultStorage) *Manager {
	return &Manager{
		id:      id,
		keys:    keys,
		results: results,
	}
}

// NewInMemoryManager creates a Manager backed entirely by memory.
func NewInMemoryManager() *Manager {
	return NewManager(
		"inmemory",
		NewInMemoryKeyStorage(),
		NewInMemoryResultStorage(),
	)
}

// ID returns the cache manager identifier.
func (m *Manager) ID() string { return m.id }

// Query looks up cache keys given dependency inputs and an operation digest.
// For root keys (no deps), returns the key if it exists.
// For non-root keys, walks the link graph from dep keys through the op digest.
//
// This is the core of cache matching and matches BuildKit's cacheManager.Query.
func (m *Manager) Query(
	deps []CacheKeyWithSelector,
	input Index,
	dgst digest.Digest,
	output Index,
) ([]*CacheKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(deps) == 0 {
		// Root key lookup.
		id := rootKeyID(dgst, output)
		if m.keys.Exists(id) {
			return []*CacheKey{m.newRootKey(dgst, output)}, nil
		}
		return nil, nil
	}

	// Non-root: walk links from each dep key.
	link := CacheInfoLink{
		Input:  input,
		Digest: dgst,
		Output: output,
	}

	var results []*CacheKey
	seen := map[string]struct{}{}

	for _, dep := range deps {
		depID := dep.ID
		if dep.Selector != "" {
			link.Selector = digest.Digest(dep.Selector)
		}

		if err := m.keys.WalkLinks(depID, link, func(targetID string) error {
			if _, ok := seen[targetID]; ok {
				return nil
			}
			seen[targetID] = struct{}{}
			ck := m.newKeyWithID(targetID, dgst, output)
			results = append(results, ck)
			return nil
		}); err != nil {
			return nil, errors.Wrap(err, "walk links")
		}
	}

	return results, nil
}

// Records returns all loadable cache records for a given key.
func (m *Manager) Records(ctx context.Context, ck *CacheKey) ([]*CacheRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var records []*CacheRecord
	if err := m.keys.WalkResults(ck.id, func(res CacheResult) error {
		if !m.results.Exists(ctx, res.ID) {
			return nil
		}
		records = append(records, &CacheRecord{
			ID:        res.ID,
			CreatedAt: res.CreatedAt,
			KeyID:     ck.id,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return records, nil
}

// Load loads a result from a cache record.
func (m *Manager) Load(ctx context.Context, rec *CacheRecord) (any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cr, err := m.keys.Load(rec.KeyID, rec.ID)
	if err != nil {
		return nil, errors.Wrap(err, "load cache result")
	}
	return m.results.Load(ctx, cr)
}

// Save persists a result under the given cache key and creates all
// necessary links in the key storage.
func (m *Manager) Save(ck *CacheKey, resultID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := ck.id
	if id == "" {
		id = rootKeyID(ck.dgst, ck.output)
	}

	// Ensure persistent key.
	if err := m.ensurePersistentKey(id, ck); err != nil {
		return err
	}

	// Add result.
	cr := CacheResult{
		ID:        resultID,
		CreatedAt: time.Now(),
	}
	if _, err := m.results.Save(resultID, cr.CreatedAt); err != nil {
		return errors.Wrap(err, "save result")
	}
	return m.keys.AddResult(id, cr)
}

// ReleaseUnreferenced removes results that are no longer valid.
func (m *Manager) ReleaseUnreferenced(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var toRelease []string
	if err := m.keys.Walk(func(id string) error {
		return m.keys.WalkResults(id, func(res CacheResult) error {
			if !m.results.Exists(ctx, res.ID) {
				toRelease = append(toRelease, res.ID)
			}
			return nil
		})
	}); err != nil {
		return err
	}

	for _, id := range toRelease {
		if err := m.keys.Release(id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ensurePersistentKey(id string, ck *CacheKey) error {
	if m.keys.Exists(id) {
		return nil
	}
	// Create links from deps to this key.
	for i, depSlice := range ck.deps {
		for _, dep := range depSlice {
			link := CacheInfoLink{
				Input:    Index(i),
				Digest:   ck.dgst,
				Output:   ck.output,
				Selector: digest.Digest(dep.Selector),
			}
			if err := m.keys.AddLink(dep.ID, link, id); err != nil {
				return errors.Wrap(err, "add link")
			}
		}
	}
	return nil
}

func (m *Manager) newRootKey(dgst digest.Digest, output Index) *CacheKey {
	return &CacheKey{
		id:     rootKeyID(dgst, output),
		dgst:   dgst,
		output: output,
	}
}

func (m *Manager) newKeyWithID(id string, dgst digest.Digest, output Index) *CacheKey {
	return &CacheKey{
		id:     id,
		dgst:   dgst,
		output: output,
	}
}

func rootKeyID(dgst digest.Digest, output Index) string {
	return fmt.Sprintf("%s@%d", dgst, output)
}

// ─── Cache key for the Manager ──────────────────────────────────────────────

// CacheKey is the manager-level cache key with link metadata.
type CacheKey struct {
	id     string
	dgst   digest.Digest
	output Index
	deps   [][]CacheKeyWithSelector
}

// ID returns the stable identifier.
func (ck *CacheKey) ID() string { return ck.id }

// CacheKeyWithSelector pairs a key ID with a selector for link matching.
type CacheKeyWithSelector struct {
	ID       string
	Selector string
}

// CacheRecord is a loadable cache entry within the Manager.
type CacheRecord struct {
	ID        string
	CreatedAt time.Time
	KeyID     string
}

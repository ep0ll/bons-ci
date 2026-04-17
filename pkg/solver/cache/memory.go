package cache

import (
	"context"
	"sync"
	"time"
)

// ─── In-Memory Key Storage ──────────────────────────────────────────────────

// inMemoryKey holds per-key state: associated results, forward links, and backlinks.
type inMemoryKey struct {
	id        string
	results   map[string]CacheResult
	links     map[CacheInfoLink]map[string]struct{}
	backlinks map[string]struct{}
}

func newInMemoryKey(id string) *inMemoryKey {
	return &inMemoryKey{
		id:        id,
		results:   map[string]CacheResult{},
		links:     map[CacheInfoLink]map[string]struct{}{},
		backlinks: map[string]struct{}{},
	}
}

// InMemoryKeyStorage implements KeyStorage with an in-memory link/backlink graph.
// This matches BuildKit's memorycachestorage.go.
type InMemoryKeyStorage struct {
	mu       sync.RWMutex
	byID     map[string]*inMemoryKey
	byResult map[string]map[string]struct{} // resultID → set of key IDs
}

// NewInMemoryKeyStorage creates a new in-memory key storage.
func NewInMemoryKeyStorage() *InMemoryKeyStorage {
	return &InMemoryKeyStorage{
		byID:     map[string]*inMemoryKey{},
		byResult: map[string]map[string]struct{}{},
	}
}

func (s *InMemoryKeyStorage) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if k, ok := s.byID[id]; ok {
		return len(k.links) > 0 || len(k.results) > 0
	}
	return false
}

func (s *InMemoryKeyStorage) Walk(fn func(string) error) error {
	s.mu.RLock()
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	for _, id := range ids {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *InMemoryKeyStorage) WalkResults(id string, fn func(CacheResult) error) error {
	s.mu.RLock()
	k, ok := s.byID[id]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	results := make([]CacheResult, 0, len(k.results))
	for _, res := range k.results {
		results = append(results, res)
	}
	s.mu.RUnlock()
	for _, res := range results {
		if err := fn(res); err != nil {
			return err
		}
	}
	return nil
}

func (s *InMemoryKeyStorage) Load(id string, resultID string) (CacheResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return CacheResult{}, ErrNotFound
	}
	r, ok := k.results[resultID]
	if !ok {
		return CacheResult{}, ErrNotFound
	}
	return r, nil
}

func (s *InMemoryKeyStorage) AddResult(id string, res CacheResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		k = newInMemoryKey(id)
		s.byID[id] = k
	}
	k.results[res.ID] = res
	m, ok := s.byResult[res.ID]
	if !ok {
		m = map[string]struct{}{}
		s.byResult[res.ID] = m
	}
	m[id] = struct{}{}
	return nil
}

func (s *InMemoryKeyStorage) WalkIDsByResult(resultID string, fn func(string) error) error {
	s.mu.Lock()
	ids := map[string]struct{}{}
	for id := range s.byResult[resultID] {
		ids[id] = struct{}{}
	}
	s.mu.Unlock()
	for id := range ids {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *InMemoryKeyStorage) Release(resultID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, ok := s.byResult[resultID]
	if !ok {
		return nil
	}
	for id := range ids {
		k, ok := s.byID[id]
		if !ok {
			continue
		}
		delete(k.results, resultID)
		delete(s.byResult[resultID], id)
		if len(s.byResult[resultID]) == 0 {
			delete(s.byResult, resultID)
		}
		s.emptyBranchWithParents(k)
	}
	return nil
}

// emptyBranchWithParents cascades cleanup: if a key has no results and no
// forward links, remove it and clean up its parents. This matches BuildKit's
// emptyBranchWithParents.
func (s *InMemoryKeyStorage) emptyBranchWithParents(k *inMemoryKey) {
	if len(k.results) != 0 || len(k.links) != 0 {
		return
	}
	for id := range k.backlinks {
		p, ok := s.byID[id]
		if !ok {
			continue
		}
		for l := range p.links {
			delete(p.links[l], k.id)
			if len(p.links[l]) == 0 {
				delete(p.links, l)
			}
		}
		s.emptyBranchWithParents(p)
	}
	delete(s.byID, k.id)
}

func (s *InMemoryKeyStorage) AddLink(id string, link CacheInfoLink, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.byID[id]
	if !ok {
		k = newInMemoryKey(id)
		s.byID[id] = k
	}
	k2, ok := s.byID[target]
	if !ok {
		k2 = newInMemoryKey(target)
		s.byID[target] = k2
	}
	m, ok := k.links[link]
	if !ok {
		m = map[string]struct{}{}
		k.links[link] = m
	}
	k2.backlinks[id] = struct{}{}
	m[target] = struct{}{}
	return nil
}

func (s *InMemoryKeyStorage) WalkLinks(id string, link CacheInfoLink, fn func(id string) error) error {
	s.mu.RLock()
	k, ok := s.byID[id]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	var links []string
	for target := range k.links[link] {
		links = append(links, target)
	}
	s.mu.RUnlock()
	for _, t := range links {
		if err := fn(t); err != nil {
			return err
		}
	}
	return nil
}

func (s *InMemoryKeyStorage) HasLink(id string, link CacheInfoLink, target string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if k, ok := s.byID[id]; ok {
		if v, ok := k.links[link]; ok {
			_, ok := v[target]
			return ok
		}
	}
	return false
}

func (s *InMemoryKeyStorage) WalkBacklinks(id string, fn func(id string, link CacheInfoLink) error) error {
	s.mu.RLock()
	k, ok := s.byID[id]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	type backlink struct {
		id   string
		link CacheInfoLink
	}
	var out []backlink
	for bid := range k.backlinks {
		b, ok := s.byID[bid]
		if !ok {
			continue
		}
		for l, m := range b.links {
			if _, ok := m[id]; !ok {
				continue
			}
			out = append(out, backlink{id: bid, link: l})
		}
	}
	s.mu.RUnlock()
	for _, bl := range out {
		if err := fn(bl.id, bl.link); err != nil {
			return err
		}
	}
	return nil
}

// ─── In-Memory Result Storage ───────────────────────────────────────────────

// InMemoryResultStorage implements ResultStorage with an in-memory map.
type InMemoryResultStorage struct {
	mu sync.RWMutex
	m  map[string]any
}

// NewInMemoryResultStorage creates a new in-memory result storage.
func NewInMemoryResultStorage() *InMemoryResultStorage {
	return &InMemoryResultStorage{m: map[string]any{}}
}

func (s *InMemoryResultStorage) Save(id string, createdAt time.Time) (CacheResult, error) {
	s.mu.Lock()
	s.m[id] = struct{}{} // placeholder
	s.mu.Unlock()
	return CacheResult{ID: id, CreatedAt: createdAt}, nil
}

func (s *InMemoryResultStorage) Load(_ context.Context, res CacheResult) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.m[res.ID]; !ok {
		return nil, ErrNotFound
	}
	return res.ID, nil
}

func (s *InMemoryResultStorage) Exists(_ context.Context, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.m[id]
	return ok
}

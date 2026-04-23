// Package metadata provides the in-memory MetadataStore implementation.
//
// Secondary indices allow O(1) lookup by:
//   - (repo, digest) primary key
//   - AccelType
//   - Source digest (for finding all accel images of a given original)
//   - Repository name
//
// All indices are kept consistent under a single RWMutex.
// For production, replace the backing maps with a persistent KV store
// (BadgerDB, LevelDB, or a SQL database) that implements the same interface.
package metadata

import (
	"context"
	"fmt"
	"sync"

	digest "github.com/opencontainers/go-digest"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Store
// ────────────────────────────────────────────────────────────────────────────

// Store implements types.MetadataStore with in-memory multi-index storage.
type Store struct {
	mu sync.RWMutex

	// Primary index: repoDigestKey → ImageMetadata
	primary map[repoDigestKey]types.ImageMetadata

	// Secondary indices (values are sets of primary keys)
	byAccelType    map[types.AccelType]map[repoDigestKey]struct{}
	bySourceDigest map[digest.Digest]map[repoDigestKey]struct{}
	byRepo         map[string]map[repoDigestKey]struct{}
}

type repoDigestKey struct {
	repo   string
	digest digest.Digest
}

// New returns a ready-to-use metadata Store.
func New() *Store {
	return &Store{
		primary:        make(map[repoDigestKey]types.ImageMetadata),
		byAccelType:    make(map[types.AccelType]map[repoDigestKey]struct{}),
		bySourceDigest: make(map[digest.Digest]map[repoDigestKey]struct{}),
		byRepo:         make(map[string]map[repoDigestKey]struct{}),
	}
}

// ── MetadataStore interface ────────────────────────────────────────────────

// Put upserts metadata for an image.
func (s *Store) Put(_ context.Context, meta types.ImageMetadata) error {
	if meta.Repository == "" {
		return fmt.Errorf("metadata: repository is required")
	}
	if meta.Digest == "" {
		return fmt.Errorf("metadata: digest is required")
	}

	key := repoDigestKey{repo: meta.Repository, digest: meta.Digest}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove old secondary index entries if replacing an existing entry.
	if old, exists := s.primary[key]; exists {
		s.removeFromSecondary(key, old)
	}

	s.primary[key] = meta
	s.addToSecondary(key, meta)
	return nil
}

// Get returns metadata for a specific (repo, digest) pair.
func (s *Store) Get(_ context.Context, repo string, dgst digest.Digest) (*types.ImageMetadata, error) {
	key := repoDigestKey{repo: repo, digest: dgst}
	s.mu.RLock()
	meta, ok := s.primary[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("metadata: not found: %s@%s", repo, dgst)
	}
	cp := meta
	return &cp, nil
}

// Delete removes metadata for a (repo, digest) pair.
func (s *Store) Delete(_ context.Context, repo string, dgst digest.Digest) error {
	key := repoDigestKey{repo: repo, digest: dgst}
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta, ok := s.primary[key]; ok {
		s.removeFromSecondary(key, meta)
		delete(s.primary, key)
	}
	return nil
}

// ListByAccelType returns all metadata entries for a given AccelType.
func (s *Store) ListByAccelType(_ context.Context, t types.AccelType) ([]types.ImageMetadata, error) {
	s.mu.RLock()
	keys := s.byAccelType[t]
	result := make([]types.ImageMetadata, 0, len(keys))
	for k := range keys {
		if m, ok := s.primary[k]; ok {
			result = append(result, m)
		}
	}
	s.mu.RUnlock()
	return result, nil
}

// ListBySourceDigest returns all metadata for images derived from sourceDigest.
func (s *Store) ListBySourceDigest(_ context.Context, sourceDigest digest.Digest) ([]types.ImageMetadata, error) {
	s.mu.RLock()
	keys := s.bySourceDigest[sourceDigest]
	result := make([]types.ImageMetadata, 0, len(keys))
	for k := range keys {
		if m, ok := s.primary[k]; ok {
			result = append(result, m)
		}
	}
	s.mu.RUnlock()
	return result, nil
}

// ListByRepo returns all metadata for a given repository.
func (s *Store) ListByRepo(_ context.Context, repo string) ([]types.ImageMetadata, error) {
	s.mu.RLock()
	keys := s.byRepo[repo]
	result := make([]types.ImageMetadata, 0, len(keys))
	for k := range keys {
		if m, ok := s.primary[k]; ok {
			result = append(result, m)
		}
	}
	s.mu.RUnlock()
	return result, nil
}

// ── Secondary index maintenance ────────────────────────────────────────────

// addToSecondary updates all secondary indices for key/meta.
// Must be called with s.mu held for writing.
func (s *Store) addToSecondary(key repoDigestKey, meta types.ImageMetadata) {
	// By AccelType
	if meta.AccelType != "" && meta.AccelType != types.AccelUnknown {
		if s.byAccelType[meta.AccelType] == nil {
			s.byAccelType[meta.AccelType] = make(map[repoDigestKey]struct{})
		}
		s.byAccelType[meta.AccelType][key] = struct{}{}
	}

	// By source digest
	if meta.SourceDigest != "" {
		if s.bySourceDigest[meta.SourceDigest] == nil {
			s.bySourceDigest[meta.SourceDigest] = make(map[repoDigestKey]struct{})
		}
		s.bySourceDigest[meta.SourceDigest][key] = struct{}{}
	}

	// By repository
	if s.byRepo[meta.Repository] == nil {
		s.byRepo[meta.Repository] = make(map[repoDigestKey]struct{})
	}
	s.byRepo[meta.Repository][key] = struct{}{}
}

// removeFromSecondary removes key from all secondary indices.
// Must be called with s.mu held for writing.
func (s *Store) removeFromSecondary(key repoDigestKey, meta types.ImageMetadata) {
	if m := s.byAccelType[meta.AccelType]; m != nil {
		delete(m, key)
	}
	if m := s.bySourceDigest[meta.SourceDigest]; m != nil {
		delete(m, key)
	}
	if m := s.byRepo[meta.Repository]; m != nil {
		delete(m, key)
	}
}

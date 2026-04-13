// Package mountcache implements BuildKit-style persistent mount caches with
// sharing modes (shared, private, locked), platform isolation, and
// content-addressed blob storage for the cached filesystem data.
package mountcache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
	"github.com/google/uuid"
)

// Key scheme:
//   mountcache/<id>                     → JSON MountCache
//   mountcache/idx/<scope>/<user_key>   → id (lookup by user key)
//   mountcache/idx/<scope>/<user_key>/<os>/<arch> → id (platform-specific lookup)

const (
	keyMountCache   = "mountcache/%s"
	keyMountIdx     = "mountcache/idx/%s/%s"
	keyMountIdxPlat = "mountcache/idx/%s/%s/%s/%s"
)

// Service manages BuildKit-style mount caches.
type Service struct {
	meta  metadata.Store
	store storage.Store
}

// New creates a mount cache Service.
func New(meta metadata.Store, store storage.Store) *Service {
	return &Service{meta: meta, store: store}
}

// Create allocates a new mount cache.
func (s *Service) Create(ctx context.Context, userKey, scope string, platformSpecific bool, platform *models.Platform, sharing models.CacheSharingMode, labels map[string]string) (*models.MountCache, error) {
	// Check if one already exists.
	if existing := s.lookup(ctx, scope, userKey, platform, platformSpecific); existing != nil {
		return existing, nil
	}

	cache := &models.MountCache{
		ID:               uuid.New().String(),
		UserKey:          userKey,
		Scope:            scope,
		PlatformSpecific: platformSpecific,
		Platform:         platform,
		Sharing:          sharing,
		Labels:           labels,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
		AccessedAt:       time.Now(),
	}
	if err := s.put(ctx, cache); err != nil {
		return nil, err
	}
	s.updateIndex(ctx, cache)
	return cache, nil
}

// Get retrieves a mount cache by ID or by (userKey + scope + platform).
func (s *Service) Get(ctx context.Context, id, userKey, scope string, platform *models.Platform) (*models.MountCache, error) {
	if id != "" {
		return s.getByID(ctx, id)
	}
	if userKey != "" {
		c := s.lookup(ctx, scope, userKey, platform, platform != nil)
		if c == nil {
			return nil, errors.ErrNotFound
		}
		return c, nil
	}
	return nil, errors.ErrInvalidArgument
}

// List returns all mount caches for a scope/platform.
func (s *Service) List(ctx context.Context, scope string, platform *models.Platform, limit int) ([]*models.MountCache, error) {
	prefix := []byte(fmt.Sprintf("mountcache/idx/%s/", scope))
	pairs, err := s.meta.ScanPrefix(ctx, prefix, 0)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var caches []*models.MountCache
	for _, p := range pairs {
		id := string(p.Value)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		c, err := s.getByID(ctx, id)
		if err != nil {
			continue
		}
		if platform != nil && c.PlatformSpecific && c.Platform != nil {
			if c.Platform.OS != platform.OS || c.Platform.Arch != platform.Arch {
				continue
			}
		}
		caches = append(caches, c)
		if limit > 0 && len(caches) >= limit {
			break
		}
	}
	return caches, nil
}

// Delete removes a mount cache and its data.
func (s *Service) Delete(ctx context.Context, id string) error {
	c, err := s.getByID(ctx, id)
	if err != nil {
		return err
	}
	if c.BlobDigest != "" {
		_ = s.store.Delete(ctx, c.BlobDigest)
	}
	s.removeIndex(ctx, c)
	return s.meta.Delete(ctx, []byte(fmt.Sprintf(keyMountCache, id)))
}

// Lock acquires an exclusive lock on a mount cache.
// Returns (true, cache) if successful, (false, cache) if already locked.
func (s *Service) Lock(ctx context.Context, id, owner string, ttl time.Duration) (bool, *models.MountCache, error) {
	var acquired bool
	var result *models.MountCache

	err := s.meta.Txn(ctx, func(txn metadata.Txn) error {
		data, err := txn.Get([]byte(fmt.Sprintf(keyMountCache, id)))
		if err != nil {
			return err
		}
		var c models.MountCache
		if err := json.Unmarshal(data, &c); err != nil {
			return err
		}
		if c.Locked && c.LockExpiresAt != nil && time.Now().Before(*c.LockExpiresAt) {
			result = &c
			acquired = false
			return nil
		}
		c.Locked = true
		c.LockOwner = owner
		exp := time.Now().Add(ttl)
		c.LockExpiresAt = &exp
		c.UpdatedAt = time.Now()
		raw, _ := json.Marshal(c)
		result = &c
		acquired = true
		return txn.Put([]byte(fmt.Sprintf(keyMountCache, id)), raw)
	})
	return acquired, result, err
}

// Unlock releases the lock on a mount cache.
func (s *Service) Unlock(ctx context.Context, id, owner string) error {
	return s.meta.Txn(ctx, func(txn metadata.Txn) error {
		data, err := txn.Get([]byte(fmt.Sprintf(keyMountCache, id)))
		if err != nil {
			return err
		}
		var c models.MountCache
		if err := json.Unmarshal(data, &c); err != nil {
			return err
		}
		if c.LockOwner != owner {
			return errors.ErrForbidden
		}
		c.Locked = false
		c.LockOwner = ""
		c.LockExpiresAt = nil
		c.UpdatedAt = time.Now()
		raw, _ := json.Marshal(c)
		return txn.Put([]byte(fmt.Sprintf(keyMountCache, id)), raw)
	})
}

// Upload streams data into a mount cache, replacing any existing blob.
func (s *Service) Upload(ctx context.Context, cacheID string, r io.Reader, size int64) (string, error) {
	c, err := s.getByID(ctx, cacheID)
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("mountcache:%s", cacheID) // stable key per cache
	if err := s.store.Put(ctx, digest, r, size, storage.PutOptions{Overwrite: true}); err != nil {
		return "", err
	}
	info, _ := s.store.Stat(ctx, digest)
	if info != nil {
		c.SizeBytes = info.Size
	}
	c.BlobDigest = digest
	c.UpdatedAt = time.Now()
	c.AccessedAt = time.Now()
	return digest, s.put(ctx, c)
}

// Download streams data from a mount cache.
func (s *Service) Download(ctx context.Context, cacheID string, offset, length int64) (io.ReadCloser, int64, error) {
	c, err := s.getByID(ctx, cacheID)
	if err != nil {
		return nil, 0, err
	}
	if c.BlobDigest == "" {
		return nil, 0, errors.ErrNotFound
	}
	c.AccessedAt = time.Now()
	_ = s.put(ctx, c)
	return s.store.Get(ctx, c.BlobDigest, storage.GetOptions{Offset: offset, Length: length})
}

// Prune removes stale caches to free storage.
func (s *Service) Prune(ctx context.Context, scope string, all bool, olderThan time.Duration, keepBytes int64) (int64, int64, error) {
	caches, err := s.List(ctx, scope, nil, 0)
	if err != nil {
		return 0, 0, err
	}

	type entry struct {
		c    *models.MountCache
		size int64
	}
	var candidates []entry
	for _, c := range caches {
		if !all && olderThan > 0 && time.Since(c.AccessedAt) < olderThan {
			continue
		}
		candidates = append(candidates, entry{c, c.SizeBytes})
	}

	// Sort oldest access first.
	sortByAccess(candidates)

	var pruned, freed int64
	var totalKept int64
	for _, e := range candidates {
		if keepBytes > 0 && totalKept+e.size <= keepBytes {
			totalKept += e.size
			continue
		}
		if err := s.Delete(ctx, e.c.ID); err == nil {
			pruned++
			freed += e.size
		}
	}
	return pruned, freed, nil
}

// ── private helpers ───────────────────────────────────────────────────────────

func (s *Service) getByID(ctx context.Context, id string) (*models.MountCache, error) {
	data, err := s.meta.Get(ctx, []byte(fmt.Sprintf(keyMountCache, id)))
	if err != nil {
		if err == metadata.ErrKeyNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, err
	}
	var c models.MountCache
	return &c, json.Unmarshal(data, &c)
}

func (s *Service) put(ctx context.Context, c *models.MountCache) error {
	data, _ := json.Marshal(c)
	return s.meta.Put(ctx, []byte(fmt.Sprintf(keyMountCache, c.ID)), data)
}

func (s *Service) lookup(ctx context.Context, scope, userKey string, platform *models.Platform, platformSpecific bool) *models.MountCache {
	var key string
	if platformSpecific && platform != nil {
		key = fmt.Sprintf(keyMountIdxPlat, scope, userKey, platform.OS, platform.Arch)
	} else {
		key = fmt.Sprintf(keyMountIdx, scope, userKey)
	}
	id, err := s.meta.Get(ctx, []byte(key))
	if err != nil {
		return nil
	}
	c, _ := s.getByID(ctx, string(id))
	return c
}

func (s *Service) updateIndex(ctx context.Context, c *models.MountCache) {
	key := fmt.Sprintf(keyMountIdx, c.Scope, c.UserKey)
	_ = s.meta.Put(ctx, []byte(key), []byte(c.ID))
	if c.PlatformSpecific && c.Platform != nil {
		platKey := fmt.Sprintf(keyMountIdxPlat, c.Scope, c.UserKey, c.Platform.OS, c.Platform.Arch)
		_ = s.meta.Put(ctx, []byte(platKey), []byte(c.ID))
	}
}

func (s *Service) removeIndex(ctx context.Context, c *models.MountCache) {
	_ = s.meta.Delete(ctx, []byte(fmt.Sprintf(keyMountIdx, c.Scope, c.UserKey)))
	if c.PlatformSpecific && c.Platform != nil {
		_ = s.meta.Delete(ctx, []byte(fmt.Sprintf(keyMountIdxPlat, c.Scope, c.UserKey, c.Platform.OS, c.Platform.Arch)))
	}
}

func sortByAccess(entries []struct {
	c    *models.MountCache
	size int64
}) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].c.AccessedAt.Before(entries[j-1].c.AccessedAt); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// unused import guards
var _ = strings.TrimSpace
var _ = io.EOF

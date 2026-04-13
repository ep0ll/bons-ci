package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/google/uuid"
)

// Key scheme:
//   cache/entry/<cache_key>          → JSON CacheEntry (primary lookup)
//   cache/vertex/<dag_id>/<vtx_id>   → list of cache_keys (reverse index)
//   cache/dag/<dag_id>               → list of cache_keys

const (
	keyCacheEntry    = "cache/entry/%s"
	keyCacheByVertex = "cache/vertex/%s/%s"
	keyCacheByDAG    = "cache/dag/%s"
)

// CacheService manages vertex result caching.
type CacheService struct {
	meta metadata.Store
}

// NewCacheService creates a CacheService.
func NewCacheService(meta metadata.Store) *CacheService {
	return &CacheService{meta: meta}
}

// CheckCache looks up a cache entry by content-addressed key.
func (c *CacheService) CheckCache(ctx context.Context, cacheKey string, withFiles bool) (bool, *models.CacheEntry, error) {
	data, err := c.meta.Get(ctx, []byte(fmt.Sprintf(keyCacheEntry, cacheKey)))
	if err != nil {
		if err == metadata.ErrKeyNotFound {
			return false, nil, nil
		}
		return false, nil, err
	}
	var entry models.CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return false, nil, err
	}
	// Update LRU timestamp.
	entry.HitCount++
	entry.LastUsedAt = time.Now()
	_ = c.storeEntry(ctx, &entry)

	if !withFiles {
		entry.OutputFiles = nil
	}
	return true, &entry, nil
}

// StoreCache persists a cache entry.
func (c *CacheService) StoreCache(ctx context.Context, req StoreCacheRequest) (*models.CacheEntry, error) {
	entry := &models.CacheEntry{
		ID:            uuid.New().String(),
		CacheKey:      req.CacheKey,
		VertexID:      req.VertexID,
		DAGID:         req.DAGID,
		OutputDigests: req.OutputDigests,
		OutputFiles:   req.OutputFiles,
		Kind:          req.Kind,
		Platform:      req.Platform,
		InlineData:    req.InlineData,
		Metadata:      req.Metadata,
		HitCount:      0,
		CreatedAt:     time.Now(),
		LastUsedAt:    time.Now(),
	}
	if req.TTL > 0 {
		exp := time.Now().Add(req.TTL)
		entry.ExpiresAt = &exp
	}
	if err := c.storeEntry(ctx, entry); err != nil {
		return nil, err
	}
	// Update reverse indexes.
	c.appendToVertexIndex(ctx, req.DAGID, req.VertexID, req.CacheKey) //nolint:errcheck
	c.appendToDAGIndex(ctx, req.DAGID, req.CacheKey)                  //nolint:errcheck
	return entry, nil
}

// GetCacheEntry fetches a specific entry by key.
func (c *CacheService) GetCacheEntry(ctx context.Context, cacheKey string) (*models.CacheEntry, error) {
	data, err := c.meta.Get(ctx, []byte(fmt.Sprintf(keyCacheEntry, cacheKey)))
	if err != nil {
		if err == metadata.ErrKeyNotFound {
			return nil, errors.ErrCacheMiss
		}
		return nil, err
	}
	var entry models.CacheEntry
	return &entry, json.Unmarshal(data, &entry)
}

// ListCacheEntries returns all entries for a vertex or DAG.
func (c *CacheService) ListCacheEntries(ctx context.Context, dagID, vertexID string, platform *models.Platform, limit int) ([]*models.CacheEntry, error) {
	var keys []string
	if vertexID != "" {
		raw, err := c.meta.Get(ctx, []byte(fmt.Sprintf(keyCacheByVertex, dagID, vertexID)))
		if err == nil {
			keys = strings.Fields(string(raw))
		}
	} else if dagID != "" {
		raw, err := c.meta.Get(ctx, []byte(fmt.Sprintf(keyCacheByDAG, dagID)))
		if err == nil {
			keys = strings.Fields(string(raw))
		}
	}

	var entries []*models.CacheEntry
	for _, k := range keys {
		entry, err := c.GetCacheEntry(ctx, k)
		if err != nil {
			continue
		}
		if platform != nil && entry.Platform != nil {
			if entry.Platform.OS != platform.OS || entry.Platform.Arch != platform.Arch {
				continue
			}
		}
		// Skip expired entries.
		if entry.ExpiresAt != nil && time.Now().After(*entry.ExpiresAt) {
			_ = c.DeleteCacheEntry(ctx, k)
			continue
		}
		entries = append(entries, entry)
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

// DeleteCacheEntry removes a single cache entry.
func (c *CacheService) DeleteCacheEntry(ctx context.Context, cacheKey string) error {
	return c.meta.Delete(ctx, []byte(fmt.Sprintf(keyCacheEntry, cacheKey)))
}

// InvalidateCache removes entries matching the given criteria.
// If cascade=true, also removes entries for all downstream vertices.
func (c *CacheService) InvalidateCache(ctx context.Context, vertexID, dagID, cacheKey string, cascade bool) (int64, error) {
	var count int64
	if cacheKey != "" {
		if err := c.DeleteCacheEntry(ctx, cacheKey); err == nil {
			count++
		}
		return count, nil
	}
	entries, err := c.ListCacheEntries(ctx, dagID, vertexID, nil, 0)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := c.DeleteCacheEntry(ctx, e.CacheKey); err == nil {
			count++
		}
	}
	if cascade && dagID != "" {
		// Invalidate all entries for the whole DAG (simple strategy)
		raw, _ := c.meta.Get(ctx, []byte(fmt.Sprintf(keyCacheByDAG, dagID)))
		for _, k := range strings.Fields(string(raw)) {
			if err := c.DeleteCacheEntry(ctx, k); err == nil {
				count++
			}
		}
	}
	return count, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// StoreCacheRequest carries all fields needed to persist a cache entry.
type StoreCacheRequest struct {
	CacheKey      string
	VertexID      string
	DAGID         string
	OutputDigests []string
	OutputFiles   []models.FileRef
	TTL           time.Duration
	InlineData    []byte
	Kind          models.CacheEntryKind
	Platform      *models.Platform
	Metadata      map[string]string
}

func (c *CacheService) storeEntry(ctx context.Context, entry *models.CacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	opts := []metadata.PutOption{}
	if entry.ExpiresAt != nil {
		ttl := int64(time.Until(*entry.ExpiresAt).Seconds())
		if ttl > 0 {
			opts = append(opts, metadata.WithTTL(ttl))
		}
	}
	return c.meta.Put(ctx, []byte(fmt.Sprintf(keyCacheEntry, entry.CacheKey)), data, opts...)
}

func (c *CacheService) appendToVertexIndex(ctx context.Context, dagID, vertexID, cacheKey string) error {
	key := []byte(fmt.Sprintf(keyCacheByVertex, dagID, vertexID))
	return c.meta.Txn(ctx, func(txn metadata.Txn) error {
		raw, _ := txn.Get(key)
		cur := strings.TrimSpace(string(raw))
		if !strings.Contains(cur, cacheKey) {
			if cur != "" {
				cur += " "
			}
			cur += cacheKey
		}
		return txn.Put(key, []byte(cur))
	})
}

func (c *CacheService) appendToDAGIndex(ctx context.Context, dagID, cacheKey string) error {
	key := []byte(fmt.Sprintf(keyCacheByDAG, dagID))
	return c.meta.Txn(ctx, func(txn metadata.Txn) error {
		raw, _ := txn.Get(key)
		cur := strings.TrimSpace(string(raw))
		if !strings.Contains(cur, cacheKey) {
			if cur != "" {
				cur += " "
			}
			cur += cacheKey
		}
		return txn.Put(key, []byte(cur))
	})
}

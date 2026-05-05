package cache

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/user/layermerkle/layer"
)

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot / Restore — cross-build cache persistence
// ─────────────────────────────────────────────────────────────────────────────

// snapshotRecord is the JSON-serialisable form of one cache entry.
type snapshotRecord struct {
	FilePath    string      `json:"path"`
	LayerDigest string      `json:"layer"`
	Hash        []byte      `json:"hash,omitempty"`
	Algorithm   string      `json:"alg,omitempty"`
	SourceLayer string      `json:"src,omitempty"`
	Tombstone   bool        `json:"tomb,omitempty"`
	CachedAt    time.Time   `json:"ts"`
}

// Snapshot writes all current cache entries to w as newline-delimited JSON
// (one record per line). Tombstones are included.
//
// The snapshot is not transactionally consistent: entries inserted or evicted
// during the snapshot may or may not be captured. For best results call
// Snapshot after all ExecOps for a build have completed.
func (sc *ShardedCache) Snapshot(w io.Writer) error {
	enc := json.NewEncoder(w)
	var encErr error
	sc.walkAll(func(k CacheKey, e CacheEntry) bool {
		rec := snapshotRecord{
			FilePath:    k.FilePath,
			LayerDigest: string(k.LayerDigest),
			Hash:        e.Hash,
			Algorithm:   e.Algorithm,
			SourceLayer: string(e.SourceLayer),
			Tombstone:   e.Tombstone,
			CachedAt:    e.CachedAt,
		}
		if err := enc.Encode(rec); err != nil {
			encErr = fmt.Errorf("cache: snapshot encode: %w", err)
			return false
		}
		return true
	})
	return encErr
}

// Restore loads cache entries from the newline-delimited JSON produced by
// Snapshot. Existing entries are NOT overwritten (uses SetIfAbsent) so that
// entries computed in the current build take precedence.
//
// Returns the number of entries restored and the number skipped (because
// they already existed).
func (sc *ShardedCache) Restore(r io.Reader) (restored, skipped int, err error) {
	dec := json.NewDecoder(r)
	for {
		var rec snapshotRecord
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				break
			}
			return restored, skipped, fmt.Errorf("cache: restore decode: %w", err)
		}
		if rec.FilePath == "" || rec.LayerDigest == "" {
			continue
		}
		key := CacheKey{
			FilePath:    rec.FilePath,
			LayerDigest: layer.Digest(rec.LayerDigest),
		}
		entry := CacheEntry{
			Hash:        rec.Hash,
			Algorithm:   rec.Algorithm,
			SourceLayer: layer.Digest(rec.SourceLayer),
			Tombstone:   rec.Tombstone,
			CachedAt:    rec.CachedAt,
		}
		_, inserted := sc.SetIfAbsent(key, entry)
		if inserted {
			restored++
		} else {
			skipped++
		}
	}
	return restored, skipped, nil
}

// walkAll iterates over every entry in the cache without the layer filter.
// fn returns false to stop.
func (sc *ShardedCache) walkAll(fn func(CacheKey, CacheEntry) bool) {
	for i := range sc.shards {
		s := &sc.shards[i]
		s.mu.RLock()
		stop := false
		for k, v := range s.entries {
			if !fn(k, v) {
				stop = true
				break
			}
		}
		s.mu.RUnlock()
		if stop {
			return
		}
	}
}

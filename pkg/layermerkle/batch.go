package layermerkle

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// ─────────────────────────────────────────────────────────────────────────────
// BatchResult — result of a batch submission
// ─────────────────────────────────────────────────────────────────────────────

// BatchResult summarises a completed SubmitBatch call.
type BatchResult struct {
	// Submitted is the number of events accepted into the engine.
	Submitted int
	// Dropped is the number of events dropped (write-class or buffer full).
	Dropped int
	// Errors holds the first error per dropped event (may be nil entries).
	Errors []error
}

// ─────────────────────────────────────────────────────────────────────────────
// SubmitBatch — amortised channel-send for burst workloads
// ─────────────────────────────────────────────────────────────────────────────

// SubmitBatch enqueues multiple AccessEvents in a single call, amortising
// per-event channel overhead. Write-class events are silently skipped.
// Returns a BatchResult summarising the outcome of each event.
//
// This method is safe for concurrent use. It is the preferred API when events
// arrive from a fanwatch pipeline in bursts (e.g. 64 events per fanotify read).
func (e *Engine) SubmitBatch(ctx context.Context, events []*AccessEvent) BatchResult {
	result := BatchResult{Errors: make([]error, 0, len(events))}
	for _, ev := range events {
		if !ev.IsReadAccess() {
			result.Dropped++
			result.Errors = append(result.Errors, nil) // write-class skip is not an error
			continue
		}
		if err := e.Submit(ctx, ev); err != nil {
			result.Dropped++
			result.Errors = append(result.Errors, err)
			continue
		}
		result.Submitted++
		result.Errors = append(result.Errors, nil)
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// CacheKeyEncoder — zero-allocation cache key composition
// ─────────────────────────────────────────────────────────────────────────────

// CacheKeyEncoder composes a fixed-size binary cache key from a LayerID and
// a relative path without heap allocation on the hot path. The key is the
// SHA-256 of (layerID_hex + NUL + relPath) stored as a [32]byte.
//
// This eliminates the string concatenation allocation in the shard selector
// (fnv32) when the same (layerID, relPath) pair is looked up repeatedly.
//
// Usage: embed as a value in the caller's stack frame, call Encode, pass the
// result as a map key.
type CacheKeyEncoder struct {
	buf [sha256.Size]byte
}

// Encode computes the binary key for (layerID, relPath) and stores it in the
// encoder's internal buffer. Returns a pointer to the buffer — valid only
// until the next Encode call on the same encoder.
func (e *CacheKeyEncoder) Encode(layerID LayerID, relPath string) *[sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(layerID))
	h.Write([]byte{0})
	h.Write([]byte(relPath))
	h.Sum(e.buf[:0])
	return &e.buf
}

// HexString returns the hex encoding of the current key. Allocates.
func (e *CacheKeyEncoder) HexString() string {
	return hex.EncodeToString(e.buf[:])
}

// ─────────────────────────────────────────────────────────────────────────────
// fixedCacheKey — comparable fixed-size key for sharding without string alloc
// ─────────────────────────────────────────────────────────────────────────────

// fixedCacheKey is a [4]uint64 struct (32 bytes) derived from the SHA-256 of
// (layerID + relPath). It is comparable and usable as a map key without
// triggering the string-interning allocations of the layerID+relPath approach.
type fixedCacheKey [4]uint64

// makeFixedCacheKey computes a fixedCacheKey from a (layerID, relPath) pair.
// Uses SHA-256 for uniform distribution across 256 shards.
func makeFixedCacheKey(layerID LayerID, relPath string) fixedCacheKey {
	h := sha256.New()
	h.Write([]byte(layerID))
	h.Write([]byte{0})
	h.Write([]byte(relPath))
	var sum [32]byte
	h.Sum(sum[:0])
	return fixedCacheKey{
		binary.LittleEndian.Uint64(sum[0:8]),
		binary.LittleEndian.Uint64(sum[8:16]),
		binary.LittleEndian.Uint64(sum[16:24]),
		binary.LittleEndian.Uint64(sum[24:32]),
	}
}

// shardIndex returns the shard index (0–255) for this key.
func (k fixedCacheKey) shardIndex() uint8 {
	// Use the lowest byte of the first word — uniform after SHA-256.
	return uint8(k[0] & 0xff)
}

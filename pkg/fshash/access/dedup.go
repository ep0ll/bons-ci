package access

import (
	"context"
	"sync/atomic"

	"github.com/bons/bons-ci/pkg/fshash/cache"
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
	"github.com/bons/bons-ci/pkg/fshash/layer"
)

// Deduplicator classifies incoming access events into actions:
// compute (unique work), reuse (cached hash valid), or skip (session duplicate).
type Deduplicator struct {
	layers   layer.Store
	cache    cache.Store
	resolver *layer.Resolver
	filter   *BloomFilter
	tracker  *Tracker

	received atomic.Uint64
	computed atomic.Uint64
	reused   atomic.Uint64
	skipped  atomic.Uint64
	excluded atomic.Uint64
}

// NewDeduplicator creates a Deduplicator wired to the provided subsystems.
func NewDeduplicator(
	layers layer.Store,
	hashCache cache.Store,
	resolver *layer.Resolver,
	expectedItems uint,
	fpRate float64,
) *Deduplicator {
	return &Deduplicator{
		layers:   layers,
		cache:    hashCache,
		resolver: resolver,
		filter:   NewBloomFilter(expectedItems, fpRate),
		tracker:  NewTracker(),
	}
}

// Process classifies an AccessEvent. Hot path: completes in <1µs for
// ActionSkip/ActionReuse.
func (d *Deduplicator) Process(_ context.Context, event core.AccessEvent, chain *layer.Chain) core.DeduplicationResult {
	d.received.Add(1)

	result := core.DeduplicationResult{Event: event}

	// Step 0: Overlay visibility check
	if !d.resolver.IsFileVisible(chain, event.Path) {
		result.Action = core.ActionExclude
		d.excluded.Add(1)
		return result
	}

	// Step 1: Bloom filter fast-path
	if d.filter.TestAndAdd(event.LayerID, event.Path) {
		result.Action = core.ActionSkip
		d.skipped.Add(1)
		return result
	}

	// Step 2: Cache lookup — walk chain bottom-to-top
	var cachedHash core.FileHash
	var cacheHit bool

	for _, lid := range chain.Layers() {
		if h, ok := d.cache.Get(lid, event.Path); ok {
			cachedHash = h
			cacheHit = true
			break
		}
	}

	// Step 3: Ownership resolution
	if cacheHit {
		if !d.resolver.NeedsRehash(chain, event.Path, cachedHash.LayerID) {
			result.Action = core.ActionReuse
			result.CachedHash = cachedHash
			result.SourceLayer = cachedHash.LayerID
			d.reused.Add(1)
			d.tracker.Record(event.LayerID, cachedHash)
			return result
		}
	}

	// Step 4: Unique work
	result.Action = core.ActionCompute
	d.computed.Add(1)
	return result
}

// RecordComputed stores a freshly computed hash in cache and tracker.
func (d *Deduplicator) RecordComputed(hash core.FileHash) {
	d.cache.Put(hash.LayerID, hash.Path, hash)
	d.tracker.Record(hash.LayerID, hash)
}

// FileHashes returns all file hashes recorded for a layer.
func (d *Deduplicator) FileHashes(layerID core.LayerID) []core.FileHash {
	return d.tracker.FileHashes(layerID)
}

// ResetSession clears the bloom filter for a new ExecOp session.
func (d *Deduplicator) ResetSession() {
	d.filter.Reset()
}

// Stats returns deduplication statistics.
func (d *Deduplicator) Stats() core.ProcessorStats {
	return core.ProcessorStats{
		EventsReceived: d.received.Load(),
		EventsComputed: d.computed.Load(),
		EventsReused:   d.reused.Load(),
		EventsSkipped:  d.skipped.Load(),
		EventsExcluded: d.excluded.Load(),
	}
}

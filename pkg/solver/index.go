package solver

import (
	"context"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// edgeIndex detects cache key collisions and enables edge merging.
// When two edges resolve to the same cache key, one can be merged into
// the other, avoiding duplicate work. This matches BuildKit's edgeIndex
// in index.go.
type edgeIndex struct {
	mu       sync.Mutex
	items    map[string]*indexItem
	backRefs map[digest.Digest]map[string]struct{}
}

type indexItem struct {
	vertexDigest digest.Digest
	links        map[cacheInfoLink]map[string]struct{}
	deps         map[string]struct{}
}

type cacheInfoLink struct {
	input    Index
	digest   digest.Digest
	output   Index
	selector digest.Digest
}

func newEdgeIndex() *edgeIndex {
	return &edgeIndex{
		items:    map[string]*indexItem{},
		backRefs: map[digest.Digest]map[string]struct{}{},
	}
}

// LoadOrStore checks if a cache key already has an associated vertex.
// If so, returns the existing vertex digest (for merging).
// Otherwise, stores the new vertex and returns zero digest.
func (ei *edgeIndex) LoadOrStore(keyID string, vtxDigest digest.Digest) (digest.Digest, bool) {
	ei.mu.Lock()
	defer ei.mu.Unlock()

	item, ok := ei.items[keyID]
	if ok && item.vertexDigest != vtxDigest {
		// Another vertex already has this cache key → merge candidate.
		return item.vertexDigest, true
	}

	if !ok {
		item = &indexItem{
			links: make(map[cacheInfoLink]map[string]struct{}),
			deps:  make(map[string]struct{}),
		}
		ei.items[keyID] = item
	}
	item.vertexDigest = vtxDigest

	// Track back-reference.
	refs, ok := ei.backRefs[vtxDigest]
	if !ok {
		refs = make(map[string]struct{})
		ei.backRefs[vtxDigest] = refs
	}
	refs[keyID] = struct{}{}

	return digest.Digest(""), false
}

// Release removes all index entries for a vertex.
func (ei *edgeIndex) Release(vtxDigest digest.Digest) {
	ei.mu.Lock()
	defer ei.mu.Unlock()

	for id := range ei.backRefs[vtxDigest] {
		ei.releaseItem(id)
	}
	delete(ei.backRefs, vtxDigest)
}

func (ei *edgeIndex) releaseItem(id string) {
	item, ok := ei.items[id]
	if !ok {
		return
	}
	item.vertexDigest = ""
	if len(item.links) == 0 {
		for d := range item.deps {
			ei.releaseLink(d, id)
		}
		delete(ei.items, id)
	}
}

func (ei *edgeIndex) releaseLink(id, target string) {
	item, ok := ei.items[id]
	if !ok {
		return
	}
	for lid, links := range item.links {
		delete(links, target)
		if len(links) == 0 {
			delete(item.links, lid)
		}
	}
	if item.vertexDigest == "" && len(item.links) == 0 {
		for d := range item.deps {
			ei.releaseLink(d, id)
		}
		delete(ei.items, id)
	}
}

// ─── Cache opts propagation ──────────────────────────────────────────────────
// These functions propagate cache options through the vertex ancestor chain
// using context values, matching BuildKit's cacheopts.go.

// CacheOpts is a map of opaque cache option values.
type CacheOpts map[any]any

type cacheOptGetterKeyType struct{}

// WithCacheOptGetter attaches a cache option getter to the context.
func WithCacheOptGetter(ctx context.Context, getter func(includeAncestors bool, keys ...any) map[any]any) context.Context {
	return context.WithValue(ctx, cacheOptGetterKeyType{}, getter)
}

// CacheOptGetterOf retrieves the cache option getter from the context.
func CacheOptGetterOf(ctx context.Context) func(includeAncestors bool, keys ...any) map[any]any {
	if v := ctx.Value(cacheOptGetterKeyType{}); v != nil {
		if getter, ok := v.(func(includeAncestors bool, keys ...any) map[any]any); ok {
			return getter
		}
	}
	return nil
}

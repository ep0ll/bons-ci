package layermerkle

import (
	"context"
	"sync/atomic"
)

// EngineHook is the observer strategy interface for engine internals.
// All methods are called synchronously on the worker goroutine and must not block.
type EngineHook interface {
	OnCacheHit(ctx context.Context, req HashRequest, result *HashResult)
	OnHashStart(ctx context.Context, req HashRequest)
	OnHashComplete(ctx context.Context, req HashRequest, h FileHash)
	OnHashError(ctx context.Context, req HashRequest, err error)
	OnEventDropped(ctx context.Context, ev *AccessEvent, reason error)
	OnVertexFinalized(ctx context.Context, tree *MerkleTree)
}

// ─────────────────────────────────────────────────────────────────────────────
// NoopHook — embed to implement only the methods you need
// ─────────────────────────────────────────────────────────────────────────────

// NoopHook is a do-nothing EngineHook.
type NoopHook struct{}

func (NoopHook) OnCacheHit(_ context.Context, _ HashRequest, _ *HashResult)  {}
func (NoopHook) OnHashStart(_ context.Context, _ HashRequest)                 {}
func (NoopHook) OnHashComplete(_ context.Context, _ HashRequest, _ FileHash)  {}
func (NoopHook) OnHashError(_ context.Context, _ HashRequest, _ error)        {}
func (NoopHook) OnEventDropped(_ context.Context, _ *AccessEvent, _ error)    {}
func (NoopHook) OnVertexFinalized(_ context.Context, _ *MerkleTree)           {}

// ─────────────────────────────────────────────────────────────────────────────
// HookChain — fan-out Composite pattern
// ─────────────────────────────────────────────────────────────────────────────

// HookChain fans each engine event to all registered hooks in order.
type HookChain struct {
	hooks []EngineHook
}

// NewHookChain constructs a HookChain from zero or more hooks.
func NewHookChain(hooks ...EngineHook) *HookChain {
	return &HookChain{hooks: hooks}
}

// Add appends a hook to the chain. Not safe after engine startup.
func (c *HookChain) Add(h EngineHook) { c.hooks = append(c.hooks, h) }

func (c *HookChain) OnCacheHit(ctx context.Context, req HashRequest, r *HashResult) {
	for _, h := range c.hooks {
		h.OnCacheHit(ctx, req, r)
	}
}

func (c *HookChain) OnHashStart(ctx context.Context, req HashRequest) {
	for _, h := range c.hooks {
		h.OnHashStart(ctx, req)
	}
}

func (c *HookChain) OnHashComplete(ctx context.Context, req HashRequest, d FileHash) {
	for _, h := range c.hooks {
		h.OnHashComplete(ctx, req, d)
	}
}

func (c *HookChain) OnHashError(ctx context.Context, req HashRequest, err error) {
	for _, h := range c.hooks {
		h.OnHashError(ctx, req, err)
	}
}

func (c *HookChain) OnEventDropped(ctx context.Context, ev *AccessEvent, reason error) {
	for _, h := range c.hooks {
		h.OnEventDropped(ctx, ev, reason)
	}
}

func (c *HookChain) OnVertexFinalized(ctx context.Context, tree *MerkleTree) {
	for _, h := range c.hooks {
		h.OnVertexFinalized(ctx, tree)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CountingHook — atomic counters for tests and lightweight metrics
// ─────────────────────────────────────────────────────────────────────────────

// CountingHook tallies engine events with atomic counters. Safe for concurrent use.
type CountingHook struct {
	NoopHook
	cacheHits  atomic.Int64
	hashStarts atomic.Int64
	hashErrors atomic.Int64
	dropped    atomic.Int64
	finalized  atomic.Int64
}

// CountingSnapshot is an immutable point-in-time view of CountingHook state.
type CountingSnapshot struct {
	CacheHits  int64
	HashStarts int64
	HashErrors int64
	Dropped    int64
	Finalized  int64
}

func (h *CountingHook) OnCacheHit(_ context.Context, _ HashRequest, _ *HashResult) {
	h.cacheHits.Add(1)
}
func (h *CountingHook) OnHashStart(_ context.Context, _ HashRequest)          { h.hashStarts.Add(1) }
func (h *CountingHook) OnHashError(_ context.Context, _ HashRequest, _ error) { h.hashErrors.Add(1) }
func (h *CountingHook) OnEventDropped(_ context.Context, _ *AccessEvent, _ error) {
	h.dropped.Add(1)
}
func (h *CountingHook) OnVertexFinalized(_ context.Context, _ *MerkleTree) { h.finalized.Add(1) }

// Snapshot returns a point-in-time copy of all counters.
func (h *CountingHook) Snapshot() CountingSnapshot {
	return CountingSnapshot{
		CacheHits:  h.cacheHits.Load(),
		HashStarts: h.hashStarts.Load(),
		HashErrors: h.hashErrors.Load(),
		Dropped:    h.dropped.Load(),
		Finalized:  h.finalized.Load(),
	}
}

// Reset zeroes all counters.
func (h *CountingHook) Reset() {
	h.cacheHits.Store(0)
	h.hashStarts.Store(0)
	h.hashErrors.Store(0)
	h.dropped.Store(0)
	h.finalized.Store(0)
}

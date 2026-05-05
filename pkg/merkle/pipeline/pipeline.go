// Package pipeline wires together the cache, DedupEngine, MerkleRegistry, and
// HookChain into an event-driven, concurrent processing pipeline.
//
// # Architecture
//
//	EventSource (fanotify / channel)
//	         │
//	         ▼  chan *event.FileAccessEvent
//	    ┌────────────┐
//	    │  Pipeline  │  dispatcher goroutine
//	    └─────┬──────┘
//	          │ fan-out to work channel
//	    ┌─────▼──────────────────────────┐
//	    │  Worker pool (N goroutines)    │
//	    │  dedup.Engine.Process()        │
//	    │  hook.HookChain.Fire()         │
//	    └────────────────────────────────┘
//
// # Ordering note
//
// The pipeline is concurrent — N workers process events in parallel. Events
// from DIFFERENT ExecOps (different output layers) are processed independently
// and may interleave. Events within a single ExecOp can also race. Cross-layer
// hash promotion requires that the lower-layer events have been fully processed
// before the higher-layer events are dispatched. This matches the real-world
// constraint: ExecOps execute sequentially, so their fanotify streams arrive
// sequentially even if events within each stream are concurrent.
//
// # Run semantics
//
// Run may be called multiple times on the same Pipeline (e.g., once per
// ExecOp). Each call drains eventCh until it closes, then returns. The result
// channel (if enabled) is NOT closed between calls — it is only closed after
// the final call via Close().
package pipeline

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/layermerkle/cache"
	"github.com/user/layermerkle/dedup"
	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/hook"
	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/merkle"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// Config holds all construction parameters for a Pipeline.
type Config struct {
	workers      int
	bufferSize   int
	resultBuffer int
	hashProvider hash.HashProvider
	cache        cache.Cache
	registry     *merkle.Registry
	hookChain    *hook.HookChain
}

func defaultConfig() Config {
	return Config{
		workers:      runtime.NumCPU(),
		bufferSize:   4096,
		resultBuffer: 1024,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Option
// ─────────────────────────────────────────────────────────────────────────────

// Option is a functional option that configures a Pipeline.
type Option func(*Config) error

// WithWorkers sets the number of concurrent worker goroutines (minimum 1).
func WithWorkers(n int) Option {
	return func(c *Config) error {
		if n < 1 {
			return fmt.Errorf("pipeline: workers must be >= 1, got %d", n)
		}
		c.workers = n
		return nil
	}
}

// WithBufferSize sets the work-channel buffer depth (minimum 1).
func WithBufferSize(n int) Option {
	return func(c *Config) error {
		if n < 1 {
			return fmt.Errorf("pipeline: buffer size must be >= 1, got %d", n)
		}
		c.bufferSize = n
		return nil
	}
}

// WithResultBuffer sets the result-channel buffer depth. Set to 0 to disable
// the result channel entirely (results only flow to hooks).
func WithResultBuffer(n int) Option {
	return func(c *Config) error {
		if n < 0 {
			return fmt.Errorf("pipeline: result buffer must be >= 0, got %d", n)
		}
		c.resultBuffer = n
		return nil
	}
}

// WithHashProvider sets the HashProvider (required).
func WithHashProvider(hp hash.HashProvider) Option {
	return func(c *Config) error {
		if hp == nil {
			return fmt.Errorf("pipeline: HashProvider must not be nil")
		}
		c.hashProvider = hp
		return nil
	}
}

// WithCache sets an external Cache implementation.
// Defaults to a new ShardedCache when not set.
func WithCache(ca cache.Cache) Option {
	return func(c *Config) error {
		if ca == nil {
			return fmt.Errorf("pipeline: Cache must not be nil")
		}
		c.cache = ca
		return nil
	}
}

// WithRegistry sets an external MerkleRegistry.
// Defaults to a new Registry when not set.
func WithRegistry(r *merkle.Registry) Option {
	return func(c *Config) error {
		if r == nil {
			return fmt.Errorf("pipeline: Registry must not be nil")
		}
		c.registry = r
		return nil
	}
}

// WithHook appends a Hook to the pipeline's HookChain.
// Multiple WithHook calls accumulate hooks in registration order.
func WithHook(h hook.Hook) Option {
	return func(c *Config) error {
		if h == nil {
			return fmt.Errorf("pipeline: Hook must not be nil")
		}
		if c.hookChain == nil {
			c.hookChain = hook.NewHookChain(false)
		}
		c.hookChain.Add(h)
		return nil
	}
}

// WithLenientHooks switches the HookChain to lenient mode: all hooks fire
// even when one returns an error.
func WithLenientHooks() Option {
	return func(c *Config) error {
		if c.hookChain == nil {
			c.hookChain = hook.NewHookChain(true)
		}
		return nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PipelineStats
// ─────────────────────────────────────────────────────────────────────────────

// PipelineStats is a snapshot of pipeline runtime metrics.
type PipelineStats struct {
	EventsReceived  int64
	EventsProcessed int64
	EventsFailed    int64
	CacheHits       int64
	CacheMisses     int64
	Tombstones      int64
	HookErrors      int64 // cumulative non-fatal hook errors
	Uptime          time.Duration
	CacheStats      cache.Stats
}

// String returns a human-readable summary.
func (s PipelineStats) String() string {
	return fmt.Sprintf(
		"events(rx=%d ok=%d fail=%d) cache(hit=%d miss=%d) tombstones=%d uptime=%s | %s",
		s.EventsReceived, s.EventsProcessed, s.EventsFailed,
		s.CacheHits, s.CacheMisses, s.Tombstones, s.Uptime, s.CacheStats,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline
// ─────────────────────────────────────────────────────────────────────────────

// Pipeline is the top-level event-driven processor. It owns the worker pool,
// hook chain, and exposes Seal/Proof/Leaves for finalising Merkle trees.
//
// # Lifecycle
//  1. New(opts...) — construct
//  2. Run(ctx, eventCh) — process one ExecOp's event stream (repeatable)
//  3. Seal(ctx, layerDigest) — finalise a layer's Merkle tree (any time)
//  4. Close() — close the result channel after the final Run
//  5. Proof/Leaves/Root — query sealed trees
type Pipeline struct {
	cfg      Config
	engine   *dedup.Engine
	registry *merkle.Registry
	cache    cache.Cache
	hooks    *hook.HookChain

	// results channel — nil if resultBuffer == 0; stays open between Run calls.
	results chan dedup.Result

	// closeOnce ensures the results channel is only closed once.
	closeOnce sync.Once

	// hookErrors tracks cumulative hook chain errors (non-fatal to the pipeline).
	hookErrors      atomic.Int64
	eventsReceived  atomic.Int64
	eventsProcessed atomic.Int64
	eventsFailed    atomic.Int64
	cacheHits       atomic.Int64
	cacheMisses     atomic.Int64
	tombstones      atomic.Int64

	startedAt time.Time
}

// New creates a Pipeline with the given options.
// Returns an error if any required option is missing or invalid.
func New(opts ...Option) (*Pipeline, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		if err := o(&cfg); err != nil {
			return nil, err
		}
	}

	if cfg.hashProvider == nil {
		return nil, fmt.Errorf("pipeline: HashProvider is required (use WithHashProvider)")
	}
	if cfg.cache == nil {
		cfg.cache = cache.NewShardedCache()
	}
	if cfg.registry == nil {
		cfg.registry = merkle.NewRegistry()
	}
	if cfg.hookChain == nil {
		cfg.hookChain = hook.NewHookChain(false)
	}

	p := &Pipeline{
		cfg:       cfg,
		engine:    dedup.NewEngine(cfg.cache, cfg.hashProvider, cfg.registry),
		registry:  cfg.registry,
		cache:     cfg.cache,
		hooks:     cfg.hookChain,
		startedAt: time.Now(),
	}
	if cfg.resultBuffer > 0 {
		p.results = make(chan dedup.Result, cfg.resultBuffer)
	}
	return p, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Run
// ─────────────────────────────────────────────────────────────────────────────

// Run starts the worker pool and dispatches events from eventCh to workers.
// Run blocks until either ctx is cancelled or eventCh is closed, at which
// point all in-flight events are drained before returning.
//
// Run is safe to call multiple times sequentially (once per ExecOp). The
// result channel remains open between calls. Call Close() after the final
// Run to signal result consumers.
//
// Run returns ctx.Err() when context cancellation caused the exit, nil when
// eventCh was closed normally.
func (p *Pipeline) Run(ctx context.Context, eventCh <-chan *event.FileAccessEvent) error {
	p.fireHook(ctx, hook.HookEvent{
		Type:      hook.HookPipelineStarted,
		Timestamp: time.Now(),
	})

	work := make(chan *event.FileAccessEvent, p.cfg.bufferSize)

	var wg sync.WaitGroup
	for i := 0; i < p.cfg.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ev := range work {
				p.processOne(ctx, ev)
			}
		}()
	}

	var runErr error
dispatch:
	for {
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
			break dispatch
		case ev, ok := <-eventCh:
			if !ok {
				break dispatch
			}
			p.eventsReceived.Add(1)
			select {
			case work <- ev:
			case <-ctx.Done():
				runErr = ctx.Err()
				break dispatch
			}
		}
	}

	close(work)
	wg.Wait()

	p.fireHook(ctx, hook.HookEvent{
		Type:      hook.HookPipelineStopped,
		Timestamp: time.Now(),
	})
	return runErr
}

// Close closes the result channel (if enabled). Call once after the final Run.
// Subsequent calls are safe (no-op). Do NOT call Close concurrently with Run.
func (p *Pipeline) Close() {
	p.closeOnce.Do(func() {
		if p.results != nil {
			close(p.results)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// processOne
// ─────────────────────────────────────────────────────────────────────────────

// processOne handles one event: fires the received hook, processes via the
// dedup engine, updates stats, fires the outcome hook, and forwards the result.
func (p *Pipeline) processOne(ctx context.Context, ev *event.FileAccessEvent) {
	outputLayer := ev.OutputLayer()

	p.fireHook(ctx, hook.HookEvent{
		Type:        hook.HookEventReceived,
		Event:       ev,
		LayerDigest: outputLayer,
		Timestamp:   time.Now(),
	})

	result := p.engine.Process(ctx, ev)
	p.eventsProcessed.Add(1)

	// Update stats counters and map to the correct hook type.
	var hookType hook.HookType
	switch result.Kind {
	case dedup.ResultCacheHit:
		p.cacheHits.Add(1)
		hookType = hook.HookCacheHit
	case dedup.ResultCacheMiss:
		p.cacheMisses.Add(1)
		hookType = hook.HookHashComputed
	case dedup.ResultTombstone:
		p.tombstones.Add(1)
		hookType = hook.HookTombstone
	case dedup.ResultError:
		p.eventsFailed.Add(1)
		hookType = hook.HookError
	default:
		hookType = hook.HookCacheHit
	}

	// Fire the primary outcome hook.
	p.fireHook(ctx, hook.HookEvent{
		Type:        hookType,
		Event:       ev,
		LayerDigest: outputLayer,
		Hash:        result.Hash,
		SourceLayer: result.SourceLayer,
		Err:         result.Error,
		Timestamp:   time.Now(),
	})

	if result.MerkleLeafAdded {
		p.fireHook(ctx, hook.HookEvent{
			Type:        hook.HookMerkleLeafAdded,
			Event:       ev,
			LayerDigest: outputLayer,
			Hash:        result.Hash,
			SourceLayer: result.SourceLayer,
			Timestamp:   time.Now(),
		})
	}

	// Non-blocking forward to result channel.
	if p.results != nil {
		select {
		case p.results <- result:
		default:
			// Result channel full; hooks have already fired. Drop silently.
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Results channel
// ─────────────────────────────────────────────────────────────────────────────

// Results returns the channel on which processed dedup.Results are sent.
// Returns nil when the pipeline was created with WithResultBuffer(0).
// The channel is NOT automatically closed between Run calls; call Close().
func (p *Pipeline) Results() <-chan dedup.Result {
	if p.results == nil {
		return nil
	}
	return p.results
}

// ─────────────────────────────────────────────────────────────────────────────
// Seal / Query
// ─────────────────────────────────────────────────────────────────────────────

// Seal finalises the Merkle tree for layerDigest and returns its root hash.
// Fires HookLayerSealed. Idempotent — may be called multiple times.
func (p *Pipeline) Seal(ctx context.Context, layerDigest layer.Digest) ([]byte, error) {
	root := p.registry.Seal(layerDigest)
	leafCount := p.registry.LeafCount(layerDigest)
	p.fireHook(ctx, hook.HookEvent{
		Type:        hook.HookLayerSealed,
		LayerDigest: layerDigest,
		MerkleRoot:  root,
		LeafCount:   leafCount,
		Timestamp:   time.Now(),
	})
	return root, nil
}

// SealAll seals every registered layer tree and returns a layer→root map.
// Fires HookLayerSealed for each layer.
func (p *Pipeline) SealAll(ctx context.Context) map[layer.Digest][]byte {
	roots := p.registry.SealAll()
	for d, root := range roots {
		p.fireHook(ctx, hook.HookEvent{
			Type:        hook.HookLayerSealed,
			LayerDigest: d,
			MerkleRoot:  root,
			LeafCount:   p.registry.LeafCount(d),
			Timestamp:   time.Now(),
		})
	}
	return roots
}

// Root returns the Merkle root for a sealed layer.
func (p *Pipeline) Root(layerDigest layer.Digest) ([]byte, error) {
	return p.registry.Root(layerDigest)
}

// Proof returns a Merkle inclusion proof for filePath in layerDigest's tree.
func (p *Pipeline) Proof(layerDigest layer.Digest, filePath string) (*merkle.Proof, error) {
	return p.registry.Proof(layerDigest, filePath)
}

// Submit enqueues a batch of events directly into the worker pool without
// going through the event channel. This avoids channel overhead for callers
// that produce events synchronously (e.g., replay from fanotify log files).
//
// Submit blocks until all events in the batch have been processed. It is safe
// to call concurrently with Run. Do NOT call Submit after the pipeline has
// been closed.
func (p *Pipeline) Submit(ctx context.Context, events []*event.FileAccessEvent) {
	var wg sync.WaitGroup
	for _, ev := range events {
		wg.Add(1)
		ev := ev
		go func() {
			defer wg.Done()
			p.eventsReceived.Add(1)
			p.processOne(ctx, ev)
		}()
	}
	wg.Wait()
}

// Leaves returns the sorted leaf list for layerDigest's sealed tree.
func (p *Pipeline) Leaves(layerDigest layer.Digest) ([]merkle.Leaf, error) {
	return p.registry.Leaves(layerDigest)
}
// appears consistently across multiple sealed layers. The proof is a slice
// of per-layer inclusion proofs, one per layer in the stack (base → output).
// Layers for which the file has no leaf (e.g., deleted via tombstone) are
// omitted. Returns an error if no layers are sealed or no proofs are found.
func (p *Pipeline) CrossLayerProof(layerStack layer.Stack, filePath string) ([]*merkle.Proof, error) {
	var proofs []*merkle.Proof
	for _, d := range layerStack {
		proof, err := p.registry.Proof(d, filePath)
		if err != nil {
			continue // file not a leaf in this layer (deleted or not accessed)
		}
		proofs = append(proofs, proof)
	}
	if len(proofs) == 0 {
		return nil, fmt.Errorf("pipeline: no cross-layer proof found for %q in stack %s", filePath, layerStack)
	}
	return proofs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Observability
// ─────────────────────────────────────────────────────────────────────────────

// Stats returns a snapshot of pipeline statistics.
func (p *Pipeline) Stats() PipelineStats {
	return PipelineStats{
		EventsReceived:  p.eventsReceived.Load(),
		EventsProcessed: p.eventsProcessed.Load(),
		EventsFailed:    p.eventsFailed.Load(),
		CacheHits:       p.cacheHits.Load(),
		CacheMisses:     p.cacheMisses.Load(),
		Tombstones:      p.tombstones.Load(),
		HookErrors:      p.hookErrors.Load(),
		Uptime:          time.Since(p.startedAt),
		CacheStats:      p.cache.Stats(),
	}
}

// fireHook fires a hook event and tracks any errors in stats.
// Hook errors are non-fatal — they are counted but do not abort processing.
func (p *Pipeline) fireHook(ctx context.Context, e hook.HookEvent) {
	if err := p.hooks.Fire(ctx, e); err != nil {
		p.hookErrors.Add(1)
	}
}

// Cache returns the underlying Cache for direct inspection.
func (p *Pipeline) Cache() cache.Cache { return p.cache }

// Registry returns the MerkleRegistry for direct access.
func (p *Pipeline) Registry() *merkle.Registry { return p.registry }

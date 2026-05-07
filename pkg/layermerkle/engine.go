package layermerkle

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Engine — top-level façade (Façade + Builder patterns)
// ─────────────────────────────────────────────────────────────────────────────

// Engine is the top-level façade that connects an AccessEvent source to the
// deduplication pipeline and Merkle tree construction.
//
// Lifecycle:
//  1. Construct with NewEngine(opts...).
//  2. Register layers via Engine.Registry().Register(...).
//  3. Start the engine with Engine.Start(ctx).
//  4. Feed events via Engine.Submit or Engine.Feed.
//  5. Call Engine.FinalizeVertex when an ExecOp completes.
//  6. Cancel ctx or call Engine.Stop to drain and shut down.
type Engine struct {
	cfg       engineConfig
	registry  *LayerRegistry
	cache     HashCache
	hasher    FileHasher
	resolver  LayerFileResolver
	hooks     *HookChain
	forest    *MerkleForest
	dedup     *DeduplicationEngine
	processor *VertexProcessor
	onTree    func(*MerkleTree)

	mu      sync.Mutex
	running bool
	eventCh chan *AccessEvent
	wg      sync.WaitGroup
}

// engineConfig holds all tunable Engine parameters.
type engineConfig struct {
	workers        int
	eventBufSize   int
	cacheCapacity  int
	statCacheSize  int
}

func defaultEngineConfig() engineConfig {
	return engineConfig{
		workers:       runtime.NumCPU(),
		eventBufSize:  4096,
		cacheCapacity: 128_000,
		statCacheSize: 200_000,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EngineOption — functional options (Builder pattern)
// ─────────────────────────────────────────────────────────────────────────────

// EngineOption configures an Engine at construction time.
type EngineOption func(*Engine)

// WithFileHasher sets the file hashing strategy. Defaults to NewSHA256Hasher().
func WithFileHasher(h FileHasher) EngineOption {
	return func(e *Engine) { e.hasher = h }
}

// WithHashCache sets the hash cache. Defaults to NewShardedLRUCache(128_000).
func WithHashCache(c HashCache) EngineOption {
	return func(e *Engine) { e.cache = c }
}

// WithResolver sets the layer file resolver.
// Defaults to NewOverlayResolver(registry, 200_000).
func WithResolver(r LayerFileResolver) EngineOption {
	return func(e *Engine) { e.resolver = r }
}

// WithHook registers an EngineHook. Multiple calls append hooks in order.
func WithHook(h EngineHook) EngineOption {
	return func(e *Engine) { e.hooks.Add(h) }
}

// WithWorkers sets the number of concurrent hash worker goroutines.
func WithWorkers(n int) EngineOption {
	return func(e *Engine) {
		if n > 0 {
			e.cfg.workers = n
		}
	}
}

// WithEventBufferSize sets the input channel buffer depth.
func WithEventBufferSize(n int) EngineOption {
	return func(e *Engine) {
		if n > 0 {
			e.cfg.eventBufSize = n
		}
	}
}

// WithCacheCapacity sets the total hash cache capacity.
func WithCacheCapacity(n int) EngineOption {
	return func(e *Engine) {
		if n > 0 {
			e.cfg.cacheCapacity = n
		}
	}
}

// WithPredefinedRegistry sets a pre-populated LayerRegistry on the engine,
// replacing the default empty one. Use when you need to register layers before
// the engine starts (e.g. warm-start scenarios with a known layer catalogue).
func WithPredefinedRegistry(r *LayerRegistry) EngineOption {
	return func(e *Engine) { e.registry = r }
}

// WithOnTree sets a callback invoked each time a MerkleTree is finalized.
func WithOnTree(fn func(*MerkleTree)) EngineOption {
	return func(e *Engine) { e.onTree = fn }
}

// ─────────────────────────────────────────────────────────────────────────────
// NewEngine — constructor
// ─────────────────────────────────────────────────────────────────────────────

// NewEngine constructs an Engine from functional options.
// Default values are chosen for production use; override with option functions.
func NewEngine(opts ...EngineOption) *Engine {
	e := &Engine{
		cfg:      defaultEngineConfig(),
		registry: NewLayerRegistry(),
		forest:   NewMerkleForest(),
		hooks:    NewHookChain(),
	}
	for _, o := range opts {
		o(e)
	}
	e.applyDefaults()
	e.wire()
	return e
}

// applyDefaults fills in nil components with sensible defaults.
func (e *Engine) applyDefaults() {
	if e.hasher == nil {
		e.hasher = NewSingleflightHasher(NewSHA256Hasher())
	}
	if e.cache == nil {
		e.cache = NewShardedLRUCache(e.cfg.cacheCapacity)
	}
	if e.resolver == nil {
		e.resolver = NewOverlayResolver(e.registry, e.cfg.statCacheSize)
	}
	if e.onTree == nil {
		e.onTree = func(*MerkleTree) {}
	}
}

// wire creates internal components that depend on the configured dependencies.
func (e *Engine) wire() {
	e.dedup = NewDeduplicationEngine(e.cache, e.hasher, e.resolver, e.hooks)
	e.processor = NewVertexProcessor(e.dedup, e.forest, func(t *MerkleTree) {
		e.hooks.OnVertexFinalized(context.Background(), t)
		e.onTree(t)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Lifecycle
// ─────────────────────────────────────────────────────────────────────────────

// Start launches the event-processing workers. Must be called before Submit.
// Returns ErrEngineNotRunning if already started.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return fmt.Errorf("layermerkle: engine already running")
	}
	e.running = true
	e.eventCh = make(chan *AccessEvent, e.cfg.eventBufSize)

	sem := make(chan struct{}, e.cfg.workers)
	e.wg.Add(1)
	go e.dispatchLoop(ctx, sem)
	return nil
}

// Stop drains in-flight events and waits for all workers to exit.
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	close(e.eventCh)
	e.mu.Unlock()
	e.wg.Wait()
}

// ─────────────────────────────────────────────────────────────────────────────
// Event submission
// ─────────────────────────────────────────────────────────────────────────────

// Submit enqueues one AccessEvent for processing. Non-blocking: drops the event
// and records OnEventDropped when the buffer is full or the engine is stopped.
func (e *Engine) Submit(ctx context.Context, ev *AccessEvent) error {
	e.mu.Lock()
	running := e.running
	ch := e.eventCh
	e.mu.Unlock()

	if !running {
		e.hooks.OnEventDropped(ctx, ev, ErrEngineNotRunning)
		return ErrEngineNotRunning
	}

	select {
	case ch <- ev:
		return nil
	case <-ctx.Done():
		e.hooks.OnEventDropped(ctx, ev, ctx.Err())
		return ctx.Err()
	default:
		e.hooks.OnEventDropped(ctx, ev, ErrEventDropped)
		return ErrEventDropped
	}
}

// Feed reads AccessEvents from in until it is closed or ctx is cancelled.
// Blocks until the source is exhausted. Errors from Submit are forwarded to
// onErr (may be nil). Use this as the primary integration point with fanwatch.
func (e *Engine) Feed(ctx context.Context, in <-chan *AccessEvent, onErr func(error)) {
	for {
		select {
		case ev, ok := <-in:
			if !ok {
				return
			}
			if !ev.IsReadAccess() {
				continue // skip write-class events
			}
			if err := e.Submit(ctx, ev); err != nil && onErr != nil {
				onErr(err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// FinalizeVertex seals the MerkleTree for vertexID and returns it.
// Should be called when the ExecOp for vertexID has completed execution.
func (e *Engine) FinalizeVertex(vertexID VertexID) (*MerkleTree, error) {
	return e.processor.FinalizeVertex(vertexID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Accessors
// ─────────────────────────────────────────────────────────────────────────────

// Registry returns the LayerRegistry for registering overlay layers.
func (e *Engine) Registry() *LayerRegistry { return e.registry }

// Forest returns the MerkleForest containing all finalized trees.
func (e *Engine) Forest() *MerkleForest { return e.forest }

// CacheStats returns a snapshot of hash cache performance.
func (e *Engine) CacheStats() CacheStats { return e.cache.Stats() }

// ─────────────────────────────────────────────────────────────────────────────
// Internal dispatch loop
// ─────────────────────────────────────────────────────────────────────────────

// dispatchLoop pulls events from eventCh and fans them out to worker goroutines.
func (e *Engine) dispatchLoop(ctx context.Context, sem chan struct{}) {
	defer e.wg.Done()

	for ev := range e.eventCh {
		if ctx.Err() != nil {
			e.hooks.OnEventDropped(ctx, ev, ctx.Err())
			continue
		}
		sem <- struct{}{}
		e.wg.Add(1)
		go func(event *AccessEvent) {
			defer e.wg.Done()
			defer func() { <-sem }()
			e.processor.Process(ctx, event)
		}(ev)
	}
}

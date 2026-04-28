package fshash

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/bons/bons-ci/pkg/fshash/access"
	"github.com/bons/bons-ci/pkg/fshash/cache"
	"github.com/bons/bons-ci/pkg/fshash/chunk"
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
	"github.com/bons/bons-ci/pkg/fshash/layer"
	"github.com/bons/bons-ci/pkg/fshash/merkle"
	"github.com/bons/bons-ci/pkg/fshash/overlay"
)

// Processor is the public-facing orchestrator for the Merkle tree
// deduplication engine. It composes all subsystems and provides an
// event-driven API for processing filesystem access events.
type Processor struct {
	cfg    processorConfig
	hasher chunk.Hasher
	pool   *chunk.Pool

	layers      layer.Store
	cache       cache.Store
	resolver    *layer.Resolver
	dedup       *access.Deduplicator
	interpreter *overlay.Interpreter

	chainMu sync.RWMutex
	chains  map[string]*layer.Chain

	eventCh chan submitRequest
	wg      sync.WaitGroup
	closed  atomic.Bool

	hooks Hooks
}

type submitRequest struct {
	ctx   context.Context
	event core.AccessEvent
	errCh chan<- error
}

// NewProcessor creates a Processor with the given options.
func NewProcessor(opts ...Option) *Processor {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	layers := layer.NewMemoryStore()
	hashCache := cache.NewShardedStore(cfg.cacheShards, cfg.cacheMaxEntries)
	resolver := layer.NewResolver(layers)
	dedup := access.NewDeduplicator(layers, hashCache, resolver, cfg.bloomExpectedItems, cfg.bloomFPRate)
	interpreter := overlay.NewInterpreter()

	p := &Processor{
		cfg:         cfg,
		hasher:      chunk.NewHasher(cfg.hashAlgorithm),
		pool:        chunk.NewPool(),
		layers:      layers,
		cache:       hashCache,
		resolver:    resolver,
		dedup:       dedup,
		interpreter: interpreter,
		chains:      make(map[string]*layer.Chain),
		eventCh:     make(chan submitRequest, cfg.channelBuffer),
		hooks:       cfg.hooks,
	}

	for i := 0; i < cfg.workerCount; i++ {
		p.wg.Add(1)
		go p.worker()
	}

	return p
}

// RegisterLayer declares a new layer in the stack.
func (p *Processor) RegisterLayer(ctx context.Context, id, parentID core.LayerID) error {
	if p.closed.Load() {
		return core.ErrClosed
	}

	if err := p.layers.Register(ctx, id, parentID); err != nil {
		return err
	}

	p.chainMu.Lock()
	defer p.chainMu.Unlock()

	builder := layer.NewChainBuilder()
	if !parentID.IsZero() {
		if parentChain, ok := p.chains[parentID.String()]; ok {
			for _, l := range parentChain.Layers() {
				builder.Push(l)
			}
		}
	}
	builder.Push(id)
	p.chains[id.String()] = builder.Build()

	p.hooks.fireLayerRegistered(ctx, id)
	return nil
}

// MarkModified records that a file was modified in the given layer.
func (p *Processor) MarkModified(layerID core.LayerID, path string) error {
	if p.closed.Load() {
		return core.ErrClosed
	}
	return p.layers.MarkModified(layerID, path)
}

// MarkDeleted records that a file was deleted in the given layer.
func (p *Processor) MarkDeleted(layerID core.LayerID, path string) error {
	if p.closed.Load() {
		return core.ErrClosed
	}
	return p.layers.MarkDeleted(layerID, path)
}

// MarkOpaque records that a directory was made opaque in the given layer.
func (p *Processor) MarkOpaque(layerID core.LayerID, path string) error {
	if p.closed.Load() {
		return core.ErrClosed
	}
	return p.layers.MarkOpaque(layerID, path)
}

// Submit enqueues an access event (blocking).
func (p *Processor) Submit(ctx context.Context, event core.AccessEvent) error {
	if p.closed.Load() {
		return core.ErrClosed
	}
	if err := event.Validate(); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	select {
	case p.eventCh <- submitRequest{ctx: ctx, event: event, errCh: errCh}:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubmitAsync enqueues without waiting for processing.
func (p *Processor) SubmitAsync(ctx context.Context, event core.AccessEvent) error {
	if p.closed.Load() {
		return core.ErrClosed
	}
	if err := event.Validate(); err != nil {
		return err
	}
	select {
	case p.eventCh <- submitRequest{ctx: ctx, event: event}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Finalize builds the Merkle tree for a layer and returns the root node.
func (p *Processor) Finalize(ctx context.Context, layerID core.LayerID) (*merkle.Node, error) {
	if p.closed.Load() {
		return nil, core.ErrClosed
	}

	hashes := p.dedup.FileHashes(layerID)
	if len(hashes) == 0 {
		return nil, fmt.Errorf("%w: no files tracked for layer %s", core.ErrTreeEmpty, layerID)
	}

	sort.Slice(hashes, func(i, j int) bool {
		return hashes[i].Path < hashes[j].Path
	})

	tree := merkle.NewTree(merkle.WithTreeAlgorithm(p.cfg.hashAlgorithm))
	for _, h := range hashes {
		tree.Insert(h.Path, h.Hash, h.LayerID)
	}

	root, err := tree.Build()
	if err != nil {
		return nil, fmt.Errorf("fshash: building Merkle tree for layer %s: %w", layerID, err)
	}

	p.hooks.fireTreeBuilt(ctx, layerID, root.Hash)
	p.dedup.ResetSession()
	return root, nil
}

// Stats returns combined deduplication and cache statistics.
func (p *Processor) Stats() core.ProcessorStats {
	dedupStats := p.dedup.Stats()
	cacheStats := p.cache.Stats()
	dedupStats.CacheHits = cacheStats.Hits
	dedupStats.CacheMisses = cacheStats.Misses
	return dedupStats
}

// Close gracefully shuts down workers and drains pending events.
func (p *Processor) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(p.eventCh)
	p.wg.Wait()
	return nil
}

// Chain returns the layer chain for a given layer ID.
func (p *Processor) Chain(id core.LayerID) *layer.Chain {
	p.chainMu.RLock()
	defer p.chainMu.RUnlock()
	return p.chains[id.String()]
}

func (p *Processor) worker() {
	defer p.wg.Done()
	for req := range p.eventCh {
		err := p.processEvent(req.ctx, req.event)
		if req.errCh != nil {
			req.errCh <- err
		}
	}
}

func (p *Processor) processEvent(ctx context.Context, event core.AccessEvent) error {
	chain := p.Chain(event.LayerID)
	if chain == nil {
		return fmt.Errorf("%w: %s", core.ErrLayerNotFound, event.LayerID)
	}

	mutations := p.interpreter.Interpret(ctx, event)

	for _, m := range mutations {
		switch m.Kind {
		case overlay.MutationDeleted:
			p.MarkDeleted(m.LayerID, m.Path)
			// Trigger exclusion hook specifically for the whiteout itself
			p.hooks.fireFileExcluded(ctx, m.LayerID, event.Path)
		case overlay.MutationOpaqued:
			p.MarkOpaque(m.LayerID, m.Path)
		case overlay.MutationModified:
			// Process via Deduplicator
			event.Path = m.Path
			result := p.dedup.Process(ctx, event, chain)

			switch result.Action {
			case core.ActionCompute:
				var hash []byte
				if event.Data != nil {
					hash = p.hasher.Hash(event.Data)
				} else {
					hash = p.hasher.Hash([]byte(event.Path))
				}

				fileHash := core.FileHash{
					Path:      event.Path,
					Hash:      hash,
					Algorithm: string(p.hasher.Algorithm()),
					LayerID:   event.LayerID,
					Size:      int64(len(event.Data)),
				}

				p.dedup.RecordComputed(fileHash)
				p.hooks.fireHashComputed(ctx, fileHash)
				p.hooks.fireCacheMiss(ctx, event.LayerID, event.Path)

			case core.ActionReuse:
				p.hooks.fireCacheHit(ctx, event.LayerID, event.Path)

			case core.ActionSkip:
				// Duplicate event in session.

			case core.ActionExclude:
				p.hooks.fireFileExcluded(ctx, event.LayerID, event.Path)
			}

			p.hooks.fireAccessDeduped(ctx, result)
		}
	}

	return nil
}

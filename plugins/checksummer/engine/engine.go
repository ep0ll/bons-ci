//go:build linux

// Package engine ties together all subsystems into a single orchestrator.
//
// Subsystem dependency graph:
//
//	fanotify.Watcher
//	       │ event fd
//	       ▼
//	pipeline.Pipeline
//	  stage[0]: key-resolve  → filekey.Resolver (name_to_handle_at)
//	  stage[1]: hash         → dedup.Engine → hasher.AdaptiveHasher
//	  stage[2]: result       → ResultCallback + PostHash hooks
//	       │
//	overlay.MountInfo (consulted for BackingPath / layer enumeration)
package engine

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/cache"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/dedup"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/fanotify"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hashdb"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hasher"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/metrics"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/overlay"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/pipeline"
)

// ─────────────────────────── Callbacks ───────────────────────────────────────

// ResultCallback is called for every computed hash.
// Invoked from a worker goroutine – must be safe for concurrent use.
type ResultCallback func(key filekey.Key, path string, hash []byte, size int64)

// ErrorCallback is called for every processing error.
type ErrorCallback func(key filekey.Key, path string, err error)

// ─────────────────────────── Options ─────────────────────────────────────────

// Options is the top-level engine configuration.
type Options struct {
	Marks               []fanotify.Mark
	WatchWorkers        int
	EventBufSize        int
	SmallFileThreshold  int64
	MediumFileThreshold int64
	ParallelWorkers     int
	ParallelChunkSize   int64
	CacheMaxEntries     int
	CacheTTL            time.Duration
	DisableFileHandles  bool
	KeyResolver         *filekey.Resolver
	MountInfoPath       string
	MountInfoCacheTTL   time.Duration
	// HashDBDir is the directory for the persistent hash database.
	// Empty string disables persistent caching across process restarts.
	HashDBDir      string
	Hooks          *hooks.HookSet
	Metrics        *metrics.Recorder
	ResultCallback ResultCallback
	ErrorCallback  ErrorCallback
}

// DefaultOptions returns production-ready defaults.
func DefaultOptions() Options {
	return Options{
		WatchWorkers:        16,
		EventBufSize:        4096,
		SmallFileThreshold:  8 << 20,
		MediumFileThreshold: 128 << 20,
		ParallelWorkers:     8,
		ParallelChunkSize:   2 << 20,
		CacheMaxEntries:     1024,
		MountInfoCacheTTL:   5 * time.Second,
	}
}

func (o *Options) validate() error {
	if o.WatchWorkers <= 0 {
		o.WatchWorkers = 16
	}
	if o.SmallFileThreshold <= 0 {
		o.SmallFileThreshold = 8 << 20
	}
	if o.MediumFileThreshold <= o.SmallFileThreshold {
		o.MediumFileThreshold = o.SmallFileThreshold * 16
	}
	if o.ParallelWorkers <= 0 {
		o.ParallelWorkers = 8
	}
	if o.ParallelChunkSize <= 0 {
		o.ParallelChunkSize = 2 << 20
	}
	if o.CacheMaxEntries <= 0 {
		o.CacheMaxEntries = 1024
	}
	if o.Hooks == nil {
		o.Hooks = hooks.NewHookSet()
	}
	if o.Metrics == nil {
		o.Metrics = &metrics.Recorder{}
	}
	if o.KeyResolver == nil {
		o.KeyResolver = &filekey.Resolver{DisableHandles: o.DisableFileHandles}
	}
	return nil
}

// ─────────────────────────── Builder ─────────────────────────────────────────

// Builder is a fluent engine configurator.
type Builder struct{ opts Options }

// Build starts a Builder with DefaultOptions.
func Build() *Builder { return &Builder{opts: DefaultOptions()} }

func (b *Builder) WatchMount(path string) *Builder {
	b.opts.Marks = append(b.opts.Marks, fanotify.DefaultMark(path))
	return b
}
func (b *Builder) WithMark(m fanotify.Mark) *Builder {
	b.opts.Marks = append(b.opts.Marks, m)
	return b
}
func (b *Builder) WatchWorkers(n int) *Builder          { b.opts.WatchWorkers = n; return b }
func (b *Builder) ParallelWorkers(n int) *Builder       { b.opts.ParallelWorkers = n; return b }
func (b *Builder) ParallelChunkSize(n int64) *Builder   { b.opts.ParallelChunkSize = n; return b }
func (b *Builder) EventBufSize(n int) *Builder          { b.opts.EventBufSize = n; return b }
func (b *Builder) SmallFileThreshold(n int64) *Builder  { b.opts.SmallFileThreshold = n; return b }
func (b *Builder) MediumFileThreshold(n int64) *Builder { b.opts.MediumFileThreshold = n; return b }
func (b *Builder) CacheMaxEntries(n int) *Builder       { b.opts.CacheMaxEntries = n; return b }
func (b *Builder) CacheTTL(d time.Duration) *Builder    { b.opts.CacheTTL = d; return b }
func (b *Builder) DisableFileHandles() *Builder         { b.opts.DisableFileHandles = true; return b }
func (b *Builder) MountInfoPath(p string) *Builder      { b.opts.MountInfoPath = p; return b }
func (b *Builder) MountInfoCacheTTL(d time.Duration) *Builder {
	b.opts.MountInfoCacheTTL = d
	return b
}
func (b *Builder) HashDBDir(dir string) *Builder       { b.opts.HashDBDir = dir; return b }
func (b *Builder) OnResult(fn ResultCallback) *Builder { b.opts.ResultCallback = fn; return b }
func (b *Builder) OnError(fn ErrorCallback) *Builder   { b.opts.ErrorCallback = fn; return b }
func (b *Builder) WithHooks(hs *hooks.HookSet) *Builder {
	b.opts.Hooks = hs
	return b
}
func (b *Builder) WithMetrics(m *metrics.Recorder) *Builder { b.opts.Metrics = m; return b }
func (b *Builder) Options() Options                         { return b.opts }

// Engine builds and returns the configured Engine.
func (b *Builder) Engine() (*Engine, error) { return New(b.opts) }

// MustEngine panics on error.
func (b *Builder) MustEngine() *Engine {
	e, err := b.Engine()
	if err != nil {
		panic("engine.Builder.MustEngine: " + err.Error())
	}
	return e
}

// ─────────────────────────── Engine ──────────────────────────────────────────

// Engine is the top-level checksum computation engine.
type Engine struct {
	opts    Options
	watcher *fanotify.Watcher
	pipe    *pipeline.Pipeline
	dd      *dedup.Engine
	db      *hashdb.DB // nil when HashDBDir is empty
	h       hasher.Hasher
	keyRes  *filekey.Resolver
	mi      *overlay.MountInfo
	backing *overlay.BackingResolver
}

// New constructs an Engine.
func New(opts Options) (*Engine, error) {
	if err := opts.validate(); err != nil {
		return nil, fmt.Errorf("engine: invalid options: %w", err)
	}

	ph := hasher.NewParallelHasher(
		hasher.WithWorkers(opts.ParallelWorkers),
		hasher.WithChunkSize(opts.ParallelChunkSize),
	)
	h := hasher.NewAdaptiveHasher(
		hasher.WithSmallThreshold(opts.SmallFileThreshold),
		hasher.WithMediumThreshold(opts.MediumFileThreshold),
		hasher.WithParallelHasher(ph),
	)

	c := cache.New(
		cache.WithMaxEntries(opts.CacheMaxEntries),
		cache.WithTTL(opts.CacheTTL),
	)
	var db *hashdb.DB
	if opts.HashDBDir != "" {
		var dbErr error
		db, dbErr = hashdb.Open(opts.HashDBDir)
		if dbErr != nil {
			return nil, fmt.Errorf("engine: open hashdb: %w", dbErr)
		}
	}
	dd := dedup.New(
		dedup.WithCache(c),
		dedup.WithHashDB(db),
		dedup.WithHooks(opts.Hooks),
		dedup.WithMetrics(opts.Metrics),
	)
	mi := overlay.NewMountInfo(opts.MountInfoPath, opts.MountInfoCacheTTL)

	e := &Engine{
		opts:    opts,
		dd:      dd,
		db:      db,
		h:       h,
		keyRes:  opts.KeyResolver,
		mi:      mi,
		backing: overlay.NewBackingResolver(mi),
	}
	e.pipe = e.buildPipeline()
	return e, nil
}

// Start begins watching and blocks until ctx is cancelled.
func (e *Engine) Start(ctx context.Context) error {
	e.pipe.Start(ctx)

	watcher, err := fanotify.New(fanotify.Options{
		Marks:        e.opts.Marks,
		Workers:      e.opts.WatchWorkers,
		EventBufSize: e.opts.EventBufSize,
		Hooks:        e.opts.Hooks,
		Metrics:      e.opts.Metrics,
		Handler:      e.handleEvent,
	})
	if err != nil {
		e.pipe.Stop()
		return fmt.Errorf("engine: create watcher: %w", err)
	}
	e.watcher = watcher

	if err := watcher.Start(ctx); err != nil {
		e.pipe.Stop()
		return fmt.Errorf("engine: start watcher: %w", err)
	}

	<-ctx.Done()
	watcher.Stop()
	e.pipe.Stop()
	return nil
}

func (e *Engine) handleEvent(ctx context.Context, evt *fanotify.Event) {
	e.pipe.Dispatch(&pipeline.Item{Event: evt, EnqueuedAt: time.Now()}, true)
}

func (e *Engine) buildPipeline() *pipeline.Pipeline {
	p := pipeline.New(
		pipeline.WithHooks(e.opts.Hooks),
		pipeline.WithMetrics(e.opts.Metrics),
	)
	p.Append(pipeline.NewStage("key-resolve", e.stageKeyResolve, e.opts.WatchWorkers, 1024))
	p.Append(pipeline.NewStage("hash", e.stageHash, e.opts.WatchWorkers, 512))
	p.Append(pipeline.NewStage("result", e.stageResult, 4, 256))
	return p
}

// ─────────────────────────── Stages ──────────────────────────────────────────

func (e *Engine) stageKeyResolve(ctx context.Context, item *pipeline.Item) error {
	if item.Event == nil || item.Event.Fd <= 0 {
		return fmt.Errorf("key-resolve: nil or closed event fd")
	}
	_ = e.opts.Hooks.OnLayerResolve.Execute(ctx, hooks.LayerPayload{
		BackingFd: item.Event.Fd,
	}, hooks.ContinueOnError)

	key, err := e.keyRes.FromFD(item.Event.Fd)
	if err != nil {
		e.opts.Metrics.LayerFallbacks.Inc()
		return fmt.Errorf("key-resolve fd=%d: %w", item.Event.Fd, err)
	}
	e.opts.Metrics.LayerResolutions.Inc()
	item.Key = key

	if path, perr := item.Event.ResolvePath(); perr == nil {
		item.Path = path
	}
	var st unix.Stat_t
	if serr := unix.Fstat(item.Event.Fd, &st); serr == nil {
		item.FileSize = st.Size
		item.MtimeNs = st.Mtim.Sec*1e9 + st.Mtim.Nsec
	}
	return nil
}

func (e *Engine) stageHash(ctx context.Context, item *pipeline.Item) error {
	if item.Key.IsZero() {
		return fmt.Errorf("hash: zero key for path %q", item.Path)
	}
	eventFd := item.Event.Fd
	size := item.FileSize
	mtimeNs := item.MtimeNs // captured at key-resolve stage from the same stat

	hashFn := func(ctx context.Context, key filekey.Key) ([]byte, error) {
		start := time.Now()
		hash, err := e.h.HashFD(ctx, eventFd, size)
		if err != nil {
			return nil, fmt.Errorf("hash fd=%d key=%s: %w", eventFd, key, err)
		}
		e.opts.Metrics.BytesHashed.Add(size)
		e.opts.Metrics.HashLatency.Record(time.Since(start))
		return hash, nil
	}

	// statFn serves the mtime/size we already have from the key-resolve stat,
	// so the persistent cache can be validated without an extra syscall.
	statFn := func(_ filekey.Key) (int64, int64, error) {
		return mtimeNs, size, nil
	}

	res, err := e.dd.Compute(ctx, item.Key, hashFn, statFn)
	if err != nil {
		e.opts.Metrics.HashErrors.Inc()
		if e.opts.ErrorCallback != nil {
			e.opts.ErrorCallback(item.Key, item.Path, err)
		}
		return err
	}
	item.Hash = res.Hash
	return nil
}

// statFnForPath returns a StatFunc that calls unix.Stat on path.
// Zero-allocation for the common case where stat is cheap.
func (e *Engine) statFnForPath(path string) dedup.StatFunc {
	return func(_ filekey.Key) (int64, int64, error) {
		var st unix.Stat_t
		if err := unix.Stat(path, &st); err != nil {
			return 0, 0, err
		}
		return st.Mtim.Sec*1e9 + st.Mtim.Nsec, st.Size, nil
	}
}

// statFnForFD returns a StatFunc that calls unix.Fstat on fd.
func (e *Engine) statFnForFD(fd int) dedup.StatFunc {
	return func(_ filekey.Key) (int64, int64, error) {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return 0, 0, err
		}
		return st.Mtim.Sec*1e9 + st.Mtim.Nsec, st.Size, nil
	}
}

func (e *Engine) stageResult(ctx context.Context, item *pipeline.Item) error {
	if item.Hash == nil {
		return nil
	}
	if e.opts.ResultCallback != nil {
		e.opts.ResultCallback(item.Key, item.Path, item.Hash, item.FileSize)
	}
	_ = e.opts.Hooks.PostHash.Execute(ctx, hooks.HashPayload{
		Path:     item.Path,
		Key:      item.Key.String(),
		FileSize: item.FileSize,
		Hash:     item.Hash,
		Duration: time.Since(item.EnqueuedAt),
	}, hooks.ContinueOnError)
	return nil
}

// ─────────────────────────── Direct APIs ─────────────────────────────────────

// HashPath computes and deduplicates the hash for path.
func (e *Engine) HashPath(ctx context.Context, path string) ([]byte, error) {
	key, err := e.keyRes.FromPath(path)
	if err != nil {
		return nil, fmt.Errorf("engine.HashPath key %q: %w", path, err)
	}
	res, err := e.dd.Compute(ctx, key, func(ctx context.Context, k filekey.Key) ([]byte, error) {
		return e.h.HashFile(ctx, path)
	}, e.statFnForPath(path))
	if err != nil {
		return nil, err
	}
	return res.Hash, nil
}

// HashFD computes and deduplicates the hash for an open fd.
func (e *Engine) HashFD(ctx context.Context, fd int, size int64) ([]byte, error) {
	key, err := e.keyRes.FromFD(fd)
	if err != nil {
		return nil, fmt.Errorf("engine.HashFD key fd=%d: %w", fd, err)
	}
	res, err := e.dd.Compute(ctx, key, func(ctx context.Context, k filekey.Key) ([]byte, error) {
		return e.h.HashFD(ctx, fd, size)
	}, e.statFnForFD(fd))
	if err != nil {
		return nil, err
	}
	return res.Hash, nil
}

// Invalidate removes the cached hash for a path.
func (e *Engine) Invalidate(path string) error {
	key, err := e.keyRes.FromPath(path)
	if err != nil {
		return err
	}
	e.dd.Invalidate(key)
	return nil
}

// InvalidateAll clears the entire result cache.
func (e *Engine) InvalidateAll() { e.dd.InvalidateAll() }

// FlushDB persists all pending hash results to the on-disk hashdb.
// Call periodically or on graceful shutdown. No-op when HashDBDir is empty.
func (e *Engine) FlushDB() error { return e.dd.Flush() }

// AddMark adds a fanotify mark while the engine is running.
func (e *Engine) AddMark(m fanotify.Mark) error {
	if e.watcher == nil {
		return fmt.Errorf("engine: not started")
	}
	return e.watcher.AddMark(m)
}

// RemoveMark removes a fanotify mark.
func (e *Engine) RemoveMark(m fanotify.Mark) error {
	if e.watcher == nil {
		return fmt.Errorf("engine: not started")
	}
	return e.watcher.RemoveMark(m)
}

// ─────────────────────────── Observability ───────────────────────────────────

func (e *Engine) Metrics() *metrics.Recorder                    { return e.opts.Metrics }
func (e *Engine) MetricsSnapshot() metrics.Snapshot             { return e.opts.Metrics.Snapshot() }
func (e *Engine) Hooks() *hooks.HookSet                         { return e.opts.Hooks }
func (e *Engine) PipelineStats() []pipeline.StageSnapshot       { return e.pipe.StageStats() }
func (e *Engine) CacheStats() dedup.CacheStats                  { return e.dd.CacheStats() }
func (e *Engine) OverlayMounts() ([]*overlay.MountEntry, error) { return e.mi.OverlayMounts() }
func (e *Engine) BackingPath(p string) (string, bool, error)    { return e.backing.BackingPath(p) }

package fanwatch

// ─────────────────────────────────────────────────────────────────────────────
// PipelineOption — functional options for Pipeline
// ─────────────────────────────────────────────────────────────────────────────

// PipelineOption configures a [Pipeline] at construction time.
type PipelineOption func(*Pipeline)

// WithFilter appends f to the pipeline's filter chain.
// All filters are evaluated in registration order; the event is dropped if any
// filter rejects it.
func WithFilter(f Filter) PipelineOption {
	return func(p *Pipeline) {
		p.filters = append(p.filters, f)
	}
}

// WithFilters appends multiple filters at once.
func WithFilters(filters ...Filter) PipelineOption {
	return func(p *Pipeline) {
		p.filters = append(p.filters, filters...)
	}
}

// WithTransformer appends t to the pipeline's transformer chain.
// Transformers run in registration order on every event that passes filters.
func WithTransformer(t Transformer) PipelineOption {
	return func(p *Pipeline) {
		p.transformers = append(p.transformers, t)
	}
}

// WithTransformers appends multiple transformers at once.
func WithTransformers(ts ...Transformer) PipelineOption {
	return func(p *Pipeline) {
		p.transformers = append(p.transformers, ts...)
	}
}

// WithHandler sets the terminal handler for the pipeline.
// Only one handler may be set; use [ChainHandler] or [MultiHandler] to
// compose multiple handlers into one.
func WithHandler(h Handler) PipelineOption {
	return func(p *Pipeline) {
		p.handler = h
	}
}

// WithMiddleware registers a middleware that wraps the pipeline's handler.
// Middlewares are applied after WithHandler; multiple middlewares stack in
// registration order (last registered = outermost wrapper).
func WithMiddleware(m Middleware) PipelineOption {
	return func(p *Pipeline) {
		p.middlewares = append(p.middlewares, m)
	}
}

// WithWorkers sets the number of goroutines used to process events concurrently.
// Defaults to runtime.NumCPU(). A value of 1 gives strictly sequential processing.
func WithWorkers(n int) PipelineOption {
	return func(p *Pipeline) {
		if n > 0 {
			p.cfg.workers = n
		}
	}
}

// WithErrorBufferSize sets the buffer depth of the pipeline's error channel.
// A larger buffer reduces the chance of dropping non-fatal errors under burst load.
func WithErrorBufferSize(n int) PipelineOption {
	return func(p *Pipeline) {
		if n > 0 {
			p.cfg.errBufSize = n
		}
	}
}

// WithReadOnlyPipeline is a convenience option that pre-configures the pipeline
// with [ReadOnlyFilter] and [NoOverflowFilter], establishing the canonical
// read-observer configuration.
func WithReadOnlyPipeline() PipelineOption {
	return func(p *Pipeline) {
		p.filters = append(p.filters, ReadOnlyFilter(), NoOverflowFilter())
	}
}

// WithOverlayEnrichment is a convenience option that adds [NewOverlayEnricher]
// and [FileStatTransformer] to the transformer chain.
func WithOverlayEnrichment(overlay *OverlayInfo) PipelineOption {
	return func(p *Pipeline) {
		p.transformers = append(p.transformers,
			NewOverlayEnricher(overlay),
			FileStatTransformer(),
		)
	}
}

// WithProcessEnrichment is a convenience option that adds [ProcessEnricher]
// to the transformer chain.
func WithProcessEnrichment() PipelineOption {
	return func(p *Pipeline) {
		p.transformers = append(p.transformers, ProcessEnricher())
	}
}

// WithFullEnrichment is a convenience option combining overlay, process, and
// stat enrichment — the most complete out-of-the-box event decoration.
func WithFullEnrichment(overlay *OverlayInfo) PipelineOption {
	return func(p *Pipeline) {
		p.transformers = append(p.transformers,
			NewOverlayEnricher(overlay),
			ProcessEnricher(),
			FileStatTransformer(),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WatcherOption — functional options for Watcher
// ─────────────────────────────────────────────────────────────────────────────

// WatcherOption configures a [Watcher] at construction time.
type WatcherOption func(*watcherConfig)

// watcherConfig holds Watcher parameters independent of platform.
type watcherConfig struct {
	mergedDir   string
	mask        EventMask
	overlay     *OverlayInfo
	bufSize     int    // raw event channel buffer
	readBufSize int    // fanotify read buffer in bytes
	watcherID   string // identifies this watcher in multi-watcher setups
}

func defaultWatcherConfig() watcherConfig {
	return watcherConfig{
		mask:        MaskReadOnly,
		bufSize:     512,
		readBufSize: 4096,
	}
}

// WithMergedDir sets the overlay merged directory to watch.
// This is the only required option; all others have sensible defaults.
func WithMergedDir(dir string) WatcherOption {
	return func(c *watcherConfig) { c.mergedDir = dir }
}

// WithMask sets the fanotify event mask. Defaults to [MaskReadOnly].
// Use [MaskAll] to observe modifications too, but be aware that write-heavy
// workloads may overwhelm the event queue.
func WithMask(mask EventMask) WatcherOption {
	return func(c *watcherConfig) { c.mask = mask }
}

// WithOverlay attaches overlay info to the watcher configuration.
// The overlay info is used by the watcher to validate that the mergedDir
// matches a known overlay mount.
func WithOverlay(o *OverlayInfo) WatcherOption {
	return func(c *watcherConfig) {
		c.overlay = o
		if c.mergedDir == "" {
			c.mergedDir = o.MergedDir
		}
	}
}

// WithEventBufferSize sets the buffer depth of the raw event output channel.
// Larger buffers absorb burst traffic at the cost of higher memory use.
func WithEventBufferSize(n int) WatcherOption {
	return func(c *watcherConfig) {
		if n > 0 {
			c.bufSize = n
		}
	}
}

// WithReadBufferSize sets the byte size of the kernel-read buffer.
// Must be at least the size of one fanotify_event_metadata struct (24 bytes).
// Larger values reduce syscall frequency at the cost of latency.
func WithReadBufferSize(n int) WatcherOption {
	return func(c *watcherConfig) {
		if n >= 4096 {
			c.readBufSize = n
		}
	}
}

// WithWatcherID attaches an identifier to every event produced by this watcher.
// Useful when multiple watchers feed a single pipeline.
func WithWatcherID(id string) WatcherOption {
	return func(c *watcherConfig) { c.watcherID = id }
}

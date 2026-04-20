package reactdag

// ---------------------------------------------------------------------------
// Scheduler functional options
// ---------------------------------------------------------------------------

// schedulerConfig holds all tunable Scheduler parameters.
type schedulerConfig struct {
	workerCount int
	fastCache   CacheStore
	slowCache   CacheStore
	keyComputer CacheKeyComputer
	executor    Executor
	hooks       *HookRegistry
	eventBus    *EventBus
}

func defaultConfig() schedulerConfig {
	return schedulerConfig{
		workerCount: 8,
		fastCache:   NewMemoryCacheStore(0),
		slowCache:   NoopCacheStore{},
		keyComputer: DefaultKeyComputer{},
		hooks:       NewHookRegistry(),
		eventBus:    NewEventBus(),
	}
}

// Option is a functional option for Scheduler construction.
type Option func(*schedulerConfig)

// WithWorkerCount sets the number of concurrent vertex workers. Default: 8.
func WithWorkerCount(n int) Option {
	return func(c *schedulerConfig) {
		if n > 0 {
			c.workerCount = n
		}
	}
}

// WithFastCache sets the fast (local/memory) cache tier.
func WithFastCache(s CacheStore) Option {
	return func(c *schedulerConfig) { c.fastCache = s }
}

// WithSlowCache sets the slow (remote/persistent) cache tier.
func WithSlowCache(s CacheStore) Option {
	return func(c *schedulerConfig) { c.slowCache = s }
}

// WithKeyComputer overrides the CacheKey derivation algorithm.
func WithKeyComputer(kc CacheKeyComputer) Option {
	return func(c *schedulerConfig) { c.keyComputer = kc }
}

// WithExecutor overrides the vertex Executor.
func WithExecutor(e Executor) Option {
	return func(c *schedulerConfig) { c.executor = e }
}

// WithHooks injects a pre-configured HookRegistry.
func WithHooks(h *HookRegistry) Option {
	return func(c *schedulerConfig) { c.hooks = h }
}

// WithEventBus injects a pre-configured EventBus.
func WithEventBus(b *EventBus) Option {
	return func(c *schedulerConfig) { c.eventBus = b }
}

// WithFileTracker injects a FileTracker used by the default Executor.
// Ignored when WithExecutor is also provided.
func WithFileTracker(t FileTracker) Option {
	return func(c *schedulerConfig) {
		if c.executor == nil {
			c.executor = newDefaultExecutor(t)
		}
	}
}

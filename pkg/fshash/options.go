package fshash

import "github.com/bons/bons-ci/pkg/fshash/chunk"

// Option configures a [Processor] instance via functional options.
type Option func(*processorConfig)

type processorConfig struct {
	hashAlgorithm      chunk.Algorithm
	cacheMaxEntries    int
	cacheShards        int
	bloomFPRate        float64
	bloomExpectedItems uint
	workerCount        int
	channelBuffer      int
	bufferPoolSize     int
	hooks              Hooks
}

func defaultConfig() processorConfig {
	return processorConfig{
		hashAlgorithm:      chunk.BLAKE3,
		cacheMaxEntries:    1 << 16,
		cacheShards:        16,
		bloomFPRate:        0.001,
		bloomExpectedItems: 1 << 16,
		workerCount:        4,
		channelBuffer:      4096,
		bufferPoolSize:     32 * 1024,
	}
}

// WithHashAlgorithm sets the hashing algorithm. Default: BLAKE3.
func WithHashAlgorithm(algo chunk.Algorithm) Option {
	return func(c *processorConfig) { c.hashAlgorithm = algo }
}

// WithCacheSize sets the max cache entries. Default: 65536.
func WithCacheSize(maxEntries int) Option {
	return func(c *processorConfig) {
		if maxEntries > 0 {
			c.cacheMaxEntries = maxEntries
		}
	}
}

// WithCacheShards sets shard count. Default: 16.
func WithCacheShards(n int) Option {
	return func(c *processorConfig) {
		if n > 0 {
			c.cacheShards = n
		}
	}
}

// WithBloomFilter configures the bloom filter.
func WithBloomFilter(expectedItems uint, fpRate float64) Option {
	return func(c *processorConfig) {
		if expectedItems > 0 {
			c.bloomExpectedItems = expectedItems
		}
		if fpRate > 0 && fpRate < 1 {
			c.bloomFPRate = fpRate
		}
	}
}

// WithWorkerCount sets concurrent hash workers. Default: 4.
func WithWorkerCount(n int) Option {
	return func(c *processorConfig) {
		if n > 0 {
			c.workerCount = n
		}
	}
}

// WithChannelBuffer sets the event channel buffer. Default: 4096.
func WithChannelBuffer(size int) Option {
	return func(c *processorConfig) {
		if size > 0 {
			c.channelBuffer = size
		}
	}
}

// WithBufferPoolSize sets the buffer pool chunk size. Default: 32KB.
func WithBufferPoolSize(bytes int) Option {
	return func(c *processorConfig) {
		if bytes > 0 {
			c.bufferPoolSize = bytes
		}
	}
}

// WithHooks registers lifecycle hooks.
func WithHooks(hooks Hooks) Option {
	return func(c *processorConfig) { c.hooks = hooks }
}

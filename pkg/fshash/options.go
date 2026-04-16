package fshash

import (
	"fmt"
	"runtime"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// Options configures a Checksummer. Use the With* helpers; do not construct directly.
type Options struct {
	Hasher         core.Hasher
	Walker         Walker
	Filter         Filter
	Meta           core.MetaFlag
	Workers        int
	CollectEntries bool
	FollowSymlinks bool
	FileCache      FileCache
	SizeLimit      int64
	Pool           core.BufPool // nil → core.DefaultPool

	// metaSet prevents applyDefaults from overriding an explicit MetaNone.
	metaSet bool
}

// Option is a functional option for New.
type Option func(*Options) error

// WithAlgorithm sets the hash algorithm by name.
func WithAlgorithm(algo core.Algorithm) Option {
	return func(o *Options) error {
		h, err := core.NewHasher(algo)
		if err != nil {
			return err
		}
		o.Hasher = h
		return nil
	}
}

// WithHasher sets a custom Hasher (e.g. registered in a custom registry).
func WithHasher(h core.Hasher) Option {
	return func(o *Options) error {
		if h == nil {
			return fmt.Errorf("fshash: Hasher must not be nil")
		}
		o.Hasher = h
		return nil
	}
}

// WithWalker sets a custom Walker.
func WithWalker(w Walker) Option {
	return func(o *Options) error {
		if w == nil {
			return fmt.Errorf("fshash: Walker must not be nil")
		}
		o.Walker = w
		return nil
	}
}

// WithFilter sets the entry filter.
func WithFilter(f Filter) Option {
	return func(o *Options) error { o.Filter = f; return nil }
}

// WithMetadata sets the metadata flags. MetaNone is valid and respected.
func WithMetadata(flags core.MetaFlag) Option {
	return func(o *Options) error {
		o.Meta = flags
		o.metaSet = true
		return nil
	}
}

// WithWorkers sets the parallel worker count (≤ 0 → NumCPU, capped at 64).
func WithWorkers(n int) Option {
	return func(o *Options) error { o.Workers = n; return nil }
}

// WithCollectEntries enables per-entry result collection in Result.Entries.
func WithCollectEntries(v bool) Option {
	return func(o *Options) error { o.CollectEntries = v; return nil }
}

// WithFollowSymlinks enables symlink resolution (with cycle detection).
func WithFollowSymlinks(v bool) Option {
	return func(o *Options) error { o.FollowSymlinks = v; return nil }
}

// WithFileCache sets a FileCache to short-circuit repeated file hashing.
func WithFileCache(c FileCache) Option {
	return func(o *Options) error { o.FileCache = c; return nil }
}

// WithSizeLimit causes Sum to return FileTooLargeError for files exceeding
// limit bytes. 0 disables the check.
func WithSizeLimit(limit int64) Option {
	return func(o *Options) error {
		if limit < 0 {
			return fmt.Errorf("fshash: SizeLimit must be >= 0, got %d", limit)
		}
		o.SizeLimit = limit
		return nil
	}
}

// WithPool sets the buffer pool (nil → core.DefaultPool).
func WithPool(p core.BufPool) Option {
	return func(o *Options) error { o.Pool = p; return nil }
}

// applyDefaults fills zero-value fields with sensible defaults.
func applyDefaults(o *Options) {
	if o.Hasher == nil {
		o.Hasher = core.MustHasher(core.SHA256)
	}
	if o.Walker == nil {
		o.Walker = OSWalker{}
	}
	if o.Filter == nil {
		o.Filter = noopFilter{}
	}
	if !o.metaSet {
		o.Meta = core.MetaModeAndSize
	}
	if o.Workers <= 0 {
		o.Workers = runtime.NumCPU()
	}
	if o.Workers > 64 {
		o.Workers = 64
	}
	if o.Pool == nil {
		o.Pool = core.DefaultPool
	}
}

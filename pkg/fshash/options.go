package fshash

import (
	"fmt"
	"runtime"
)

// Options configures a [Checksummer].  Use the With* helpers to build one
// instead of constructing it directly.
type Options struct {
	// Hasher selects the hash algorithm.  Defaults to SHA-256.
	Hasher Hasher

	// Walker controls how the filesystem is traversed.  Defaults to
	// [OSWalker], which calls os.ReadDir and os.Lstat.
	Walker Walker

	// Filter decides which entries to include or skip.
	// Defaults to [AllowAll].
	Filter Filter

	// Meta selects which metadata fields are mixed into each file's digest.
	// Defaults to [MetaModeAndSize].
	Meta MetaFlag

	// Workers is the number of goroutines used to hash files in parallel.
	// A value ≤ 0 uses runtime.NumCPU().  Set to 1 to disable parallelism.
	Workers int

	// CollectEntries, when true, populates [Result.Entries] with per-entry
	// results.  Adds a small overhead.
	CollectEntries bool

	// FollowSymlinks, when true, follows symbolic links.  Cycles are
	// detected and result in an error.
	FollowSymlinks bool

	// FileCache, when non-nil, is consulted before hashing each file and
	// updated after.  Directory digests are never cached here (they are
	// recomputed from already-cached children and are therefore cheap).
	// Use [NewCachingChecksummer] as a convenience constructor.
	FileCache FileCache

	// SizeLimit, when > 0, causes hashFile to return a [FileTooLargeError]
	// instead of reading any file whose size exceeds this value.  Set to 0
	// to disable the limit (default).
	SizeLimit int64

	// metaSet is true when WithMetadata was called explicitly.  It lets
	// applyDefaults distinguish MetaNone (intentional) from zero (unset).
	metaSet bool
}

// Option is a functional option for [New].
type Option func(*Options) error

// WithAlgorithm sets the hash algorithm by name.
func WithAlgorithm(algo Algorithm) Option {
	return func(o *Options) error {
		h, err := NewHasher(algo)
		if err != nil {
			return err
		}
		o.Hasher = h
		return nil
	}
}

// WithHasher sets a custom [Hasher].
func WithHasher(h Hasher) Option {
	return func(o *Options) error {
		if h == nil {
			return fmt.Errorf("fshash: Hasher must not be nil")
		}
		o.Hasher = h
		return nil
	}
}

// WithWalker sets a custom [Walker].
func WithWalker(w Walker) Option {
	return func(o *Options) error {
		if w == nil {
			return fmt.Errorf("fshash: Walker must not be nil")
		}
		o.Walker = w
		return nil
	}
}

// WithFilter sets the [Filter] used to include/exclude entries.
func WithFilter(f Filter) Option {
	return func(o *Options) error {
		o.Filter = f
		return nil
	}
}

// WithMetadata selects which metadata fields are mixed into file digests.
// MetaNone is a valid value and is respected even though its numeric value
// is zero.
func WithMetadata(flags MetaFlag) Option {
	return func(o *Options) error {
		o.Meta = flags
		o.metaSet = true
		return nil
	}
}

// WithWorkers sets the number of parallel hashing workers.
// A value ≤ 0 uses runtime.NumCPU().
func WithWorkers(n int) Option {
	return func(o *Options) error {
		o.Workers = n
		return nil
	}
}

// WithCollectEntries enables per-entry result collection.
func WithCollectEntries(v bool) Option {
	return func(o *Options) error {
		o.CollectEntries = v
		return nil
	}
}

// WithFollowSymlinks controls symlink resolution.
func WithFollowSymlinks(v bool) Option {
	return func(o *Options) error {
		o.FollowSymlinks = v
		return nil
	}
}

// WithFileCache sets a [FileCache] that short-circuits file hashing on hits.
func WithFileCache(c FileCache) Option {
	return func(o *Options) error {
		o.FileCache = c
		return nil
	}
}

// WithSizeLimit causes Sum to return a [FileTooLargeError] for any file
// whose size exceeds limit bytes.  A limit of 0 disables the check.
func WithSizeLimit(limit int64) Option {
	return func(o *Options) error {
		if limit < 0 {
			return fmt.Errorf("fshash: SizeLimit must be >= 0, got %d", limit)
		}
		o.SizeLimit = limit
		return nil
	}
}

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(o *Options) {
	if o.Hasher == nil {
		o.Hasher = mustHasher(SHA256)
	}
	if o.Walker == nil {
		o.Walker = OSWalker{}
	}
	if o.Filter == nil {
		o.Filter = noopFilter{}
	}
	if !o.metaSet {
		o.Meta = MetaModeAndSize
	}
	if o.Workers <= 0 {
		o.Workers = runtime.NumCPU()
	}
	if o.Workers > 64 {
		// Cap to avoid over-subscription on massive machines.
		o.Workers = 64
	}
}

package dirprune

// options.go – functional options for [Pruner].
//
// Options are split into three orthogonal groups:
//
//   Filter options   — which paths to KEEP (everything else is deleted)
//   Batcher options  — how deletions are dispatched to the kernel
//   Pruner options   — concurrency and observability

import (
	dirsync "github.com/bons/bons-ci/internal/dirsync"
)

// Option is a functional option that configures a [Pruner].
type Option func(*config)

// config is the fully resolved configuration produced from a slice of Options.
type config struct {
	// ── Filter fields ────────────────────────────────────────────────────
	includePatterns []string
	excludePatterns []string
	requiredPaths   []string
	allowWildcards  bool
	followSymlinks  bool

	// ── Batcher fields ───────────────────────────────────────────────────
	// batcherFactory is called once per Prune to build the Batcher that
	// receives deletion ops. Defaults to dirsync.NewBestBatcher.
	batcherFactory func(dirsync.MergedView) (dirsync.Batcher, error)

	// ── Execution fields ─────────────────────────────────────────────────
	workers  int      // concurrent Batcher.Submit goroutines
	observer Observer // receives one event per entry decision
}

func defaultConfig() config {
	return config{
		batcherFactory: dirsync.NewBestBatcher,
		workers:        4,
		observer:       NoopObserver{},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Filter options
// ─────────────────────────────────────────────────────────────────────────────

// WithIncludePatterns registers patterns for paths that should be KEPT.
// Any entry that does not match at least one include pattern (and is not
// explicitly excluded) is deleted from the target directory.
//
// An empty include list means "keep everything" (default).
//
// Pattern syntax:
//
//	AllowWildcards == false (default): exact prefix match.
//	    "src" keeps "src", "src/main.go", "src/pkg/util.go"
//
//	AllowWildcards == true: filepath.Match glob applied to the full path
//	and to the base name independently.
//	    "*.go"  keeps "main.go", "pkg/main.go"
//	    "src/*" keeps "src/main.go"
//
// Multiple calls append to the include list; they do not replace it.
func WithIncludePatterns(patterns ...string) Option {
	return func(c *config) {
		c.includePatterns = append(c.includePatterns, patterns...)
	}
}

// WithExcludePatterns registers patterns for paths that must be DELETED even
// if they would otherwise match an include pattern. Exclusions always win.
//
// Multiple calls append to the exclude list.
func WithExcludePatterns(patterns ...string) Option {
	return func(c *config) {
		c.excludePatterns = append(c.excludePatterns, patterns...)
	}
}

// WithRequiredPaths declares paths (relative to the target directory) that
// must exist before any deletions begin. [Pruner.Prune] returns an error
// listing every absent required path without performing any deletions.
//
// This is a pre-flight safety gate: if critical paths are missing the
// directory may be in an unexpected state and deletion could be dangerous.
func WithRequiredPaths(paths ...string) Option {
	return func(c *config) {
		c.requiredPaths = append(c.requiredPaths, paths...)
	}
}

// WithAllowWildcards enables filepath.Match glob syntax in include/exclude
// patterns. Disabled by default to avoid accidental misinterpretation of
// literal bracket and question-mark characters in path names.
func WithAllowWildcards(v bool) Option {
	return func(c *config) { c.allowWildcards = v }
}

// WithFollowSymlinks instructs the walker to dereference symbolic links when
// deciding whether an entry is a directory. When false (default), symlinks
// are treated as opaque leaf entries and are never traversed.
func WithFollowSymlinks(v bool) Option {
	return func(c *config) { c.followSymlinks = v }
}

// ─────────────────────────────────────────────────────────────────────────────
// Batcher options
// ─────────────────────────────────────────────────────────────────────────────

// WithBatcher injects a custom [dirsync.Batcher] factory.
//
// The factory is called once per [Pruner.Prune] invocation, after the
// [dirsync.FSMergedView] for the target directory is constructed. The default
// factory is [dirsync.NewBestBatcher]: io_uring on Linux 5.11+, goroutine
// pool elsewhere.
//
// Inject a [dirsync.RecordingBatcher] for assertion-based tests:
//
//	rb := &dirsync.RecordingBatcher{}
//	p := dirprune.New(dirprune.WithBatcher(func(v dirsync.MergedView) (dirsync.Batcher, error) {
//	    return rb, nil
//	}))
func WithBatcher(factory func(dirsync.MergedView) (dirsync.Batcher, error)) Option {
	return func(c *config) {
		if factory != nil {
			c.batcherFactory = factory
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Execution options
// ─────────────────────────────────────────────────────────────────────────────

// WithWorkers sets the number of goroutines that concurrently call
// [dirsync.Batcher.Submit]. Default: 4.
//
// For io_uring backends the submit call is non-blocking (ops are queued in
// shared memory), so higher worker counts increase throughput on large trees.
// For synchronous backends (GoroutineBatcher) increasing workers beyond
// the storage device's I/O parallelism provides diminishing returns.
func WithWorkers(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.workers = n
		}
	}
}

// WithObserver registers an [Observer] that receives one event per path
// decision (kept or deleted). Default: [NoopObserver].
func WithObserver(o Observer) Option {
	return func(c *config) {
		if o != nil {
			c.observer = o
		}
	}
}

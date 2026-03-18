package diffview

// options.go – functional options for DiffView.
//
// Options are split into three groups mirroring the dirsync components they
// configure:
//
//   1. ClassifierOptions  – control which paths are walked and compared
//   2. HashOptions        – control the parallel content-hashing pipeline
//   3. ExecutionOptions   – control concurrency, batching, and observability
//
// Each group can be extended independently without affecting the others.

import (
	dirsync "github.com/bons/bons-ci/internal/dirsync"
)

// Option is a functional option for DiffView.
type Option func(*config)

// config is the internal configuration resolved from all applied options.
type config struct {
	// Classifier options forwarded to dirsync.NewClassifier.
	classifierOpts []dirsync.ClassifierOption

	// HashPipeline options forwarded to dirsync.NewHashPipeline.
	hashOpts []dirsync.HashPipelineOption

	// Pipeline execution options forwarded to dirsync.NewPipeline.
	pipelineOpts []dirsync.PipelineOption

	// observer receives one event per DiffEntry.
	observer Observer

	// viewFactory builds the MergedView; nil → FSMergedView.
	viewFactory func(merged string) (dirsync.MergedView, error)

	// batcherFactory builds the Batcher; nil → NewBestBatcher.
	batcherFactory func(view dirsync.MergedView) (dirsync.Batcher, error)
}

func defaultConfig() config {
	return config{
		observer:       NoopObserver{},
		viewFactory:    dirsync.NewFSMergedView,
		batcherFactory: dirsync.NewBestBatcher,
	}
}

// ─── Classifier options ───────────────────────────────────────────────────────

// WithFollowSymlinks instructs the classifier to follow symlinks when
// determining whether a filesystem entry is a directory.
func WithFollowSymlinks(v bool) Option {
	return func(c *config) {
		c.classifierOpts = append(c.classifierOpts, dirsync.WithFollowSymlinks(v))
	}
}

// WithAllowWildcards enables filepath.Match glob syntax in include/exclude
// patterns. Disabled by default to avoid accidental misinterpretation of
// literal bracket and question-mark characters.
func WithAllowWildcards(v bool) Option {
	return func(c *config) {
		c.classifierOpts = append(c.classifierOpts, dirsync.WithAllowWildcards(v))
	}
}

// WithIncludePatterns restricts the walk to paths satisfying at least one
// pattern. Empty means include everything.
func WithIncludePatterns(patterns ...string) Option {
	return func(c *config) {
		c.classifierOpts = append(c.classifierOpts, dirsync.WithIncludePatterns(patterns...))
	}
}

// WithExcludePatterns excludes paths matching any of the given patterns.
// Exclusions take precedence over inclusions.
func WithExcludePatterns(patterns ...string) Option {
	return func(c *config) {
		c.classifierOpts = append(c.classifierOpts, dirsync.WithExcludePatterns(patterns...))
	}
}

// WithRequiredPaths registers paths that must exist in lower.
// Apply returns an error if any required path is absent.
func WithRequiredPaths(paths ...string) Option {
	return func(c *config) {
		c.classifierOpts = append(c.classifierOpts, dirsync.WithRequiredPaths(paths...))
	}
}

// ─── Hash pipeline options ────────────────────────────────────────────────────

// WithHashWorkers sets the maximum number of concurrent SHA-256 goroutines.
// Defaults to runtime.NumCPU().
func WithHashWorkers(n int) Option {
	return func(c *config) {
		c.hashOpts = append(c.hashOpts, dirsync.WithHashWorkers(n))
	}
}

// WithHasher replaces the default TwoPhaseHasher with a custom ContentHasher.
// Useful for injecting BLAKE3, xxhash, or a deterministic test double.
func WithHasher(h dirsync.ContentHasher) Option {
	return func(c *config) {
		c.hashOpts = append(c.hashOpts, dirsync.WithHasher(h))
	}
}

// WithBufPool sets the pooled I/O buffer source for the hash pipeline.
// Nil uses the dirsync package's shared 64 KiB pool.
func WithBufPool(bp *dirsync.BufPool) Option {
	return func(c *config) {
		c.hashOpts = append(c.hashOpts, dirsync.WithBufPool(bp))
	}
}

// WithHashPool sets the pooled hash.Hash source for the hash pipeline.
// Nil uses the dirsync package's shared SHA-256 pool.
func WithHashPool(hp *dirsync.HashPool) Option {
	return func(c *config) {
		c.hashOpts = append(c.hashOpts, dirsync.WithHashPool(hp))
	}
}

// ─── Execution options ────────────────────────────────────────────────────────

// WithWorkers sets the number of concurrent goroutines that invoke the
// ExclusiveHandler and CommonHandler. Defaults to runtime.NumCPU().
func WithWorkers(n int) Option {
	return func(c *config) {
		c.pipelineOpts = append(c.pipelineOpts,
			dirsync.WithExclusiveWorkers(n),
			dirsync.WithCommonWorkers(n),
		)
	}
}

// WithExclusiveWorkers sets the number of concurrent exclusive-path handler
// goroutines independently from common-path handlers.
func WithExclusiveWorkers(n int) Option {
	return func(c *config) {
		c.pipelineOpts = append(c.pipelineOpts, dirsync.WithExclusiveWorkers(n))
	}
}

// WithCommonWorkers sets the number of concurrent common-path handler goroutines.
func WithCommonWorkers(n int) Option {
	return func(c *config) {
		c.pipelineOpts = append(c.pipelineOpts, dirsync.WithCommonWorkers(n))
	}
}

// WithObserver sets the Observer that receives every classification event.
// Default: NoopObserver.
func WithObserver(o Observer) Option {
	return func(c *config) {
		if o != nil {
			c.observer = o
		}
	}
}

// WithMergedView injects a custom dirsync.MergedView factory function.
//
// This is the primary extension point for testing: inject dirsync.NewMemMergedView
// to get an in-memory test double that records ops without touching the filesystem.
//
// Example (production — default, no need to set):
//
//	WithMergedView(dirsync.NewFSMergedView)
//
// Example (test):
//
//	mem := dirsync.NewMemMergedView("/merged")
//	opt := diffview.WithMergedView(func(_ string) (dirsync.MergedView, error) {
//	    return mem, nil
//	})
func WithMergedView(factory func(merged string) (dirsync.MergedView, error)) Option {
	return func(c *config) {
		if factory != nil {
			c.viewFactory = factory
		}
	}
}

// WithBatcher injects a custom dirsync.Batcher factory function.
//
// Use this when you need precise control over the io_uring ring size, SQPOLL
// mode, or auto-flush threshold. The default uses dirsync.NewBestBatcher which
// selects io_uring on Linux 5.11+ and falls back to GoroutineBatcher elsewhere.
//
// Example (io_uring with custom ring size):
//
//	opt := diffview.WithBatcher(func(view dirsync.MergedView) (dirsync.Batcher, error) {
//	    return dirsync.NewIOURingBatcher(view, dirsync.WithRingEntries(1024))
//	})
func WithBatcher(factory func(view dirsync.MergedView) (dirsync.Batcher, error)) Option {
	return func(c *config) {
		if factory != nil {
			c.batcherFactory = factory
		}
	}
}

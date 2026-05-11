package dirsync

// ─────────────────────────────────────────────────────────────────────────────
// ClassifierOption
// ─────────────────────────────────────────────────────────────────────────────

// ClassifierOption is a functional option that configures a [DirsyncClassifier].
// Options are applied in the order they are passed to [NewClassifier].
type ClassifierOption func(*classifierConfig)

// WithFollowSymlinks instructs the classifier to dereference symlinks when
// determining whether a filesystem entry is a directory. When enabled, a
// symlink pointing to a directory is walked like a real directory and its
// target's Kind (PathKindDir) is reported rather than PathKindSymlink.
func WithFollowSymlinks(enabled bool) ClassifierOption {
	return func(c *classifierConfig) { c.followSymlinks = enabled }
}

// WithAllowWildcards enables filepath.Match glob syntax in include and exclude
// patterns. Disabled by default to avoid misinterpreting literal bracket and
// question-mark characters that appear in some build system path patterns.
func WithAllowWildcards(enabled bool) ClassifierOption {
	return func(c *classifierConfig) {
		// Wildcards are always enabled in patternmatch; this option is a no-op
		// kept for API backward compatibility.
		_ = enabled
	}
}

// WithIncludePatterns restricts the diff to paths satisfying at least one of
// the given patterns. An empty list (the default) means include everything.
//
// When both include and exclude patterns are set, exclusions take precedence
// over inclusions (a path matching both is rejected).
func WithIncludePatterns(patterns ...string) ClassifierOption {
	return func(c *classifierConfig) {
		c.includePatterns = append(c.includePatterns, patterns...)
	}
}

// WithExcludePatterns excludes paths that match any of the given patterns.
// Exclusions are evaluated before inclusions and always take precedence.
func WithExcludePatterns(patterns ...string) ClassifierOption {
	return func(c *classifierConfig) {
		c.excludePatterns = append(c.excludePatterns, patterns...)
	}
}

// WithRequiredPaths registers paths (relative to the lower root) that must
// exist before classification begins. Classify reports a [RequiredPathError]
// (satisfying errors.Is(err, ErrRequiredPathMissing)) for every absent path.
func WithRequiredPaths(paths ...string) ClassifierOption {
	return func(c *classifierConfig) {
		c.requiredPaths = append(c.requiredPaths, paths...)
	}
}

// WithExclusiveBufferSize sets the channel buffer depth for the exclusive-path
// stream. Larger values reduce back-pressure from slow consumers. Default: 512.
func WithExclusiveBufferSize(n int) ClassifierOption {
	return func(c *classifierConfig) {
		if n > 0 {
			c.exclusiveBufSz = n
		}
	}
}

// WithCommonBufferSize sets the channel buffer depth for the common-path stream.
// Default: 512.
func WithCommonBufferSize(n int) ClassifierOption {
	return func(c *classifierConfig) {
		if n > 0 {
			c.commonBufSz = n
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PipelineOption
// ─────────────────────────────────────────────────────────────────────────────

// PipelineOption is a functional option that configures a [Pipeline].
// Options are applied in the order they are passed to [NewPipeline].
type PipelineOption func(*pipelineConfig)

// pipelineConfig holds all tunable parameters for a [Pipeline].
// Zero values are valid; they are replaced with defaults in NewPipeline.
type pipelineConfig struct {
	exclusiveWorkers int // goroutine count for the exclusive pool; 0 → NumCPU
	commonWorkers    int // goroutine count for the common pool; 0 → NumCPU
	abortOnError     bool
	hashPipeline     *HashPipeline // nil → created with defaults
	exclusiveBatcher Batcher       // nil → NopBatcher
	commonBatcher    Batcher       // nil → NopBatcher
}

func defaultPipelineConfig() pipelineConfig {
	return pipelineConfig{} // zero values; defaults applied in NewPipeline
}

// WithExclusiveWorkers sets the number of goroutines that concurrently dispatch
// exclusive-path operations to the handler and batcher. Default: runtime.NumCPU().
func WithExclusiveWorkers(n int) PipelineOption {
	return func(c *pipelineConfig) {
		if n > 0 {
			c.exclusiveWorkers = n
		}
	}
}

// WithCommonWorkers sets the number of goroutines that concurrently dispatch
// common-path operations to the handler and batcher. Default: runtime.NumCPU().
func WithCommonWorkers(n int) PipelineOption {
	return func(c *pipelineConfig) {
		if n > 0 {
			c.commonWorkers = n
		}
	}
}

// WithAbortOnError causes the pipeline to cancel all stages immediately when
// any stage returns an error. Without this option (the default), all errors
// are collected and returned together via errors.Join after the pipeline drains.
//
// Use WithAbortOnError(true) for fail-fast scenarios where partial completion
// is worse than no completion (e.g. atomic deletes across a transaction).
func WithAbortOnError(abort bool) PipelineOption {
	return func(c *pipelineConfig) { c.abortOnError = abort }
}

// WithHashPipeline replaces the default [HashPipeline] with a custom instance.
// Allows callers to inject a different hasher, worker count, or buffer pools
// without rebuilding the entire pipeline.
func WithHashPipeline(hp *HashPipeline) PipelineOption {
	return func(c *pipelineConfig) { c.hashPipeline = hp }
}

// WithExclusiveBatcher sets the [Batcher] that receives exclusive-path ops
// after the handler returns successfully. Defaults to [NopBatcher] when not
// set, meaning exclusive entries are handled but not batched for mutation.
func WithExclusiveBatcher(b Batcher) PipelineOption {
	return func(c *pipelineConfig) { c.exclusiveBatcher = b }
}

// WithCommonBatcher sets the [Batcher] that receives common-path ops when
// content comparison confirms equality. Defaults to [NopBatcher].
func WithCommonBatcher(b Batcher) PipelineOption {
	return func(c *pipelineConfig) { c.commonBatcher = b }
}

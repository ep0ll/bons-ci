package differ

// ─────────────────────────────────────────────────────────────────────────────
// ClassifierOption
// ─────────────────────────────────────────────────────────────────────────────

// ClassifierOption is a functional option that configures a [DirsyncClassifier].
type ClassifierOption func(*classifierConfig)

// WithFollowSymlinks instructs the classifier to dereference symlinks when
// determining whether a filesystem entry is a directory.
func WithFollowSymlinks(v bool) ClassifierOption {
	return func(c *classifierConfig) { c.followSymlinks = v }
}

// WithAllowWildcards enables filepath.Match glob syntax in include/exclude
// patterns. Disabled by default to avoid accidental misinterpretation of
// literal bracket and question-mark characters.
func WithAllowWildcards(v bool) ClassifierOption {
	return func(c *classifierConfig) { c.allowWildcards = v }
}

// WithIncludePatterns restricts the diff to paths satisfying at least one
// pattern. An empty list means "include everything".
func WithIncludePatterns(patterns ...string) ClassifierOption {
	return func(c *classifierConfig) {
		c.includePatterns = append(c.includePatterns, patterns...)
	}
}

// WithExcludePatterns excludes paths that match any of the given patterns.
// Exclusions are evaluated before inclusions and take precedence.
func WithExcludePatterns(patterns ...string) ClassifierOption {
	return func(c *classifierConfig) {
		c.excludePatterns = append(c.excludePatterns, patterns...)
	}
}

// WithRequiredPaths registers paths that must exist in the lower directory.
// Classify reports an error for every absent required path.
func WithRequiredPaths(paths ...string) ClassifierOption {
	return func(c *classifierConfig) {
		c.requiredPaths = append(c.requiredPaths, paths...)
	}
}

// WithExclusiveBufferSize sets the channel buffer depth for the exclusive-path
// stream. Larger values reduce back-pressure from slow consumers.
func WithExclusiveBufferSize(n int) ClassifierOption {
	return func(c *classifierConfig) {
		if n > 0 {
			c.exclusiveBufSz = n
		}
	}
}

// WithCommonBufferSize sets the channel buffer depth for the common-path stream.
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
type PipelineOption func(*pipelineConfig)

type pipelineConfig struct {
	exclusiveWorkers int
	commonWorkers    int
	abortOnError     bool
	hashPipeline     *HashPipeline
	exclusiveBatcher Batcher
	commonBatcher    Batcher
}

func defaultPipelineConfig() pipelineConfig {
	return pipelineConfig{}
}

// WithExclusiveWorkers sets the number of goroutines that concurrently dispatch
// exclusive-path operations. Default: runtime.NumCPU().
func WithExclusiveWorkers(n int) PipelineOption {
	return func(c *pipelineConfig) {
		if n > 0 {
			c.exclusiveWorkers = n
		}
	}
}

// WithCommonWorkers sets the number of goroutines that concurrently dispatch
// common-path operations. Default: runtime.NumCPU().
func WithCommonWorkers(n int) PipelineOption {
	return func(c *pipelineConfig) {
		if n > 0 {
			c.commonWorkers = n
		}
	}
}

// WithAbortOnError causes the pipeline to cancel all stages as soon as any
// stage returns an error. Without this option, all errors are collected and
// returned together via errors.Join after the pipeline drains.
func WithAbortOnError(v bool) PipelineOption {
	return func(c *pipelineConfig) { c.abortOnError = v }
}

// WithHashPipeline replaces the default [HashPipeline] with a custom instance,
// allowing callers to inject a different hasher, worker count, or buffer pools.
func WithHashPipeline(hp *HashPipeline) PipelineOption {
	return func(c *pipelineConfig) { c.hashPipeline = hp }
}

// WithExclusiveBatcher sets the [Batcher] that receives exclusive-path ops.
// Defaults to a [NopBatcher] when the caller uses a handler instead.
func WithExclusiveBatcher(b Batcher) PipelineOption {
	return func(c *pipelineConfig) { c.exclusiveBatcher = b }
}

// WithCommonBatcher sets the [Batcher] that receives common-path ops.
func WithCommonBatcher(b Batcher) PipelineOption {
	return func(c *pipelineConfig) { c.commonBatcher = b }
}

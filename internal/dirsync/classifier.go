package dirsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ─────────────────────────────────────────────────────────────────────────────
// Classifier interface
// ─────────────────────────────────────────────────────────────────────────────

// Classifier compares two directory trees and produces concurrent streams of
// classified filesystem entries.
//
// # Stream contracts
//
//   - exclusive: entries found only in lower. A collapsed directory entry
//     subsumes its entire subtree; no descendants of a collapsed entry are
//     ever emitted on this channel.
//   - common: entries present in both directories. HashEqual is NOT populated
//     here — content comparison is delegated to the [HashPipeline].
//   - errs: I/O and validation errors. Receiving from this channel will never
//     block after exclusive and common are closed.
//
// Callers MUST drain all three channels to prevent goroutine leaks. The
// idiomatic pattern is to select across all three until all are nil.
//
// All three channels are closed when classification finishes or when ctx is
// cancelled.
type Classifier interface {
	Classify(ctx context.Context) (
		exclusive <-chan ExclusivePath,
		common <-chan CommonPath,
		errs <-chan error,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// classifierConfig — internal options applied by ClassifierOption functions
// ─────────────────────────────────────────────────────────────────────────────

// classifierConfig holds tunable parameters for [DirsyncClassifier].
// All fields are set exclusively through [ClassifierOption] functions;
// direct field access is package-private.
type classifierConfig struct {
	followSymlinks  bool
	includePatterns []string
	excludePatterns []string
	requiredPaths   []string
	exclusiveBufSz  int // channel buffer depth for the exclusive stream
	commonBufSz     int // channel buffer depth for the common stream
}

func defaultClassifierConfig() classifierConfig {
	return classifierConfig{
		exclusiveBufSz: 512,
		commonBufSz:    512,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DirsyncClassifier
// ─────────────────────────────────────────────────────────────────────────────

// DirsyncClassifier implements [Classifier] using the callback-based
// [walkBoth] walker. The walker is the sole producer; DirsyncClassifier adds
// channel semantics and context propagation on top.
//
// # Separation of concerns
//
// DirsyncClassifier knows only about lowerRoot, upperRoot, and the filter.
// It has no knowledge of the merged view, the batcher, or the hash pipeline —
// those are injected at the [Pipeline] level.
type DirsyncClassifier struct {
	lowerRoot string
	upperRoot string
	cfg       classifierConfig
	filter    Filter
}

// NewClassifier constructs a [DirsyncClassifier].
//
// lowerRoot and upperRoot are cleaned via filepath.Clean but not stat-checked
// eagerly — I/O errors surface on the errs channel returned by Classify, not
// in the constructor. This keeps construction infallible for dependency injection.
func NewClassifier(lowerRoot, upperRoot string, opts ...ClassifierOption) *DirsyncClassifier {
	cfg := defaultClassifierConfig()
	for _, o := range opts {
		o(&cfg)
	}

	filter := buildFilter(cfg)

	return &DirsyncClassifier{
		lowerRoot: filepath.Clean(lowerRoot),
		upperRoot: filepath.Clean(upperRoot),
		cfg:       cfg,
		filter:    filter,
	}
}

// buildFilter constructs the appropriate Filter from the classifier config.
// Returns a NoopFilter when no patterns are configured, avoiding any pattern
// matching overhead in the common case.
func buildFilter(cfg classifierConfig) Filter {
	hasPatterns := len(cfg.includePatterns) > 0 ||
		len(cfg.excludePatterns) > 0 ||
		len(cfg.requiredPaths) > 0

	if !hasPatterns {
		return NoopFilter{}
	}
	pf, err := NewPatternFilter(
		cfg.includePatterns,
		cfg.excludePatterns,
		cfg.requiredPaths,
	)
	if err != nil {
		// Invalid patterns are a programmer error caught at construction time.
		// Panic here surfaces the issue early rather than silently matching nothing.
		panic("dirsync: invalid filter patterns: " + err.Error())
	}
	return pf
}

// WithFilter replaces the filter derived from pattern options with a custom
// [Filter] implementation. Useful for composite or dynamic filtering strategies
// that cannot be expressed as simple string patterns.
func (c *DirsyncClassifier) WithFilter(f Filter) *DirsyncClassifier {
	c.filter = f
	return c
}

// LowerRoot returns the cleaned absolute path to the lower directory.
func (c *DirsyncClassifier) LowerRoot() string { return c.lowerRoot }

// UpperRoot returns the cleaned absolute path to the upper directory.
func (c *DirsyncClassifier) UpperRoot() string { return c.upperRoot }

// Classify implements [Classifier].
//
// It launches a single goroutine that drives [walkBoth] and fans results out
// to the two output channels. Cancelling ctx signals the walker to stop; both
// channels are then closed and the goroutine exits cleanly.
//
// The goroutine is always started, even when lowerRoot does not exist. The I/O
// error surfaces on the errs channel rather than as a Classify return value,
// keeping the calling pattern consistent regardless of whether the roots exist.
func (c *DirsyncClassifier) Classify(ctx context.Context) (
	<-chan ExclusivePath, <-chan CommonPath, <-chan error,
) {
	exclusiveCh := make(chan ExclusivePath, c.cfg.exclusiveBufSz)
	commonCh := make(chan CommonPath, c.cfg.commonBufSz)
	errCh := make(chan error, 8)

	go func() {
		defer close(exclusiveCh)
		defer close(commonCh)
		defer close(errCh)

		// Validate required paths before touching the walker. This gives callers
		// a clear, structured error ([RequiredPathError]) rather than a generic
		// "file not found" buried inside a walk error.
		if err := c.validateRequiredPaths(); err != nil {
			sendErr(ctx, errCh, err)
			return
		}

		fn := WalkFn{
			OnExclusive: func(ep ExclusivePath) error {
				select {
				case exclusiveCh <- ep:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			OnCommon: func(cp CommonPath) error {
				select {
				case commonCh <- cp:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		}

		if err := walkBoth(ctx, c.lowerRoot, c.upperRoot, "", c.filter, c.cfg.followSymlinks, fn); err != nil {
			if !isContextErr(err) {
				sendErr(ctx, errCh, err)
			}
		}
	}()

	return exclusiveCh, commonCh, errCh
}

// validateRequiredPaths verifies that every path in filter.RequiredPaths()
// exists in the lower directory.
//
// Returns a [RequiredPathError] (which satisfies errors.Is(err, ErrRequiredPathMissing))
// for the first absent path, or a wrapped I/O error for unexpected stat failures.
func (c *DirsyncClassifier) validateRequiredPaths() error {
	for _, rel := range c.filter.RequiredPaths() {
		abs := filepath.Join(c.lowerRoot, filepath.FromSlash(rel))
		_, err := os.Lstat(abs)
		if err == nil {
			continue
		}
		// errors.Is(err, fs.ErrNotExist) correctly handles wrapped errors from
		// *os.PathError chains (unlike the deprecated os.IsNotExist).
		if errors.Is(err, fs.ErrNotExist) {
			return &RequiredPathError{RelPath: rel, LowerRoot: c.lowerRoot}
		}
		return fmt.Errorf("classifier: stat required path %q: %w", rel, err)
	}
	return nil
}

package differ

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

// Classifier compares two directory trees and produces disjoint, concurrent
// streams of classified filesystem entries.
//
// # Exclusive stream
//
// Carries entries found only in the lower directory. Collapsed directory
// entries (Collapsed==true) subsume their entire subtrees — consumers need not
// (and must not) further enumerate descendants. This enables O(1) syscalls per
// exclusive subtree rather than O(N_files).
//
// # Common stream
//
// Carries entries present in both directories. HashEqual is NOT populated by
// the Classifier; enrichment is handled by the downstream [HashPipeline].
//
// # Error stream
//
// Carries I/O and validation errors. All three channels are closed when the
// operation finishes or ctx is cancelled.
//
// Callers must drain all three channels to prevent goroutine leaks.
type Classifier interface {
	Classify(ctx context.Context) (
		exclusive <-chan ExclusivePath,
		common <-chan CommonPath,
		errs <-chan error,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// DirsyncClassifier
// ─────────────────────────────────────────────────────────────────────────────

// DirsyncClassifier implements [Classifier] using the callback-based
// [walkBoth] walker. The walker is the sole producer; the classifier adds
// channel semantics and context propagation on top.
//
// # Decoupling
//
// The classifier knows only about lower and upper roots and the filter. It has
// no knowledge of the merged view, the batcher, or the hash pipeline.
type DirsyncClassifier struct {
	lowerRoot string
	upperRoot string
	cfg       classifierConfig
	filter    Filter
}

// classifierConfig holds tunable parameters for DirsyncClassifier.
type classifierConfig struct {
	followSymlinks  bool
	allowWildcards  bool
	includePatterns []string
	excludePatterns []string
	requiredPaths   []string
	exclusiveBufSz  int
	commonBufSz     int
}

func defaultClassifierConfig() classifierConfig {
	return classifierConfig{
		exclusiveBufSz: 512,
		commonBufSz:    512,
	}
}

// NewClassifier constructs a [DirsyncClassifier].
// lowerRoot and upperRoot are cleaned via filepath.Clean but not stat-checked
// eagerly. I/O errors surface on the errs channel from Classify.
func NewClassifier(lowerRoot, upperRoot string, opts ...ClassifierOption) *DirsyncClassifier {
	cfg := defaultClassifierConfig()
	for _, o := range opts {
		o(&cfg)
	}

	var filter Filter
	if len(cfg.includePatterns) > 0 || len(cfg.excludePatterns) > 0 || len(cfg.requiredPaths) > 0 {
		filter = NewPatternFilter(cfg.includePatterns, cfg.excludePatterns,
			cfg.requiredPaths, cfg.allowWildcards)
	} else {
		filter = NoopFilter{}
	}

	return &DirsyncClassifier{
		lowerRoot: filepath.Clean(lowerRoot),
		upperRoot: filepath.Clean(upperRoot),
		cfg:       cfg,
		filter:    filter,
	}
}

// WithFilter replaces the filter derived from pattern options with a custom
// [Filter]. Useful for composite or dynamic filtering strategies.
func (c *DirsyncClassifier) WithFilter(f Filter) *DirsyncClassifier {
	c.filter = f
	return c
}

// LowerRoot returns the absolute lower directory path.
func (c *DirsyncClassifier) LowerRoot() string { return c.lowerRoot }

// UpperRoot returns the absolute upper directory path.
func (c *DirsyncClassifier) UpperRoot() string { return c.upperRoot }

// Classify implements [Classifier].
//
// It launches a single goroutine that drives walkBoth and sends results to the
// two output channels. Cancelling ctx signals the walker to stop; both channels
// are then closed and the goroutine exits cleanly.
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

		if err := c.validateRequired(); err != nil {
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

		if err := walkBoth(ctx, c.lowerRoot, c.upperRoot, "", c.filter,
			c.cfg.followSymlinks, fn); err != nil && !isContextErr(err) {
			sendErr(ctx, errCh, err)
		}
	}()

	return exclusiveCh, commonCh, errCh
}

// validateRequired verifies that every path in filter.RequiredPaths() exists
// in the lower directory.
func (c *DirsyncClassifier) validateRequired() error {
	for _, rel := range c.filter.RequiredPaths() {
		abs := filepath.Join(c.lowerRoot, filepath.FromSlash(rel))
		if _, err := os.Lstat(abs); err != nil {
			// BUG FIX L2: os.IsNotExist does not unwrap error chains beyond
			// *os.PathError. errors.Is(err, fs.ErrNotExist) is the correct
			// Go 1.13+ idiom and handles wrapped errors correctly.
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("classifier: required path %q absent from lower %q",
					rel, c.lowerRoot)
			}
			return fmt.Errorf("classifier: stat required %q: %w", rel, err)
		}
	}
	return nil
}

// sendErr sends err to errCh without blocking. Drops silently when ctx is done.
func sendErr(ctx context.Context, errCh chan<- error, err error) {
	select {
	case errCh <- err:
	case <-ctx.Done():
	}
}

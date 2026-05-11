package dirsync

import (
	"errors"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors — match with errors.Is
// ─────────────────────────────────────────────────────────────────────────────

// ErrBatcherClosed is returned by [Batcher.Submit] and [Batcher.Flush] after
// [Batcher.Close] has been called. It signals a programmer error: work was
// submitted to a batcher that was already shut down.
var ErrBatcherClosed = errors.New("dirsync: batcher already closed")

// ErrRequiredPathMissing is returned by [Classifier.Classify] when a path
// registered via [WithRequiredPaths] is absent from the lower directory.
var ErrRequiredPathMissing = errors.New("dirsync: required path absent from lower directory")

// ErrPathTraversal is returned when a path component would escape the root.
var ErrPathTraversal = errors.New("dirsync: path traversal attempt detected")

// ─────────────────────────────────────────────────────────────────────────────
// Structured errors — extract context with errors.As
// ─────────────────────────────────────────────────────────────────────────────

// RequiredPathError is the structured form of [ErrRequiredPathMissing].
// It carries both the missing relative path and the lower root it was expected
// to exist within, making it actionable in logs and metrics.
type RequiredPathError struct {
	RelPath   string // missing path relative to LowerRoot
	LowerRoot string // absolute path to the lower directory
}

// Error implements the error interface.
func (e *RequiredPathError) Error() string {
	return fmt.Sprintf("dirsync: required path %q absent from lower %q",
		e.RelPath, e.LowerRoot)
}

// Is makes errors.Is(err, ErrRequiredPathMissing) return true for this type,
// allowing sentinel matching without losing the structured context.
func (e *RequiredPathError) Is(target error) bool {
	return target == ErrRequiredPathMissing
}

// OpError wraps a filesystem operation error with the path that caused it.
// Returned by [MergedView] and [Batcher] implementations.
type OpError struct {
	Op   string // operation name: "remove", "removeAll", "stat"
	Path string // relative path within the merged view
	Err  error  // underlying OS error
}

// Error implements the error interface.
func (e *OpError) Error() string {
	return fmt.Sprintf("dirsync: %s %q: %v", e.Op, e.Path, e.Err)
}

// Unwrap enables errors.Is and errors.As to unwrap through OpError.
func (e *OpError) Unwrap() error { return e.Err }

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// wrapOp wraps err with operation context. Returns nil when err is nil,
// keeping the happy path free of allocation.
func wrapOp(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return &OpError{Op: op, Path: path, Err: err}
}

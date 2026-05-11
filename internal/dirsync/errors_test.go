package dirsync_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// ErrBatcherClosed
// ─────────────────────────────────────────────────────────────────────────────

func TestErrBatcherClosed_SentinelIdentity(t *testing.T) {
	t.Parallel()
	assert.True(t, errors.Is(dirsync.ErrBatcherClosed, dirsync.ErrBatcherClosed))
}

// ─────────────────────────────────────────────────────────────────────────────
// RequiredPathError
// ─────────────────────────────────────────────────────────────────────────────

func TestRequiredPathError_ErrorString(t *testing.T) {
	t.Parallel()
	e := &dirsync.RequiredPathError{RelPath: "go.mod", LowerRoot: "/lower"}
	msg := e.Error()
	assert.Contains(t, msg, "go.mod")
	assert.Contains(t, msg, "/lower")
}

func TestRequiredPathError_MatchesSentinel(t *testing.T) {
	t.Parallel()
	e := &dirsync.RequiredPathError{RelPath: "go.mod", LowerRoot: "/lower"}
	// errors.Is must match the sentinel ErrRequiredPathMissing
	assert.True(t, errors.Is(e, dirsync.ErrRequiredPathMissing))
}

func TestRequiredPathError_ErrorsAs_ExtractsFields(t *testing.T) {
	t.Parallel()
	e := &dirsync.RequiredPathError{RelPath: "go.sum", LowerRoot: "/lower"}
	wrapped := errors.Join(errors.New("outer"), e)

	var rpe *dirsync.RequiredPathError
	require.True(t, errors.As(wrapped, &rpe))
	assert.Equal(t, "go.sum", rpe.RelPath)
	assert.Equal(t, "/lower", rpe.LowerRoot)
}

// ─────────────────────────────────────────────────────────────────────────────
// OpError
// ─────────────────────────────────────────────────────────────────────────────

func TestOpError_ErrorString(t *testing.T) {
	t.Parallel()
	e := &dirsync.OpError{Op: "remove", Path: "a/b.txt", Err: errors.New("permission denied")}
	msg := e.Error()
	assert.Contains(t, msg, "remove")
	assert.Contains(t, msg, "a/b.txt")
	assert.Contains(t, msg, "permission denied")
}

func TestOpError_Unwrap_PreservesChain(t *testing.T) {
	t.Parallel()
	underlying := errors.New("underlying-error")
	e := &dirsync.OpError{Op: "stat", Path: "x", Err: underlying}
	assert.True(t, errors.Is(e, underlying), "Unwrap must expose the underlying error")
}

func TestOpError_ErrorsAs_ExtractsOpError(t *testing.T) {
	t.Parallel()
	inner := &dirsync.OpError{Op: "removeAll", Path: "lib", Err: errors.New("io error")}
	wrapped := errors.Join(errors.New("context"), inner)

	var oe *dirsync.OpError
	require.True(t, errors.As(wrapped, &oe))
	assert.Equal(t, "removeAll", oe.Op)
	assert.Equal(t, "lib", oe.Path)
}

// ─────────────────────────────────────────────────────────────────────────────
// ErrRequiredPathMissing from Classifier
// ─────────────────────────────────────────────────────────────────────────────

func TestClassifier_RequiredPath_Error_IsRequiredPathMissing(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	makeTree(t, lower, map[string]string{"a.txt": "a"})

	c := dirsync.NewClassifier(lower, upper, dirsync.WithRequiredPaths("missing.txt"))
	_, _, errCh := c.Classify(context.Background())

	var errs []error
	for e := range errCh {
		errs = append(errs, e)
	}
	require.NotEmpty(t, errs)
	assert.True(t, errors.Is(errs[0], dirsync.ErrRequiredPathMissing))

	// Should also be extractable as RequiredPathError
	var rpe *dirsync.RequiredPathError
	assert.True(t, errors.As(errs[0], &rpe))
	assert.Equal(t, "missing.txt", rpe.RelPath)
	assert.True(t, strings.HasPrefix(rpe.LowerRoot, "/"))
}

package dirsync_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// NoopHandlers
// ─────────────────────────────────────────────────────────────────────────────

func TestNoopExclusiveHandler_AlwaysNil(t *testing.T) {
	t.Parallel()
	h := dirsync.NoopExclusiveHandler{}
	assert.NoError(t, h.HandleExclusive(context.Background(), dirsync.ExclusivePath{}))
}

func TestNoopCommonHandler_AlwaysNil(t *testing.T) {
	t.Parallel()
	h := dirsync.NoopCommonHandler{}
	assert.NoError(t, h.HandleCommon(context.Background(), dirsync.CommonPath{}))
}

// ─────────────────────────────────────────────────────────────────────────────
// Function adapters
// ─────────────────────────────────────────────────────────────────────────────

func TestExclusiveHandlerFunc_WrapsFunction(t *testing.T) {
	t.Parallel()
	var called bool
	h := dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error {
		called = true
		return nil
	})
	require.NoError(t, h.HandleExclusive(context.Background(), dirsync.ExclusivePath{}))
	assert.True(t, called)
}

func TestCommonHandlerFunc_WrapsFunction(t *testing.T) {
	t.Parallel()
	var called bool
	h := dirsync.CommonHandlerFunc(func(_ context.Context, _ dirsync.CommonPath) error {
		called = true
		return nil
	})
	require.NoError(t, h.HandleCommon(context.Background(), dirsync.CommonPath{}))
	assert.True(t, called)
}

// ─────────────────────────────────────────────────────────────────────────────
// ChainExclusiveHandler — stops on first error
// ─────────────────────────────────────────────────────────────────────────────

func TestChainExclusiveHandler_StopsOnFirstError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stop-here")
	var secondCalled atomic.Bool

	chain := dirsync.ChainExclusiveHandler{
		dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error {
			return sentinel
		}),
		dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error {
			secondCalled.Store(true)
			return nil
		}),
	}

	err := chain.HandleExclusive(context.Background(), dirsync.ExclusivePath{})
	assert.ErrorIs(t, err, sentinel)
	assert.False(t, secondCalled.Load(), "second handler must NOT run after first error")
}

func TestChainCommonHandler_StopsOnFirstError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("chain-stop")
	var secondCalled atomic.Bool

	chain := dirsync.ChainCommonHandler{
		dirsync.CommonHandlerFunc(func(_ context.Context, _ dirsync.CommonPath) error { return sentinel }),
		dirsync.CommonHandlerFunc(func(_ context.Context, _ dirsync.CommonPath) error {
			secondCalled.Store(true); return nil
		}),
	}

	err := chain.HandleCommon(context.Background(), dirsync.CommonPath{})
	assert.ErrorIs(t, err, sentinel)
	assert.False(t, secondCalled.Load())
}

// ─────────────────────────────────────────────────────────────────────────────
// MultiExclusiveHandler — fan-out, collects all errors
// ─────────────────────────────────────────────────────────────────────────────

func TestMultiExclusiveHandler_CollectsAllErrors(t *testing.T) {
	t.Parallel()
	e1, e2 := errors.New("e1"), errors.New("e2")
	multi := dirsync.MultiExclusiveHandler{
		dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error { return e1 }),
		dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error { return e2 }),
	}
	err := multi.HandleExclusive(context.Background(), dirsync.ExclusivePath{})
	assert.ErrorIs(t, err, e1, "must contain e1")
	assert.ErrorIs(t, err, e2, "must contain e2")
}

func TestMultiExclusiveHandler_AllHandlersCalledDespiteErrors(t *testing.T) {
	t.Parallel()
	var count atomic.Int32
	sentinel := errors.New("fail")
	multi := dirsync.MultiExclusiveHandler{
		dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error {
			count.Add(1); return sentinel
		}),
		dirsync.ExclusiveHandlerFunc(func(_ context.Context, _ dirsync.ExclusivePath) error {
			count.Add(1); return nil
		}),
	}
	_ = multi.HandleExclusive(context.Background(), dirsync.ExclusivePath{})
	assert.Equal(t, int32(2), count.Load(), "both handlers must run even when first fails")
}

// ─────────────────────────────────────────────────────────────────────────────
// PredicateExclusiveHandler
// ─────────────────────────────────────────────────────────────────────────────

func TestPredicateExclusiveHandler_OnlyCollapsed_FiltersCorrectly(t *testing.T) {
	t.Parallel()
	var seen []string
	h := dirsync.PredicateExclusiveHandler{
		Predicate: dirsync.OnlyCollapsed(),
		Handler: dirsync.ExclusiveHandlerFunc(func(_ context.Context, ep dirsync.ExclusivePath) error {
			seen = append(seen, ep.Path)
			return nil
		}),
	}

	_ = h.HandleExclusive(context.Background(), dirsync.ExclusivePath{Path: "dir", Collapsed: true, Kind: dirsync.PathKindDir})
	_ = h.HandleExclusive(context.Background(), dirsync.ExclusivePath{Path: "file", Collapsed: false, Kind: dirsync.PathKindFile})

	require.Len(t, seen, 1)
	assert.Equal(t, "dir", seen[0])
}

func TestPredicateCommonHandler_OnlyChanged_FiltersCorrectly(t *testing.T) {
	t.Parallel()
	var seen []string
	h := dirsync.PredicateCommonHandler{
		Predicate: dirsync.OnlyChanged(),
		Handler: dirsync.CommonHandlerFunc(func(_ context.Context, cp dirsync.CommonPath) error {
			seen = append(seen, cp.Path)
			return nil
		}),
	}

	f := false; tr := true
	_ = h.HandleCommon(context.Background(), dirsync.CommonPath{Path: "changed", HashEqual: &f})
	_ = h.HandleCommon(context.Background(), dirsync.CommonPath{Path: "same", HashEqual: &tr})
	_ = h.HandleCommon(context.Background(), dirsync.CommonPath{Path: "dir"}) // nil HashEqual

	require.Len(t, seen, 1)
	assert.Equal(t, "changed", seen[0])
}

func TestPredicateCommonHandler_OnlyUnchanged_FiltersCorrectly(t *testing.T) {
	t.Parallel()
	var seen []string
	h := dirsync.PredicateCommonHandler{
		Predicate: dirsync.OnlyUnchanged(),
		Handler: dirsync.CommonHandlerFunc(func(_ context.Context, cp dirsync.CommonPath) error {
			seen = append(seen, cp.Path)
			return nil
		}),
	}

	f := false; tr := true
	_ = h.HandleCommon(context.Background(), dirsync.CommonPath{Path: "changed", HashEqual: &f})
	_ = h.HandleCommon(context.Background(), dirsync.CommonPath{Path: "same", HashEqual: &tr})

	require.Len(t, seen, 1)
	assert.Equal(t, "same", seen[0])
}

// ─────────────────────────────────────────────────────────────────────────────
// CountingExclusiveHandler
// ─────────────────────────────────────────────────────────────────────────────

func TestCountingExclusiveHandler_CountsByKind(t *testing.T) {
	t.Parallel()
	var c dirsync.CountingExclusiveHandler
	eps := []dirsync.ExclusivePath{
		{Kind: dirsync.PathKindFile},
		{Kind: dirsync.PathKindFile},
		{Kind: dirsync.PathKindDir, Collapsed: true},
		{Kind: dirsync.PathKindSymlink},
	}
	for _, ep := range eps {
		require.NoError(t, c.HandleExclusive(context.Background(), ep))
	}

	snap := c.Snapshot()
	assert.Equal(t, int64(2), snap.Files)
	assert.Equal(t, int64(1), snap.Dirs)
	assert.Equal(t, int64(1), snap.Symlinks)
	assert.Equal(t, int64(1), snap.Collapsed)
	assert.Equal(t, int64(4), snap.Total())
}

// ─────────────────────────────────────────────────────────────────────────────
// CountingCommonHandler
// ─────────────────────────────────────────────────────────────────────────────

func TestCountingCommonHandler_CountsByOutcome(t *testing.T) {
	t.Parallel()
	var c dirsync.CountingCommonHandler
	f, tr := false, true
	cps := []dirsync.CommonPath{
		{Kind: dirsync.PathKindFile, HashEqual: &tr},
		{Kind: dirsync.PathKindFile, HashEqual: &f},
		{Kind: dirsync.PathKindDir},
	}
	for _, cp := range cps {
		require.NoError(t, c.HandleCommon(context.Background(), cp))
	}

	snap := c.Snapshot()
	assert.Equal(t, int64(3), snap.Total)
	assert.Equal(t, int64(1), snap.Equal)
	assert.Equal(t, int64(1), snap.Changed)
	assert.Equal(t, int64(1), snap.Unchecked)
}

// ─────────────────────────────────────────────────────────────────────────────
// LogExclusiveHandler
// ─────────────────────────────────────────────────────────────────────────────

func TestLogExclusiveHandler_WritesStructuredLog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := &dirsync.LogExclusiveHandler{
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
		Level:  slog.LevelInfo,
	}
	_ = h.HandleExclusive(context.Background(), dirsync.ExclusivePath{
		Path: "vendor/dep", Kind: dirsync.PathKindDir, Collapsed: true,
	})
	assert.Contains(t, buf.String(), "vendor/dep")
	assert.Contains(t, buf.String(), "dir")
}

// ─────────────────────────────────────────────────────────────────────────────
// DryRunExclusiveHandler
// ─────────────────────────────────────────────────────────────────────────────

func TestDryRunExclusiveHandler_CollapsedUsesRemoveAll(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := &dirsync.DryRunExclusiveHandler{Writer: &buf}
	_ = h.HandleExclusive(context.Background(), dirsync.ExclusivePath{
		Path: "lib", Collapsed: true, Kind: dirsync.PathKindDir,
	})
	assert.True(t, strings.Contains(buf.String(), "removeAll"),
		"collapsed dir should use removeAll, got: %s", buf.String())
}

func TestDryRunExclusiveHandler_LeafFileUsesRemove(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := &dirsync.DryRunExclusiveHandler{Writer: &buf}
	_ = h.HandleExclusive(context.Background(), dirsync.ExclusivePath{
		Path: "f.txt", Kind: dirsync.PathKindFile,
	})
	out := buf.String()
	assert.True(t, strings.Contains(out, "remove"),
		"leaf file should use remove, got: %s", out)
	assert.False(t, strings.Contains(out, "removeAll"),
		"leaf file must NOT use removeAll, got: %s", out)
}

func TestDryRunExclusiveHandler_NilWriter_DefaultsToStdout(t *testing.T) {
	t.Parallel()
	h := &dirsync.DryRunExclusiveHandler{} // nil Writer → defaults to os.Stdout
	// Must not panic.
	assert.NotPanics(t, func() {
		_ = h.HandleExclusive(context.Background(), dirsync.ExclusivePath{Path: "x"})
	})
}

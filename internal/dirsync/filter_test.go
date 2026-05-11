package dirsync_test

import (
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// NoopFilter
// ─────────────────────────────────────────────────────────────────────────────

func TestNoopFilter_IncludeAlwaysTrue(t *testing.T) {
	t.Parallel()
	f := dirsync.NoopFilter{}
	assert.True(t, f.Include("anything", false))
	assert.True(t, f.Include("a/b/c", true))
	assert.True(t, f.Include("", false))
}

func TestNoopFilter_RequiredPathsNil(t *testing.T) {
	t.Parallel()
	assert.Nil(t, dirsync.NoopFilter{}.RequiredPaths())
}

// ─────────────────────────────────────────────────────────────────────────────
// PatternFilter — construction
// ─────────────────────────────────────────────────────────────────────────────

func TestPatternFilter_InvalidPatternReturnsError(t *testing.T) {
	t.Parallel()
	_, err := dirsync.NewPatternFilter([]string{"["}, nil, nil) // unclosed bracket
	assert.Error(t, err, "invalid glob pattern must return an error")
}

func TestPatternFilter_EmptyPatternsActsLikeNoop(t *testing.T) {
	t.Parallel()
	f, err := dirsync.NewPatternFilter(nil, nil, nil)
	require.NoError(t, err)
	assert.True(t, f.Include("vendor/foo.go", false))
	assert.True(t, f.Include("main.go", false))
}

// ─────────────────────────────────────────────────────────────────────────────
// PatternFilter — include patterns
// ─────────────────────────────────────────────────────────────────────────────

func TestPatternFilter_IncludeExact(t *testing.T) {
	t.Parallel()
	f, err := dirsync.NewPatternFilter([]string{"vendor"}, nil, nil)
	require.NoError(t, err)

	assert.True(t, f.Include("vendor", false), "exact match should be included")
	assert.True(t, f.Include("vendor/pkg/foo.go", false), "descendant should be included")
	assert.False(t, f.Include("main.go", false), "non-matching file should be excluded")
}

func TestPatternFilter_IncludeGlob(t *testing.T) {
	t.Parallel()
	// moby/patternmatcher follows gitignore semantics: "*.go" matches at depth 0.
	// To match at any depth use "**/*.go". This test validates library behaviour.
	f, err := dirsync.NewPatternFilter([]string{"*.go"}, nil, nil)
	require.NoError(t, err)

	assert.True(t, f.Include("main.go", false), "direct .go match at root")
	assert.False(t, f.Include("README.md", false), "non-.go file excluded")

	// "pkg/util.go" does NOT match "*.go" in moby/patternmatcher (gitignore depth-0 semantics).
	// Use "**/*.go" for depth-independent matching.
	assert.False(t, f.Include("pkg/util.go", false), "nested path does not match depth-0 glob")
}

func TestPatternFilter_DoubleStarGlob_MatchesAtAnyDepth(t *testing.T) {
	t.Parallel()
	f, err := dirsync.NewPatternFilter([]string{"**/*.go"}, nil, nil)
	require.NoError(t, err)

	assert.True(t, f.Include("main.go", false), "root .go match")
	assert.True(t, f.Include("pkg/util.go", false), "nested .go match with **")
	assert.False(t, f.Include("README.md", false), "non-.go excluded")
}

func TestPatternFilter_DirectoryAllowsDescent(t *testing.T) {
	t.Parallel()
	// "vendor/pkg" include should allow descent into "vendor" dir
	f, err := dirsync.NewPatternFilter([]string{"vendor/pkg"}, nil, nil)
	require.NoError(t, err)

	assert.True(t, f.Include("vendor", true), "parent dir should be entered for descent")
	assert.True(t, f.Include("vendor/pkg", false), "exact match included")
	assert.False(t, f.Include("other", true), "unrelated dir excluded")
}

// ─────────────────────────────────────────────────────────────────────────────
// PatternFilter — exclude patterns
// ─────────────────────────────────────────────────────────────────────────────

func TestPatternFilter_ExcludeWinsOverInclude(t *testing.T) {
	t.Parallel()
	f, err := dirsync.NewPatternFilter([]string{"vendor"}, []string{"vendor"}, nil)
	require.NoError(t, err)
	assert.False(t, f.Include("vendor", false), "exclude takes precedence over include")
	assert.False(t, f.Include("vendor/foo.go", false), "exclude descendant too")
}

func TestPatternFilter_ExcludeSubdirectory(t *testing.T) {
	t.Parallel()
	f, err := dirsync.NewPatternFilter(nil, []string{"vendor"}, nil)
	require.NoError(t, err)
	assert.False(t, f.Include("vendor", true))
	assert.False(t, f.Include("vendor/dep.go", false))
	assert.True(t, f.Include("main.go", false), "non-excluded file still passes")
}

// ─────────────────────────────────────────────────────────────────────────────
// PatternFilter — required paths
// ─────────────────────────────────────────────────────────────────────────────

func TestPatternFilter_RequiredPaths(t *testing.T) {
	t.Parallel()
	f, err := dirsync.NewPatternFilter(nil, nil, []string{"go.mod", "go.sum"})
	require.NoError(t, err)
	assert.Equal(t, []string{"go.mod", "go.sum"}, f.RequiredPaths())
}

// ─────────────────────────────────────────────────────────────────────────────
// PatternFilter — edge cases
// ─────────────────────────────────────────────────────────────────────────────

func TestPatternFilter_EmptyRelPath(t *testing.T) {
	t.Parallel()
	f, err := dirsync.NewPatternFilter([]string{"*"}, nil, nil)
	require.NoError(t, err)
	// Empty path means the root; an include-all pattern should accept it.
	assert.True(t, f.Include("", false))
}

func TestPatternFilter_WildcardAllowsDescent(t *testing.T) {
	t.Parallel()
	// A "*.go" pattern should allow descent into ALL directories
	// because a .go file might exist anywhere.
	f, err := dirsync.NewPatternFilter([]string{"*.go"}, nil, nil)
	require.NoError(t, err)
	assert.True(t, f.Include("pkg", true), "wildcards allow descent into any dir")
	assert.True(t, f.Include("internal/util", true))
}

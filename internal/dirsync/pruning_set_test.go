package dirsync_test

import (
	"sync"
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// PruningSet — unit tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPruningSet_Add_AcceptsLeafEntry(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ok := ps.Add(dirsync.ExclusivePath{Path: "a.txt", Kind: dirsync.PathKindFile})
	assert.True(t, ok)
	assert.Equal(t, 1, ps.Len())
}

func TestPruningSet_CollapseDir_SubsumesExistingDescendants(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ps.Add(dirsync.ExclusivePath{Path: "a/b/c.txt", Kind: dirsync.PathKindFile})
	ps.Add(dirsync.ExclusivePath{Path: "a/b/d.txt", Kind: dirsync.PathKindFile})

	ok := ps.Add(dirsync.ExclusivePath{Path: "a/b", Kind: dirsync.PathKindDir, Collapsed: true})
	require.True(t, ok, "collapsed dir must be accepted")
	// The two children should have been pruned; only the collapsed dir remains.
	assert.Equal(t, 1, ps.Len())
	entries := ps.Entries()
	assert.Equal(t, "a/b", entries[0].Path)
}

func TestPruningSet_Descendant_RejectedWhenAncestorCollapsed(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ps.Add(dirsync.ExclusivePath{Path: "top", Kind: dirsync.PathKindDir, Collapsed: true})
	ok := ps.Add(dirsync.ExclusivePath{Path: "top/sub/file.txt", Kind: dirsync.PathKindFile})
	assert.False(t, ok, "descendant of collapsed dir must be rejected")
	assert.Equal(t, 1, ps.Len())
}

func TestPruningSet_SiblingPaths_BothAccepted(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ps.Add(dirsync.ExclusivePath{Path: "a", Kind: dirsync.PathKindDir, Collapsed: true})
	ok := ps.Add(dirsync.ExclusivePath{Path: "b/c.txt", Kind: dirsync.PathKindFile})
	assert.True(t, ok, "sibling path must be accepted")
	assert.Equal(t, 2, ps.Len())
}

func TestPruningSet_Covered_ReportsCorrectly(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ps.Add(dirsync.ExclusivePath{Path: "libs", Kind: dirsync.PathKindDir, Collapsed: true})

	assert.True(t, ps.Covered("libs/foo/bar.so"), "must be covered by 'libs'")
	assert.True(t, ps.Covered("libs"), "exact match is covered")
	assert.False(t, ps.Covered("other/file.txt"), "unrelated path must not be covered")
	assert.False(t, ps.Covered("libsextra/foo"), "prefix without separator must not match")
}

func TestPruningSet_Drain_EmptiesSet(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	for _, p := range []string{"a", "b", "c"} {
		ps.Add(dirsync.ExclusivePath{Path: p, Kind: dirsync.PathKindFile})
	}

	var drained []string
	ps.Drain(func(ep dirsync.ExclusivePath) { drained = append(drained, ep.Path) })

	assert.Equal(t, 0, ps.Len(), "set must be empty after Drain")
	assert.Len(t, drained, 3)
}

func TestPruningSet_ForEach_DoesNotRemoveEntries(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ps.Add(dirsync.ExclusivePath{Path: "x"})
	ps.Add(dirsync.ExclusivePath{Path: "y"})

	var seen []string
	ps.ForEach(func(ep dirsync.ExclusivePath) { seen = append(seen, ep.Path) })

	assert.Len(t, seen, 2, "ForEach must visit both entries")
	assert.Equal(t, 2, ps.Len(), "ForEach must NOT remove entries")
}

func TestPruningSet_Reset_ClearsWithoutCallback(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ps.Add(dirsync.ExclusivePath{Path: "x"})
	ps.Reset()
	assert.Equal(t, 0, ps.Len())
}

func TestPruningSet_Entries_ReturnsCopy(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	ps.Add(dirsync.ExclusivePath{Path: "a"})
	entries := ps.Entries()
	entries[0].Path = "mutated"
	// Original set must not be affected.
	assert.Equal(t, "a", ps.Entries()[0].Path, "Entries must return a defensive copy")
}

func TestPruningSet_ConcurrentAdd_RaceDetector(t *testing.T) {
	t.Parallel()
	var ps dirsync.PruningSet
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ps.Add(dirsync.ExclusivePath{
				Path: "path/" + string(rune('a'+i%26)),
				Kind: dirsync.PathKindFile,
			})
		}()
	}
	wg.Wait()
	// No assertions on exact count (concurrent adds may collapse), but must not race.
	assert.GreaterOrEqual(t, ps.Len(), 1)
}

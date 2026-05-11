package dirsync_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// PathKind.String
// ─────────────────────────────────────────────────────────────────────────────

func TestPathKind_String_AllValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind dirsync.PathKind
		want string
	}{
		{dirsync.PathKindFile, "file"},
		{dirsync.PathKindDir, "dir"},
		{dirsync.PathKindSymlink, "symlink"},
		{dirsync.PathKindOther, "other"},
		{dirsync.PathKindUnknown, "unknown"},
		{dirsync.PathKind(255), "unknown"}, // any unrecognised value
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.String())
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PathKindOf
// ─────────────────────────────────────────────────────────────────────────────

func TestPathKindOf_RegularFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
	info, err := os.Lstat(f)
	require.NoError(t, err)
	assert.Equal(t, dirsync.PathKindFile, dirsync.PathKindOf(info))
}

func TestPathKindOf_Directory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	info, err := os.Lstat(dir)
	require.NoError(t, err)
	assert.Equal(t, dirsync.PathKindDir, dirsync.PathKindOf(info))
}

func TestPathKindOf_Symlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink("/tmp", link))
	info, err := os.Lstat(link) // Lstat sees the symlink itself
	require.NoError(t, err)
	assert.Equal(t, dirsync.PathKindSymlink, dirsync.PathKindOf(info))
}

// ─────────────────────────────────────────────────────────────────────────────
// CommonPath helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestCommonPath_IsContentEqual_NilHashEqual(t *testing.T) {
	t.Parallel()
	cp := dirsync.CommonPath{}
	eq, checked := cp.IsContentEqual()
	assert.False(t, eq)
	assert.False(t, checked)
}

func TestCommonPath_IsContentEqual_True(t *testing.T) {
	t.Parallel()
	tr := true
	cp := dirsync.CommonPath{HashEqual: &tr}
	eq, checked := cp.IsContentEqual()
	assert.True(t, eq)
	assert.True(t, checked)
}

func TestCommonPath_IsContentEqual_False(t *testing.T) {
	t.Parallel()
	f := false
	cp := dirsync.CommonPath{HashEqual: &f}
	eq, checked := cp.IsContentEqual()
	assert.False(t, eq)
	assert.True(t, checked)
}

func TestCommonPath_TypeMismatch_NilInfos(t *testing.T) {
	t.Parallel()
	cp := dirsync.CommonPath{} // both nil
	assert.False(t, cp.TypeMismatch(), "nil infos must return false, not panic")
}

func TestCommonPath_TypeMismatch_SameType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.txt")
	require.NoError(t, os.WriteFile(f1, []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(f2, []byte("b"), 0o644))
	i1, _ := os.Lstat(f1)
	i2, _ := os.Lstat(f2)
	cp := dirsync.CommonPath{LowerInfo: i1, UpperInfo: i2}
	assert.False(t, cp.TypeMismatch())
}

func TestCommonPath_TypeMismatch_FileVsDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	d := filepath.Join(dir, "sub")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(d, 0o755))
	fi, _ := os.Lstat(f)
	di, _ := os.Lstat(d)
	cp := dirsync.CommonPath{LowerInfo: di, UpperInfo: fi}
	assert.True(t, cp.TypeMismatch())
}

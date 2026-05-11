package dirsync_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// TwoPhaseHasher — unit tests
// ─────────────────────────────────────────────────────────────────────────────

func newHasher(t int64, workers int) *dirsync.TwoPhaseHasher {
	return &dirsync.TwoPhaseHasher{
		LargeFileThreshold: t,
		SegmentWorkers:     workers,
	}
}

func writePastMtime(t *testing.T, path string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, past, past))
}

func writeFilePair(t *testing.T, lower, upper, name, lContent, uContent string) (lAbs, uAbs string) {
	t.Helper()
	lAbs = filepath.Join(lower, name)
	uAbs = filepath.Join(upper, name)
	require.NoError(t, os.WriteFile(lAbs, []byte(lContent), 0o644))
	require.NoError(t, os.WriteFile(uAbs, []byte(uContent), 0o644))
	return
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 1 — Inode identity (hard links)
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_Tier1_SameInode_HardLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.txt")
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.WriteFile(orig, []byte("data"), 0o644))
	require.NoError(t, os.Link(orig, link))

	h := newHasher(1, 1)
	lInfo, _ := os.Lstat(orig)
	uInfo, _ := os.Lstat(link)
	eq, err := h.Equal(orig, link, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq, "hard links are definitionally equal (Tier 1)")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 2 — Size mismatch
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_Tier2_DifferentSizes_NotEqual(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	lAbs, uAbs := writeFilePair(t, lower, upper, "f.txt", "short", "much longer content")

	h := newHasher(1<<30, 1)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "different sizes must be not equal (Tier 2)")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 3 — Mtime equality (same mtime means assumed equal)
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_Tier3_SameMtime_EqualWithoutIO(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	lAbs, uAbs := writeFilePair(t, lower, upper, "f.txt", "version-A", "version-B") // same size different content

	// Force same mtime on both files — Tier 3 should report equal without I/O.
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(lAbs, ts, ts))
	require.NoError(t, os.Chtimes(uAbs, ts, ts))

	h := newHasher(1<<30, 1)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq, "same mtime → assumed equal (Tier 3), even with different content")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 4S — sequential comparison
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_Tier4S_EqualFiles(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	content := "identical content for both files"
	lAbs, uAbs := writeFilePair(t, lower, upper, "f.txt", content, content)
	writePastMtime(t, lAbs) // force past mtime to skip Tier 3

	h := newHasher(1<<30, 1) // threshold above file size → always sequential
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq, "identical files must be equal (Tier 4S)")
}

func TestHasher_Tier4S_DifferentContent_SameSize(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	lAbs, uAbs := writeFilePair(t, lower, upper, "f.txt", "version-A", "version-B") // same length
	writePastMtime(t, lAbs)

	h := newHasher(1<<30, 1)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "different content must be not equal (Tier 4S)")
}

func TestHasher_Tier4S_EmptyFiles_Equal(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	lAbs, uAbs := writeFilePair(t, lower, upper, "empty.txt", "", "")
	writePastMtime(t, lAbs)

	h := newHasher(1<<30, 1)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq, "both empty → equal")
}

func TestHasher_Tier4S_DifferInSecondChunk(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	chunkSize := 64 * 1024
	lData := make([]byte, 3*chunkSize)
	uData := make([]byte, 3*chunkSize)
	copy(uData, lData)
	lData[chunkSize] = 0x01 // differ in second chunk
	uData[chunkSize] = 0x02

	lAbs := filepath.Join(lower, "f.bin")
	uAbs := filepath.Join(upper, "f.bin")
	require.NoError(t, os.WriteFile(lAbs, lData, 0o644))
	require.NoError(t, os.WriteFile(uAbs, uData, 0o644))
	writePastMtime(t, lAbs)

	h := newHasher(1<<30, 1)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "files differ in second chunk")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 4P — parallel comparison
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_Tier4P_EqualLargeFile(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	data := make([]byte, 4<<20) // 4 MiB
	for i := range data { data[i] = byte(i * 7) }

	lAbs := filepath.Join(lower, "big.bin")
	uAbs := filepath.Join(upper, "big.bin")
	require.NoError(t, os.WriteFile(lAbs, data, 0o644))
	require.NoError(t, os.WriteFile(uAbs, data, 0o644))
	writePastMtime(t, lAbs)

	h := newHasher(1<<20, 4) // 1 MiB threshold, 4 workers
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq, "identical large files must be equal (Tier 4P)")
}

func TestHasher_Tier4P_DifferInFirstByte_EarlyExit(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	size := 8 << 20 // 8 MiB
	lData := make([]byte, size)
	uData := make([]byte, size)
	uData[0] = 0xFF // differ at byte 0 — all segments should cancel quickly

	lAbs := filepath.Join(lower, "big.bin")
	uAbs := filepath.Join(upper, "big.bin")
	require.NoError(t, os.WriteFile(lAbs, lData, 0o644))
	require.NoError(t, os.WriteFile(uAbs, uData, 0o644))
	writePastMtime(t, lAbs)

	h := newHasher(1<<20, 4)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "differ at byte 0 must not be equal (Tier 4P)")
}

func TestHasher_Tier4P_DifferInLastSegment(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	size := 4 << 20
	lData := make([]byte, size)
	uData := make([]byte, size)
	for i := range lData { lData[i] = byte(i); uData[i] = byte(i) }
	uData[size-1] ^= 0xFF // flip last byte

	lAbs := filepath.Join(lower, "tail.bin")
	uAbs := filepath.Join(upper, "tail.bin")
	require.NoError(t, os.WriteFile(lAbs, lData, 0o644))
	require.NoError(t, os.WriteFile(uAbs, uData, 0o644))
	writePastMtime(t, lAbs)

	h := newHasher(1<<20, 4)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "differ in last byte must be not equal")
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier threshold routing
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_ThresholdRouting_SmallUsesSequential(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	data := make([]byte, 512*1024) // 512 KiB — below 1 MiB threshold
	lAbs, uAbs := writeFilePair(t, lower, upper, "small.bin", string(data), string(data))
	writePastMtime(t, lAbs)

	h := newHasher(1<<20, 4)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq)
}

func TestHasher_ThresholdRouting_LargeUsesParallel(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	data := make([]byte, 2<<20) // 2 MiB — at 1 MiB threshold → parallel
	lAbs, uAbs := writeFilePair(t, lower, upper, "large.bin", string(data), string(data))
	writePastMtime(t, lAbs)

	h := newHasher(1<<20, 4)
	lInfo, _ := os.Lstat(lAbs)
	uInfo, _ := os.Lstat(uAbs)
	eq, err := h.Equal(lAbs, uAbs, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq)
}

// ─────────────────────────────────────────────────────────────────────────────
// Symlink comparison
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_Symlinks_SameTarget_Equal(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	lLink := filepath.Join(lower, "link")
	uLink := filepath.Join(upper, "link")
	require.NoError(t, os.Symlink("/etc/hosts", lLink))
	require.NoError(t, os.Symlink("/etc/hosts", uLink))

	h := newHasher(1, 1)
	lInfo, _ := os.Lstat(lLink)
	uInfo, _ := os.Lstat(uLink)
	eq, err := h.Equal(lLink, uLink, lInfo, uInfo)
	require.NoError(t, err)
	assert.True(t, eq, "same symlink target must be equal")
}

func TestHasher_Symlinks_DifferentTarget_NotEqual(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	lLink := filepath.Join(lower, "link")
	uLink := filepath.Join(upper, "link")
	require.NoError(t, os.Symlink("/etc/hosts", lLink))
	require.NoError(t, os.Symlink("/etc/passwd", uLink))

	h := newHasher(1, 1)
	lInfo, _ := os.Lstat(lLink)
	uInfo, _ := os.Lstat(uLink)
	eq, err := h.Equal(lLink, uLink, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "different symlink targets must not be equal")
}

// ─────────────────────────────────────────────────────────────────────────────
// Type mismatch
// ─────────────────────────────────────────────────────────────────────────────

func TestHasher_TypeMismatch_FileVsDir_NotEqual(t *testing.T) {
	t.Parallel()
	lower, upper := t.TempDir(), t.TempDir()
	lFile := filepath.Join(lower, "x")
	uDir := filepath.Join(upper, "x")
	require.NoError(t, os.WriteFile(lFile, []byte("file"), 0o644))
	require.NoError(t, os.MkdirAll(uDir, 0o755))

	h := newHasher(1, 1)
	lInfo, _ := os.Lstat(lFile)
	uInfo, _ := os.Lstat(uDir)
	eq, err := h.Equal(lFile, uDir, lInfo, uInfo)
	require.NoError(t, err)
	assert.False(t, eq, "file vs dir must not be equal")
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmarks
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkHasher_Sequential_Equal_1MiB(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	data := make([]byte, 1<<20)
	_ = os.WriteFile(filepath.Join(lower, "f.bin"), data, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "f.bin"), data, 0o644)
	writePastMtime(&testing.T{}, filepath.Join(lower, "f.bin"))
	lInfo, _ := os.Lstat(filepath.Join(lower, "f.bin"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.bin"))

	h := newHasher(64<<20, 1) // above file size → always sequential
	b.SetBytes(int64(len(data)) * 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eq, _ := h.Equal(filepath.Join(lower, "f.bin"), filepath.Join(upper, "f.bin"), lInfo, uInfo)
		if !eq { b.Fatal("expected equal") }
	}
}

func BenchmarkHasher_Parallel_Equal_8MiB(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	data := make([]byte, 8<<20)
	for i := range data { data[i] = byte(i * 7) }
	_ = os.WriteFile(filepath.Join(lower, "f.bin"), data, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "f.bin"), data, 0o644)
	writePastMtime(&testing.T{}, filepath.Join(lower, "f.bin"))
	lInfo, _ := os.Lstat(filepath.Join(lower, "f.bin"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.bin"))

	h := newHasher(1<<20, 4)
	b.SetBytes(int64(len(data)) * 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eq, _ := h.Equal(filepath.Join(lower, "f.bin"), filepath.Join(upper, "f.bin"), lInfo, uInfo)
		if !eq { b.Fatal("expected equal") }
	}
}

func BenchmarkHasher_Parallel_EarlyExit_8MiB(b *testing.B) {
	lower, upper := b.TempDir(), b.TempDir()
	lData := make([]byte, 8<<20)
	uData := make([]byte, 8<<20)
	uData[0] = 0xFF
	_ = os.WriteFile(filepath.Join(lower, "f.bin"), lData, 0o644)
	_ = os.WriteFile(filepath.Join(upper, "f.bin"), uData, 0o644)
	writePastMtime(&testing.T{}, filepath.Join(lower, "f.bin"))
	lInfo, _ := os.Lstat(filepath.Join(lower, "f.bin"))
	uInfo, _ := os.Lstat(filepath.Join(upper, "f.bin"))

	h := newHasher(1<<20, 4)
	b.SetBytes(int64(len(lData)) * 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eq, _ := h.Equal(filepath.Join(lower, "f.bin"), filepath.Join(upper, "f.bin"), lInfo, uInfo)
		if eq { b.Fatal("expected unequal") }
	}
}

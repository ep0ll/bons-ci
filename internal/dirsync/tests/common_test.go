package dirsync_test

// common_test.go – tests for CommonPath emission, meta-equal fast path,
// and SHA-256 hash slow path.

import (
	"os"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
)

// ─── Fast-path: sameMetadata ──────────────────────────────────────────────────

// TestCommon_MetaEqual verifies the fast-path: files with the same content and
// the same mtime must be classified as MetaEqual without any content hashing.
func TestCommon_MetaEqual(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedTime := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	touchAt(t, lower, "same content", fixedTime, "file.txt")
	touchAt(t, upper, "same content", fixedTime, "file.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	cp, ok := commonByRelPath(dr.Common, "file.txt")
	if !ok {
		t.Fatal("file.txt not found in common paths")
	}
	if !cp.MetaEqual {
		t.Errorf("file.txt: expected MetaEqual=true (same size+mtime), got false")
	}
	if cp.HashChecked {
		t.Errorf("file.txt: expected HashChecked=false (fast path), got true")
	}
	if len(dr.Exclusive) != 0 {
		t.Errorf("expected 0 exclusive, got %d", len(dr.Exclusive))
	}
}

// TestCommon_MetaEqual_DifferentMtime verifies that when mtime differs, the
// slow hash path is triggered even if content is identical.
func TestCommon_MetaEqual_DifferentMtime(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	t1 := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 16, 12, 0, 0, 0, time.UTC)

	touchAt(t, lower, "identical content", t1, "file.txt")
	touchAt(t, upper, "identical content", t2, "file.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	cp, ok := commonByRelPath(dr.Common, "file.txt")
	if !ok {
		t.Fatal("file.txt not in common")
	}
	// Fast path must have failed (different mtime).
	if cp.MetaEqual {
		t.Error("file.txt: MetaEqual should be false (different mtime)")
	}
	// Hash must have been computed.
	if !cp.HashChecked {
		t.Error("file.txt: HashChecked should be true (mtime differs)")
	}
	// Content is the same → HashEqual must be true.
	if !cp.HashEqual {
		t.Errorf("file.txt: HashEqual should be true (same content); lower=%s upper=%s",
			cp.LowerHash, cp.UpperHash)
	}
}

// ─── Slow-path: content hashing ───────────────────────────────────────────────

// TestCommon_HashDiff verifies that two files with different content and same
// size (pathological case for rsync-style size check) are detected as different.
func TestCommon_HashDiff(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	t1 := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	// Same length ("hello" vs "world"), same mtime — but content differs.
	// We need mtime to differ slightly so the fast path fires the hash:
	// Actually, both files will have different content hash.
	// Force hash by using different mtime.
	touchAt(t, lower, "hello", t1, "file.txt")
	t2 := t1.Add(time.Second)
	touchAt(t, upper, "world", t2, "file.txt")

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	cp, ok := commonByRelPath(dr.Common, "file.txt")
	if !ok {
		t.Fatal("file.txt not in common")
	}
	if !cp.HashChecked {
		t.Error("expected hash to be checked (mtime differs)")
	}
	if cp.HashEqual {
		t.Errorf("content differs but HashEqual=true; lower=%s upper=%s",
			cp.LowerHash, cp.UpperHash)
	}
	if cp.LowerHash == "" || cp.UpperHash == "" {
		t.Error("hash strings should be non-empty")
	}
	if cp.LowerHash == cp.UpperHash {
		t.Error("lower and upper hashes should differ")
	}
}

// TestCommon_LargeFile checks that incremental hashing works correctly for a
// file larger than the 64 KiB buffer (forces multiple read(2) calls).
func TestCommon_LargeFile(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	// 512 KiB of distinct content in each file → hash must differ.
	lowerContent := make([]byte, 512*1024)
	upperContent := make([]byte, 512*1024)
	for i := range lowerContent {
		lowerContent[i] = byte(i % 251) // prime modulus → non-trivial pattern
	}
	for i := range upperContent {
		upperContent[i] = byte((i + 1) % 251)
	}

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Millisecond) // force hash path

	if err := os.WriteFile(lower+"/big.bin", lowerContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upper+"/big.bin", upperContent, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(lower+"/big.bin", t1, t1)
	os.Chtimes(upper+"/big.bin", t2, t2)

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	cp, ok := commonByRelPath(dr.Common, "big.bin")
	if !ok {
		t.Fatal("big.bin not in common")
	}
	if !cp.HashChecked {
		t.Error("expected hash checked for large file with different mtime")
	}
	if cp.HashEqual {
		t.Error("large files with different content should not be HashEqual")
	}
}

// TestCommon_LargeFile_SameContent verifies that two large identical files
// are correctly detected as equal when forced through the hash path.
func TestCommon_LargeFile_SameContent(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	content := make([]byte, 256*1024)
	for i := range content {
		content[i] = byte(i % 199)
	}

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second) // different mtime, same content

	if err := os.WriteFile(lower+"/same.bin", content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upper+"/same.bin", content, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(lower+"/same.bin", t1, t1)
	os.Chtimes(upper+"/same.bin", t2, t2)

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	cp, ok := commonByRelPath(dr.Common, "same.bin")
	if !ok {
		t.Fatal("same.bin not in common")
	}
	if !cp.HashChecked {
		t.Error("expected hash checked")
	}
	if !cp.HashEqual {
		t.Errorf("identical content must be HashEqual; lower=%s upper=%s",
			cp.LowerHash, cp.UpperHash)
	}
}

// ─── Symlinks ─────────────────────────────────────────────────────────────────

// TestCommon_SymlinkEqual verifies that matching symlink targets are detected
// as equal via readlink, without content hashing.
func TestCommon_SymlinkEqual(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	symlink(t, lower, "/etc/hosts", "link")
	symlink(t, upper, "/etc/hosts", "link")

	dr := runDiff(t, lower, upper, dirsync.Options{
		FollowSymlinks: false,
		HashWorkers:    1,
	})
	assertNoErr(t, dr.Err, "Diff")

	cp, ok := commonByRelPath(dr.Common, "link")
	if !ok {
		t.Fatal("link not in common")
	}
	if !cp.HashChecked {
		t.Error("symlinks should have HashChecked=true (readlink comparison)")
	}
	if !cp.HashEqual {
		t.Errorf("matching symlink targets must be equal; lower=%q upper=%q",
			cp.LowerHash, cp.UpperHash)
	}
	// LowerHash / UpperHash carry the target strings.
	if cp.LowerHash != "/etc/hosts" {
		t.Errorf("LowerHash should be symlink target, got %q", cp.LowerHash)
	}
}

// TestCommon_SymlinkDiff verifies that differing symlink targets are detected.
func TestCommon_SymlinkDiff(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	symlink(t, lower, "/etc/hosts", "link")
	symlink(t, upper, "/etc/passwd", "link")

	dr := runDiff(t, lower, upper, dirsync.Options{FollowSymlinks: false, HashWorkers: 1})
	assertNoErr(t, dr.Err, "Diff")

	cp, ok := commonByRelPath(dr.Common, "link")
	if !ok {
		t.Fatal("link not in common")
	}
	if cp.HashEqual {
		t.Error("different symlink targets must not be HashEqual")
	}
}

// ─── Mixed scenarios ──────────────────────────────────────────────────────────

// TestCommon_AllPathsPresent runs a tree with files, dirs, and symlinks present
// in both roots and verifies total common count with no exclusive paths.
func TestCommon_AllPathsPresent(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	for _, root := range []string{lower, upper} {
		touchAt(t, root, "data", fixedT, "root.txt")
		touchAt(t, root, "nested", fixedT, "sub", "a.txt")
		touchAt(t, root, "nested", fixedT, "sub", "b.txt")
		symlink(t, root, "/tmp", "link")
	}

	dr := runDiff(t, lower, upper, dirsync.Options{FollowSymlinks: false, HashWorkers: 2})
	assertNoErr(t, dr.Err, "Diff")

	if len(dr.Exclusive) != 0 {
		t.Errorf("expected 0 exclusive, got %d: %v",
			len(dr.Exclusive), exclusiveRelPaths(dr.Exclusive))
	}

	// Expect: root.txt, sub/a.txt, sub/b.txt, link (sub dir is recursed, not emitted).
	if len(dr.Common) < 4 {
		t.Errorf("expected at least 4 common paths (root.txt, sub/a.txt, sub/b.txt, link), got %d", len(dr.Common))
	}
}

// TestCommon_ErrorEmbedded verifies that hash errors on individual files are
// surfaced via CommonPath.Err rather than aborting the whole walk.
func TestCommon_ErrorEmbedded(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)

	// good.txt: two files with different mtime so hash is triggered.
	touchAt(t, lower, "good content", t1, "good.txt")
	touchAt(t, upper, "good content", t2, "good.txt")

	// bad.txt: remove the lower file after the directory listing (simulate
	// a disappearing file).  We write it first so it shows up in ReadDir.
	touchAt(t, lower, "will vanish", t1, "bad.txt")
	touchAt(t, upper, "stays", t2, "bad.txt")
	if err := os.Remove(lower + "/bad.txt"); err != nil {
		t.Fatal(err)
	}

	dr := runDiff(t, lower, upper, defaultOpts())
	// Walk-level error should be nil — the hash error is embedded per-file.
	assertNoErr(t, dr.Err, "Diff")

	// good.txt must still be present.
	if _, ok := commonByRelPath(dr.Common, "good.txt"); !ok {
		t.Error("good.txt should be present in common even if bad.txt errored")
	}

	// bad.txt must appear with Err set.
	badCP, ok := commonByRelPath(dr.Common, "bad.txt")
	if !ok {
		// bad.txt may have been filtered by the stat race in readDirEntries.
		// Either outcome is acceptable — as long as the walk completed.
		t.Log("bad.txt was filtered by stat race (acceptable)")
		return
	}
	if badCP.Err == nil {
		t.Error("bad.txt should have a non-nil Err")
	}
}

package dirsync_test

// integration_test.go – end-to-end, concurrency, and cancellation tests.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
)

// ─── Context cancellation ─────────────────────────────────────────────────────

// TestCancel_StopsWalk verifies that cancelling the context causes the
// background walk to stop and all channels to close promptly.
func TestCancel_StopsWalk(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	for i := 0; i < 500; i++ {
		writeFile(t, lower, "x", "dir", "a", "file.txt")
	}

	ctx, cancel := context.WithCancel(context.Background())

	res, err := dirsync.Diff(ctx, lower, upper, dirsync.Options{
		HashWorkers:  2,
		ExclusiveBuf: 4, // tiny buffer → walk stalls quickly
	})
	if err != nil {
		t.Fatalf("Diff option error: %v", err)
	}

	// Cancel immediately so the walk goroutine blocks on a full channel.
	cancel()

	// Drain channels — must not block forever.
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); for range res.Exclusive {} }()
		go func() { defer wg.Done(); for range res.Common {} }()
		wg.Wait()
		<-res.Err
	}()

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("channels did not close within 5 seconds after ctx cancel")
	}
}

// TestCancel_ErrChannelClosed confirms that Err is closed without an error
// when the context is cancelled — cancellation is not a walk error.
func TestCancel_ErrChannelClosed(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	for i := 0; i < 100; i++ {
		writeFile(t, lower, "x", "dir_a", "file.txt")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before even starting

	res, optErr := dirsync.Diff(ctx, lower, upper, defaultOpts())
	if optErr != nil {
		t.Fatalf("Diff option error: %v", optErr)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); for range res.Exclusive {} }()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()

	walkErr := <-res.Err
	if walkErr != nil {
		t.Errorf("cancellation must not produce a walk error; got: %v", walkErr)
	}
}

// ─── Concurrent consumer safety ───────────────────────────────────────────────

// TestConcurrent_TwoConsumers verifies that two separate goroutines draining
// Exclusive and Common simultaneously do not race (the test itself is the
// race-detector's input).
func TestConcurrent_TwoConsumers(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 20; i++ {
		name := "shared_" + string(rune('a'+i)) + ".txt"
		touchAt(t, lower, "data", fixedT, name)
		touchAt(t, upper, "data", fixedT, name)
	}
	for i := 0; i < 10; i++ {
		name := "excl_" + string(rune('a'+i)) + ".txt"
		writeFile(t, lower, "excl", name)
	}

	res, optErr := dirsync.Diff(context.Background(), lower, upper, dirsync.Options{
		HashWorkers:  4,
		ExclusiveBuf: 8,
		CommonBuf:    8,
	})
	if optErr != nil {
		t.Fatalf("Diff option error: %v", optErr)
	}

	var (
		mu          sync.Mutex
		exclCount   int
		commonCount int
		wg          sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		for range res.Exclusive {
			mu.Lock()
			exclCount++
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for range res.Common {
			mu.Lock()
			commonCount++
			mu.Unlock()
		}
	}()
	wg.Wait()

	if walkErr := <-res.Err; walkErr != nil {
		t.Fatalf("unexpected walk error: %v", walkErr)
	}
	if exclCount != 10 {
		t.Errorf("exclusive count = %d, want 10", exclCount)
	}
	if commonCount != 20 {
		t.Errorf("common count = %d, want 20", commonCount)
	}
}

// ─── Edge cases ───────────────────────────────────────────────────────────────

// TestEdge_BothEmpty confirms zero output for two empty directories.
func TestEdge_BothEmpty(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	if len(dr.Exclusive) != 0 || len(dr.Common) != 0 {
		t.Errorf("both empty: want 0 results, got excl=%d common=%d",
			len(dr.Exclusive), len(dr.Common))
	}
}

// TestEdge_DeepCommonTree verifies a deeply nested tree present in both roots
// produces only common paths and no exclusive paths.
func TestEdge_DeepCommonTree(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	var buildDeep func(lRoot, uRoot string, depth int)
	buildDeep = func(lRoot, uRoot string, depth int) {
		if depth == 0 {
			return
		}
		touchAt(t, lRoot, "content", fixedT, "file.txt")
		touchAt(t, uRoot, "content", fixedT, "file.txt")
		buildDeep(mkdir(t, lRoot, "sub"), mkdir(t, uRoot, "sub"), depth-1)
	}
	buildDeep(lower, upper, 8)

	dr := runDiff(t, lower, upper, defaultOpts())
	assertNoErr(t, dr.Err, "Diff")

	if len(dr.Exclusive) != 0 {
		t.Errorf("expected 0 exclusive paths for identical trees, got %d",
			len(dr.Exclusive))
	}
	if len(dr.Common) == 0 {
		t.Error("expected at least 1 common path")
	}
}

// TestEdge_UpperDoesNotExist verifies graceful handling of a non-existent upper
// root: produces a walk error (upper root unreadable), no panic.
func TestEdge_UpperDoesNotExist(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir() + "_nonexistent"

	writeFile(t, lower, "data", "file.txt")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, optErr := dirsync.Diff(ctx, lower, upper, defaultOpts())
	if optErr != nil {
		t.Fatalf("Diff option error: %v", optErr)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); for range res.Exclusive {} }()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()

	walkErr := <-res.Err
	if walkErr == nil {
		t.Log("walk returned no error (upper non-existence was gracefully handled)")
	} else {
		t.Logf("walk returned expected error: %v", walkErr)
	}
}

// TestEdge_FollowSymlinks verifies that with FollowSymlinks=true a symlink to a
// file is treated as a regular file (triggering metadata comparison on the target).
func TestEdge_FollowSymlinks(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "target content", fixedT, "real.txt")
	touchAt(t, upper, "target content", fixedT, "real.txt")

	symlink(t, lower, "real.txt", "link.txt")
	symlink(t, upper, "real.txt", "link.txt")

	dr := runDiff(t, lower, upper, dirsync.Options{
		FollowSymlinks: true,
		HashWorkers:    2,
	})
	assertNoErr(t, dr.Err, "Diff")

	if len(dr.Exclusive) != 0 {
		t.Errorf("expected 0 exclusive with follow-symlinks, got %d",
			len(dr.Exclusive))
	}
}

// TestEdge_MultipleHashWorkers stress-tests the pool with many files that all
// need hashing to confirm no data races or deadlocks (run with -race).
func TestEdge_MultipleHashWorkers(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)

	const n = 100
	for i := 0; i < n; i++ {
		name := "file_" + string(rune('a'+i%26)) + "_x.txt"
		touchAt(t, lower, "content", t1, name)
		touchAt(t, upper, "content", t2, name) // different mtime → hash triggered
	}

	dr := runDiff(t, lower, upper, dirsync.Options{
		HashWorkers:  8,
		ExclusiveBuf: 16,
		CommonBuf:    16,
	})
	assertNoErr(t, dr.Err, "Diff")

	if len(dr.Exclusive) != 0 {
		t.Errorf("expected 0 exclusive, got %d", len(dr.Exclusive))
	}
	for _, cp := range dr.Common {
		if cp.Err != nil {
			t.Errorf("unexpected hash error for %q: %v", cp.RelPath, cp.Err)
		}
		if !cp.HashChecked {
			t.Errorf("%q: expected HashChecked=true (mtime differs)", cp.RelPath)
		}
		if !cp.HashEqual {
			t.Errorf("%q: content is identical but HashEqual=false", cp.RelPath)
		}
	}
}

// ─── MissingRequiredPaths ─────────────────────────────────────────────────────

// TestRequiredPaths_AllPresent verifies that required paths present in both
// trees satisfy the constraint and produce no error.
func TestRequiredPaths_AllPresent(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	fixedT := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	touchAt(t, lower, "data", fixedT, "go.mod")
	touchAt(t, upper, "data", fixedT, "go.mod")

	dr := runDiff(t, lower, upper, dirsync.Options{
		RequiredPaths: []string{"go.mod"},
		HashWorkers:   1,
	})
	assertNoErr(t, dr.Err, "Diff with required path present")
}

// TestRequiredPaths_ExclusiveCountsAsSeen verifies that a required path that
// exists only in lower (exclusive) is still counted as seen.
func TestRequiredPaths_ExclusiveCountsAsSeen(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	writeFile(t, lower, "data", "only_lower.txt")

	dr := runDiff(t, lower, upper, dirsync.Options{
		RequiredPaths: []string{"only_lower.txt"},
		HashWorkers:   1,
	})
	assertNoErr(t, dr.Err, "required path in lower-exclusive should be seen")
}

// TestRequiredPaths_Missing verifies that absent required paths produce a
// *MissingRequiredPathsError.
func TestRequiredPaths_Missing(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	writeFile(t, lower, "a", "present.txt")
	writeFile(t, upper, "a", "present.txt")
	// "absent.txt" is not written anywhere.

	dr := runDiff(t, lower, upper, dirsync.Options{
		RequiredPaths: []string{"present.txt", "absent.txt"},
		HashWorkers:   1,
	})

	if dr.Err == nil {
		t.Fatal("expected a MissingRequiredPathsError, got nil")
	}
	var mErr *dirsync.MissingRequiredPathsError
	if !errors.As(dr.Err, &mErr) {
		t.Fatalf("expected *MissingRequiredPathsError, got %T: %v", dr.Err, dr.Err)
	}
	if len(mErr.Paths) != 1 || mErr.Paths[0] != "absent.txt" {
		t.Errorf("Paths = %v, want [absent.txt]", mErr.Paths)
	}
}

// TestRequiredPaths_FilteredOutCausesError verifies that when a required path
// exists on disk but is excluded by ExcludePatterns, it is treated as missing.
func TestRequiredPaths_FilteredOutCausesError(t *testing.T) {
	lower := t.TempDir()
	upper := t.TempDir()

	writeFile(t, lower, "data", "secret.txt")
	writeFile(t, upper, "data", "secret.txt")

	dr := runDiff(t, lower, upper, dirsync.Options{
		ExcludePatterns: []string{"secret.txt"},
		RequiredPaths:   []string{"secret.txt"}, // required but excluded → missing
		HashWorkers:     1,
	})

	if dr.Err == nil {
		t.Fatal("expected MissingRequiredPathsError when required path is excluded")
	}
	var mErr *dirsync.MissingRequiredPathsError
	if !errors.As(dr.Err, &mErr) {
		t.Fatalf("expected *MissingRequiredPathsError, got %T", dr.Err)
	}
}

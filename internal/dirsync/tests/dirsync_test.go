package dirsync_test

// testutil_test.go – shared test helpers.
//
// All helpers follow a consistent pattern:
//   - Accept *testing.T as the first argument.
//   - Call t.Helper() to keep error lines pointing at the call site.
//   - Use t.Fatal / t.Fatalf for unrecoverable setup failures.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
)

// ─── Directory / file builders ────────────────────────────────────────────────

// mkdir creates a directory (and all parents) inside root.
func mkdir(t *testing.T, root string, rel ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{root}, rel...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", p, err)
	}
	return p
}

// writeFile writes content to root/rel… creating parent directories.
func writeFile(t *testing.T, root string, content string, rel ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{root}, rel...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir parent %q: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", p, err)
	}
	return p
}

// symlink creates a symbolic link at root/rel pointing to target.
func symlink(t *testing.T, root, target string, rel ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{root}, rel...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir parent %q: %v", filepath.Dir(p), err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatalf("symlink %q -> %q: %v", p, target, err)
	}
	return p
}

// touchAt writes content to a file then sets its mtime via os.Chtimes so tests
// that depend on the mtime fast-path are deterministic.
func touchAt(t *testing.T, root string, content string, mtime time.Time, rel ...string) string {
	t.Helper()
	p := writeFile(t, root, content, rel...)
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes %q: %v", p, err)
	}
	return p
}

// ─── Diff runner ──────────────────────────────────────────────────────────────

// diffResult is a collected snapshot of all streamed Diff output.
type diffResult struct {
	Exclusive []dirsync.ExclusivePath
	Common    []dirsync.CommonPath
	Err       error
}

// runDiff executes Diff synchronously (blocks until all channels are drained)
// and returns a collected snapshot.
func runDiff(t *testing.T, lower, upper string, opts dirsync.Options) diffResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res := dirsync.Diff(ctx, lower, upper, opts)

	var (
		dr diffResult
		mu sync.Mutex
		wg sync.WaitGroup
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		for ep := range res.Exclusive {
			mu.Lock()
			dr.Exclusive = append(dr.Exclusive, ep)
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		for cp := range res.Common {
			mu.Lock()
			dr.Common = append(dr.Common, cp)
			mu.Unlock()
		}
	}()

	wg.Wait()
	dr.Err = <-res.Err
	return dr
}

// ─── Assertion helpers ────────────────────────────────────────────────────────

// assertNoErr fails the test immediately if err != nil.
func assertNoErr(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", msg, err)
	}
}

// exclusiveRelPaths extracts the RelPath of every ExclusivePath in a slice,
// returning them in the order they were appended.
func exclusiveRelPaths(eps []dirsync.ExclusivePath) []string {
	out := make([]string, len(eps))
	for i, ep := range eps {
		out[i] = ep.RelPath
	}
	return out
}

// containsRelPath reports whether any ExclusivePath in eps has RelPath == rel.
func containsRelPath(eps []dirsync.ExclusivePath, rel string) bool {
	for _, ep := range eps {
		if ep.RelPath == rel {
			return true
		}
	}
	return false
}

// commonByRelPath returns the first CommonPath whose RelPath matches rel,
// and a bool indicating whether it was found.
func commonByRelPath(cps []dirsync.CommonPath, rel string) (dirsync.CommonPath, bool) {
	for _, cp := range cps {
		if cp.RelPath == rel {
			return cp, true
		}
	}
	return dirsync.CommonPath{}, false
}

// defaultOpts returns a minimal Options suitable for unit tests.
func defaultOpts() dirsync.Options {
	return dirsync.Options{HashWorkers: 2, ExclusiveBuf: 64, CommonBuf: 64}
}

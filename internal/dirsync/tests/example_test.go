package dirsync_test

// example_test.go – runnable Go examples that appear in `go doc dirsync`.
//
// Each Example function is compiled and run by `go test`; the output is
// compared against the `// Output:` comment.  They therefore serve as
// both documentation AND regression tests.
//
// Filesystem helpers from testutil_test.go are available because this file
// is in the same package (dirsync_test).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bons/bons-ci/internal/dirsync"
)

// ExampleDiff_exclusive demonstrates finding paths that exist only in the
// lower directory and would need to be deleted.
func ExampleDiff_exclusive() {
	lower, upper := makeDirs()
	defer os.RemoveAll(lower)
	defer os.RemoveAll(upper)

	// lower has two files; upper has none.
	os.WriteFile(lower+"/alpha.txt", []byte("a"), 0o644)
	os.WriteFile(lower+"/beta.txt", []byte("b"), 0o644)

	res, err := dirsync.Diff(context.Background(), lower, upper, dirsync.Options{
		HashWorkers:  1,
		ExclusiveBuf: 8,
	})
	if err != nil {
		fmt.Println("options error:", err)
		return
	}

	var exclusive []string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ep := range res.Exclusive {
			exclusive = append(exclusive, ep.RelPath)
		}
	}()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()
	if err := <-res.Err; err != nil {
		fmt.Println("walk error:", err)
		return
	}

	sort.Strings(exclusive)
	for _, p := range exclusive {
		fmt.Println(p)
	}
	// Output:
	// alpha.txt
	// beta.txt
}

// ExampleDiff_common demonstrates comparing files present in both trees.
func ExampleDiff_common() {
	lower, upper := makeDirs()
	defer os.RemoveAll(lower)
	defer os.RemoveAll(upper)

	// Both trees have the same file at the same mtime → MetaEqual.
	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	os.WriteFile(lower+"/same.txt", []byte("hello"), 0o644)
	os.WriteFile(upper+"/same.txt", []byte("hello"), 0o644)
	os.Chtimes(lower+"/same.txt", fixed, fixed)
	os.Chtimes(upper+"/same.txt", fixed, fixed)

	res, err := dirsync.Diff(context.Background(), lower, upper, dirsync.Options{
		HashWorkers: 1,
		CommonBuf:   8,
	})
	if err != nil {
		fmt.Println("options error:", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); for range res.Exclusive {} }()
	go func() {
		defer wg.Done()
		for cp := range res.Common {
			verdict := "changed"
			if cp.MetaEqual {
				verdict = "unchanged"
			}
			fmt.Printf("%s: %s\n", cp.RelPath, verdict)
		}
	}()
	wg.Wait()
	<-res.Err
	// Output:
	// same.txt: unchanged
}

// ExampleDiff_exclude demonstrates suppressing paths via ExcludePatterns.
// The vendor/ directory is excluded entirely (FilterPrune stops recursion).
func ExampleDiff_exclude() {
	lower, upper := makeDirs()
	defer os.RemoveAll(lower)
	defer os.RemoveAll(upper)

	// lower-only files.
	os.MkdirAll(lower+"/vendor/pkg", 0o755)
	os.WriteFile(lower+"/vendor/pkg/lib.go", []byte("x"), 0o644)
	os.WriteFile(lower+"/main.go", []byte("x"), 0o644)

	res, err := dirsync.Diff(context.Background(), lower, upper, dirsync.Options{
		ExcludePatterns: []string{"vendor"},
		HashWorkers:     1,
		ExclusiveBuf:    8,
	})
	if err != nil {
		fmt.Println("options error:", err)
		return
	}

	var exclusive []string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ep := range res.Exclusive {
			exclusive = append(exclusive, ep.RelPath)
		}
	}()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()
	<-res.Err

	sort.Strings(exclusive)
	for _, p := range exclusive {
		fmt.Println(p)
	}
	// Output:
	// main.go
}

// ExampleDiff_include demonstrates restricting output to matching files.
// Non-matching directories are still traversed so their children are checked.
func ExampleDiff_include() {
	lower, upper := makeDirs()
	defer os.RemoveAll(lower)
	defer os.RemoveAll(upper)

	// lower-only: src/main.go (matches *.go) and src/README.md (does not).
	os.MkdirAll(lower+"/src", 0o755)
	os.WriteFile(lower+"/src/main.go", []byte("x"), 0o644)
	os.WriteFile(lower+"/src/README.md", []byte("x"), 0o644)

	res, err := dirsync.Diff(context.Background(), lower, upper, dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		HashWorkers:     1,
		ExclusiveBuf:    8,
	})
	if err != nil {
		fmt.Println("options error:", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ep := range res.Exclusive {
			fmt.Println(ep.RelPath)
		}
	}()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()
	<-res.Err
	// Output:
	// src/main.go
}

// ExampleDiff_requiredPaths demonstrates RequiredPaths asserting that specific
// paths appear in output.  Missing paths produce a *MissingRequiredPathsError.
func ExampleDiff_requiredPaths() {
	lower, upper := makeDirs()
	defer os.RemoveAll(lower)
	defer os.RemoveAll(upper)

	// lower has go.mod but not go.sum.
	os.WriteFile(lower+"/go.mod", []byte("module example"), 0o644)

	res, err := dirsync.Diff(context.Background(), lower, upper, dirsync.Options{
		RequiredPaths: []string{"go.mod", "go.sum"},
		HashWorkers:   1,
		ExclusiveBuf:  8,
	})
	if err != nil {
		fmt.Println("options error:", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); for range res.Exclusive {} }()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()

	if walkErr := <-res.Err; walkErr != nil {
		var mErr *dirsync.MissingRequiredPathsError
		if errors.As(walkErr, &mErr) {
			fmt.Println("missing:", mErr.Paths[0])
		}
	}
	// Output:
	// missing: go.sum
}

// ExampleDiff_customFilter demonstrates injecting a custom PathFilter via
// Options.Filter.  The custom filter blocks any path containing "secret".
func ExampleDiff_customFilter() {
	lower, upper := makeDirs()
	defer os.RemoveAll(lower)
	defer os.RemoveAll(upper)

	os.WriteFile(lower+"/public.txt", []byte("x"), 0o644)
	os.WriteFile(lower+"/secret.key", []byte("x"), 0o644)

	res, err := dirsync.Diff(context.Background(), lower, upper, dirsync.Options{
		Filter:       secretFilter{},
		HashWorkers:  1,
		ExclusiveBuf: 8,
	})
	if err != nil {
		fmt.Println("options error:", err)
		return
	}

	var exclusive []string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ep := range res.Exclusive {
			exclusive = append(exclusive, ep.RelPath)
		}
	}()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()
	<-res.Err

	sort.Strings(exclusive)
	for _, p := range exclusive {
		fmt.Println(p)
	}
	// Output:
	// public.txt
}

// ExampleNewCompositeFilter demonstrates composing a custom PathFilter with
// the built-in pattern filter using NewCompositeFilter.
func ExampleNewCompositeFilter() {
	lower, upper := makeDirs()
	defer os.RemoveAll(lower)
	defer os.RemoveAll(upper)

	os.WriteFile(lower+"/main.go", []byte("x"), 0o644)
	os.WriteFile(lower+"/secret.go", []byte("x"), 0o644)
	os.WriteFile(lower+"/README.md", []byte("x"), 0o644)

	// IncludePatterns keeps only *.go; secretFilter excludes "secret.*".
	// NewCompositeFilter gives secretFilter veto power over the include filter.
	opts := dirsync.Options{
		AllowWildcards:  true,
		IncludePatterns: []string{"*.go"},
		HashWorkers:     1,
		ExclusiveBuf:    8,
	}
	builtin, err := dirsync.BuildFilter(opts)
	if err != nil {
		fmt.Println("build error:", err)
		return
	}
	opts.Filter = dirsync.NewCompositeFilter(secretFilter{}, builtin)
	// Clear patterns — already baked into builtin.
	opts.IncludePatterns = nil
	opts.AllowWildcards = false

	res, diffErr := dirsync.Diff(context.Background(), lower, upper, opts)
	if diffErr != nil {
		fmt.Println("options error:", diffErr)
		return
	}

	var exclusive []string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ep := range res.Exclusive {
			exclusive = append(exclusive, ep.RelPath)
		}
	}()
	go func() { defer wg.Done(); for range res.Common {} }()
	wg.Wait()
	<-res.Err

	sort.Strings(exclusive)
	for _, p := range exclusive {
		fmt.Println(p)
	}
	// Output:
	// main.go
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// makeDirs creates two temporary directories and returns their paths.
// Callers are responsible for calling os.RemoveAll on both.
func makeDirs() (lower, upper string) {
	lower, _ = os.MkdirTemp("", "dirsync_lower_*")
	upper, _ = os.MkdirTemp("", "dirsync_upper_*")
	return
}

// secretFilter is a PathFilter that suppresses any entry whose base name
// contains the word "secret".
type secretFilter struct{}

func (secretFilter) Decide(relPath string, isDir bool) dirsync.FilterDecision {
	base := relPath
	for i := len(relPath) - 1; i >= 0; i-- {
		if relPath[i] == '/' || relPath[i] == '\\' {
			base = relPath[i+1:]
			break
		}
	}
	for i := 0; i <= len(base)-6; i++ {
		if base[i:i+6] == "secret" {
			if isDir {
				return dirsync.FilterPrune
			}
			return dirsync.FilterSkip
		}
	}
	return dirsync.FilterAllow
}

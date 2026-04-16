package fshash_test

// example_test.go — runnable godoc examples for pkg/fshash.
// Each Example* function is a compilable, verified example that appears
// in the generated documentation.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bons/bons-ci/pkg/fshash"
	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── Basic usage ───────────────────────────────────────────────────────────────

func ExampleChecksummer_Sum() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello, world"), 0o644)

	cs, _ := fshash.New(
		fshash.WithAlgorithm(fshash.SHA256),
		fshash.WithMetadata(fshash.MetaNone), // content-only, no mode/size
	)
	res, _ := cs.Sum(context.Background(), root)
	fmt.Printf("digest length: %d bytes\n", len(res.Digest))
	// Output: digest length: 32 bytes
}

func ExampleFileDigest() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "data.txt")
	os.WriteFile(p, []byte("hello"), 0o644)

	dgst, _ := fshash.FileDigest(context.Background(), p,
		fshash.WithMetadata(fshash.MetaNone))
	fmt.Printf("SHA-256 of 'hello': %x\n", dgst)
	// Output: SHA-256 of 'hello': 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
}

func ExampleHashReader() {
	dgst, _ := fshash.HashReader(
		context.Background(),
		bytes.NewReader([]byte("")),
	)
	fmt.Printf("SHA-256 of empty input: %x\n", dgst)
	// Output: SHA-256 of empty input: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
}

// ── Algorithm selection ───────────────────────────────────────────────────────

func ExampleWithAlgorithm() {
	data := bytes.NewReader([]byte("benchmark me"))

	// Blake3: best crypto throughput (~2.5 GB/s)
	b3, _ := fshash.HashReader(context.Background(), data,
		fshash.WithAlgorithm(fshash.Blake3))

	data.Seek(0, 0) //nolint:errcheck
	// XXHash3: fastest non-crypto (~40 GB/s)
	xx3, _ := fshash.HashReader(context.Background(), data,
		fshash.WithAlgorithm(fshash.XXHash3))

	fmt.Printf("Blake3 bytes: %d\n", len(b3))   // 32
	fmt.Printf("XXHash3 bytes: %d\n", len(xx3)) // 8
	// Output:
	// Blake3 bytes: 32
	// XXHash3 bytes: 8
}

// ── Filters ───────────────────────────────────────────────────────────────────

func ExampleExcludeNames() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref:"), 0o644)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)

	cs, _ := fshash.New(
		fshash.WithFilter(fshash.ExcludeNames(".git", "vendor")),
		fshash.WithCollectEntries(true),
		fshash.WithMetadata(fshash.MetaNone),
	)
	res, _ := cs.Sum(context.Background(), root)

	for _, e := range res.Entries {
		if e.Kind == fshash.KindFile {
			fmt.Println(e.RelPath)
		}
	}
	// Output: main.go
}

func ExampleChainFilters() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("code"), 0o644)
	os.WriteFile(filepath.Join(root, "main.tmp"), []byte("temp"), 0o644)
	os.WriteFile(filepath.Join(root, "debug.log"), []byte("log"), 0o644)

	cs, _ := fshash.New(
		fshash.WithFilter(fshash.ChainFilters(
			fshash.ExcludePatterns("*.tmp"),
			fshash.ExcludePatterns("*.log"),
		)),
		fshash.WithCollectEntries(true),
		fshash.WithMetadata(fshash.MetaNone),
	)
	res, _ := cs.Sum(context.Background(), root)

	for _, e := range res.Entries {
		if e.Kind == fshash.KindFile {
			fmt.Println(e.RelPath)
		}
	}
	// Output: main.go
}

// ── Caching ───────────────────────────────────────────────────────────────────

func ExampleMtimeCache() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "data.bin"), []byte("content"), 0o644)

	cache := &fshash.MtimeCache{}
	cs, _ := fshash.NewCachingChecksummer(cache)

	// First Sum: computes digest and populates cache.
	r1, _ := cs.Sum(context.Background(), root)

	// Second Sum: served entirely from cache (no disk read).
	r2, _ := cs.Sum(context.Background(), root)

	fmt.Println("digests match:", r1.Equal(r2))
	fmt.Println("cache entries:", cache.Len())
	// Output:
	// digests match: true
	// cache entries: 1
}

// ── Diff and compare ──────────────────────────────────────────────────────────

func ExampleChecksummer_Diff() {
	rootA, _ := os.MkdirTemp("", "exampleA")
	rootB, _ := os.MkdirTemp("", "exampleB")
	defer os.RemoveAll(rootA)
	defer os.RemoveAll(rootB)

	os.WriteFile(filepath.Join(rootA, "common"), []byte("same"), 0o644)
	os.WriteFile(filepath.Join(rootA, "removed"), []byte("gone"), 0o644)
	os.WriteFile(filepath.Join(rootB, "common"), []byte("same"), 0o644)
	os.WriteFile(filepath.Join(rootB, "added"), []byte("new"), 0o644)

	cs, _ := fshash.New(fshash.WithMetadata(fshash.MetaNone))
	diff, _ := cs.Diff(context.Background(), rootA, rootB)

	fmt.Println("added:", diff.Added)
	fmt.Println("removed:", diff.Removed)
	// Output:
	// added: [added]
	// removed: [removed]
}

func ExampleDiffResult_String() {
	d := fshash.DiffResult{
		Added:    []string{"new.go"},
		Modified: []string{"main.go", "util.go"},
	}
	fmt.Println(d.String())
	// Output: 1 added, 2 modified
}

// ── Snapshot ──────────────────────────────────────────────────────────────────

func ExampleTakeSnapshot() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "build.go"), []byte("package main"), 0o644)

	snap, _ := fshash.TakeSnapshot(context.Background(), root,
		fshash.WithAlgorithm(fshash.Blake3),
		fshash.WithMetadata(fshash.MetaNone),
	)

	fmt.Println("algorithm:", snap.Algorithm)
	fmt.Println("has entries:", len(snap.Entries) > 0)
	fmt.Println("verify passes:", snap.VerifyAgainst(context.Background(), root) == nil)
	// Output:
	// algorithm: blake3
	// has entries: true
	// verify passes: true
}

// ── Walk (streaming, bottom-up) ───────────────────────────────────────────────

func ExampleChecksummer_Walk() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta"), 0o644)

	cs, _ := fshash.New(fshash.WithMetadata(fshash.MetaNone))

	var fileCount int
	cs.Walk(context.Background(), root, func(e fshash.EntryResult) error { //nolint:errcheck
		if e.Kind == fshash.KindFile {
			fileCount++
		}
		return nil
	})
	fmt.Println("files visited:", fileCount)
	// Output: files visited: 2
}

// ── SumStream (reactive) ──────────────────────────────────────────────────────

func ExampleChecksummer_SumStream() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "x.go"), []byte("code"), 0o644)

	cs, _ := fshash.New(fshash.WithMetadata(fshash.MetaNone))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	for range cs.SumStream(ctx, root).Chan() {
		count++
	}
	fmt.Println("entries emitted:", count) // x.go + "." (root dir) = 2
	// Output: entries emitted: 2
}

// ── Watcher (reactive, multi-subscriber) ─────────────────────────────────────

func ExampleNewWatcher() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"v":1}`), 0o644)

	cs, _ := fshash.New()
	w := fshash.NewWatcher(cs, root,
		fshash.WithWatchInterval(50*time.Millisecond),
	)

	// Subscribe before starting — zero race window.
	id, events := w.Events().Subscribe(4)
	defer w.Events().Unsubscribe(id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Watch(ctx) //nolint:errcheck

	// Trigger a change.
	time.Sleep(80 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"v":2}`), 0o644)

	select {
	case evt := <-events:
		fmt.Println("change detected:", evt.Path != "")
	case <-time.After(3 * time.Second):
		fmt.Println("timeout")
	}
	// Output: change detected: true
}

// ── Registry (custom algorithm) ───────────────────────────────────────────────

func ExampleRegistry_Register() {
	// Register a fast non-crypto algorithm in an isolated registry.
	reg := core.NewRegistry()
	reg.Register(core.XXHash3, core.DefaultRegistry.MustGet(core.XXHash3))
	reg.Register(core.Blake3, core.DefaultRegistry.MustGet(core.Blake3))

	h, _ := reg.Get(core.XXHash3)
	hh := h.New()
	hh.Write([]byte("test"))
	fmt.Printf("XXHash3 digest size: %d bytes\n", len(hh.Sum(nil)))
	// Output: XXHash3 digest size: 8 bytes
}

// ── SumMany ───────────────────────────────────────────────────────────────────

func ExampleChecksummer_SumMany() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	paths := make([]string, 3)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		os.WriteFile(p, []byte(fmt.Sprintf("file %d", i)), 0o644)
		paths[i] = p
	}

	cs, _ := fshash.New(fshash.WithWorkers(3))
	results, errs := cs.SumMany(context.Background(), paths)

	for i, err := range errs {
		if err != nil {
			fmt.Printf("error on file %d: %v\n", i, err)
		}
	}
	fmt.Printf("computed %d digests\n", len(results))
	// Output: computed 3 digests
}

// ── Canonicalize ─────────────────────────────────────────────────────────────

func ExampleChecksummer_Canonicalize() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)

	cs, _ := fshash.New(fshash.WithMetadata(fshash.MetaNone))
	var buf bytes.Buffer
	cs.Canonicalize(context.Background(), root, &buf) //nolint:errcheck

	entries, _ := fshash.ReadCanonical(&buf)
	fmt.Printf("lines: %d\n", len(entries))
	fmt.Printf("last kind: %s\n", entries[len(entries)-1].Kind)
	// Output:
	// lines: 2
	// last kind: root
}

// ── ChangeFeed ────────────────────────────────────────────────────────────────

func ExampleChecksummer_ChangeFeed() {
	root, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(root)
	os.WriteFile(filepath.Join(root, "version.txt"), []byte("1.0"), 0o644)

	cs, _ := fshash.New(fshash.WithMetadata(fshash.MetaNone))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	feed := cs.ChangeFeed(ctx, root, 30*time.Millisecond)

	// First poll: all entries arrive with PrevDigest==nil.
	var firstCount int
	timeout := time.After(200 * time.Millisecond)
loop:
	for {
		select {
		case e, ok := <-feed.Chan():
			if !ok {
				break loop
			}
			if e.PrevDigest == nil {
				firstCount++
			}
		case <-timeout:
			break loop
		}
	}
	fmt.Printf("first-poll entries: %d\n", firstCount) // version.txt + "." = 2 or more
	// Output: first-poll entries: 2
}

// ── core.Stream pipeline ──────────────────────────────────────────────────────

func ExampleMapStream() {
	ctx := context.Background()
	numbers := core.NewStream[int](ctx, 5)
	for _, v := range []int{1, 2, 3} {
		numbers.Emit(v)
	}
	numbers.Close()

	squares := core.MapStream(ctx, numbers, func(v int) int { return v * v })
	for v := range squares.Chan() {
		fmt.Printf("%d ", v)
	}
	fmt.Println()
	// Output: 1 4 9
}

func ExampleFilterStream() {
	ctx := context.Background()
	src := core.NewStream[int](ctx, 10)
	for i := 1; i <= 6; i++ {
		src.Emit(i)
	}
	src.Close()

	evens := core.FilterStream(ctx, src, func(v int) bool { return v%2 == 0 })
	for v := range evens.Chan() {
		fmt.Printf("%d ", v)
	}
	fmt.Println()
	// Output: 2 4 6
}

func ExampleEventBus() {
	bus := core.NewEventBus[string]()
	defer bus.Close()

	id1, ch1 := bus.Subscribe(4)
	id2, ch2 := bus.Subscribe(4)
	defer bus.Unsubscribe(id1)
	defer bus.Unsubscribe(id2)

	bus.Publish("hello")
	bus.Publish("world")

	// Each subscriber receives both messages.
	fmt.Println(<-ch1, <-ch1)
	fmt.Println(<-ch2, <-ch2)
	// Output:
	// hello world
	// hello world
}

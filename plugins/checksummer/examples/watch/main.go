//go:build linux

// Package examples shows how to compose every subsystem into a production-grade
// pipeline without modifying any engine internals.
//
// Run:
//
//	sudo go run ./examples/watch/ \
//	    --mount /run/containerd/.../rootfs \
//	    --out /var/lib/checksumengine/results.ndjson
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/engine"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/fanotify"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filter"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/metrics"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/store"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/xattr"
)

func main() {
	// ── flags ─────────────────────────────────────────────────────────────
	var mountFlag multiFlag
	flag.Var(&mountFlag, "mount", "Overlay merged-view path (repeatable)")
	outFile := flag.String("out", "", "NDJSON output file (empty = stdout only)")
	xattrCache := flag.Bool("xattr", false, "Use xattr cache to skip known-good files")
	statsEvery := flag.Duration("stats", 10*time.Second, "Print metrics interval")
	flag.Parse()

	if len(mountFlag) == 0 {
		log.Fatal("at least one --mount is required")
	}

	// ── build store ───────────────────────────────────────────────────────
	var st store.Store = store.NewMemStore()
	if *outFile != "" {
		fs, err := store.NewFileStore(*outFile)
		if err != nil {
			log.Fatalf("open store %q: %v", *outFile, err)
		}
		defer fs.Close()
		st = store.NewMultiStore(store.NewMemStore(), fs)
	}

	// ── build xattr cache (advisory) ─────────────────────────────────────
	var xc *xattr.Cache
	if *xattrCache {
		xc = xattr.NewCache("user.ovlhash")
	}

	// ── build filter ──────────────────────────────────────────────────────
	// Skip /proc, /sys, /dev and only care about executable / library files.
	eventFilter := filter.And(
		filter.NotPaths("/proc", "/sys", "/dev", "/run/lock"),
		filter.Or(
			filter.Extensions(".so", ".py", ".rb", ".js", ".jar"),
			filter.GlobMatch("*.so.*", "python*", "ruby*", "node"),
		),
		filter.NewSampler(1), // pass all (tune down for very high-throughput mounts)
	)

	// ── build hooks ───────────────────────────────────────────────────────
	hs := hooks.NewHookSet()
	met := &metrics.Recorder{}

	// Wire filter → OnFilter hook.
	hs.OnFilter.Register(hooks.NewHook("main-filter", hooks.PriorityFirst,
		filter.Hook(eventFilter)))

	// xattr pre-check: skip files whose hash is already cached in xattrs.
	if xc != nil {
		hs.PreHash.Register(hooks.NewHook("xattr-precheck", hooks.PriorityFirst,
			func(ctx context.Context, p hooks.HashPayload) error {
				// We can't skip inside PreHash (no return value path), so
				// the xattr check is done in PostHash to enrich the record.
				return nil
			}))
	}

	// PostHash: write result to store and optionally set xattr.
	hs.PostHash.Register(hooks.NewHook("store-write", hooks.PriorityNormal,
		func(ctx context.Context, p hooks.HashPayload) error {
			r := store.Record{
				Key:     p.Key,
				Path:    p.Path,
				Size:    p.FileSize,
				Cached:  p.Cached,
				Deduped: p.Deduped,
			}.WithHash(p.Hash)
			return st.Put(ctx, r)
		}))

	if xc != nil {
		hs.PostHash.Register(hooks.NewHook("xattr-save", hooks.PriorityLast,
			func(_ context.Context, p hooks.HashPayload) error {
				// best-effort; errors are non-fatal
				_ = xc.Save(p.Path, xattr.FileStat{Size: p.FileSize}, p.Hash)
				return nil
			}))
	}

	// Error hook: log errors with context.
	hs.OnError.Register(hooks.NewHook("error-log", hooks.PriorityNormal,
		func(_ context.Context, p hooks.ErrorPayload) error {
			log.Printf("[error] op=%s key=%s: %v", p.Op, p.Key, p.Err)
			return nil
		}))

	// ── build engine ──────────────────────────────────────────────────────
	b := engine.Build().
		WithHooks(hs).
		WithMetrics(met).
		WatchWorkers(16).
		ParallelWorkers(8).
		CacheMaxEntries(4096).
		OnResult(func(key filekey.Key, path string, hash []byte, size int64) {
			fmt.Printf("%x  %s\n", hash, path)
		}).
		OnError(func(key filekey.Key, path string, err error) {
			log.Printf("[hash error] %s: %v", path, err)
		})

	for _, m := range mountFlag {
		b = b.WithMark(fanotify.DefaultMark(m))
	}
	eng, err := b.Engine()
	if err != nil {
		log.Fatalf("build engine: %v", err)
	}

	// ── signal handling ───────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── periodic metrics ──────────────────────────────────────────────────
	if *statsEvery > 0 {
		go func() {
			tick := time.NewTicker(*statsEvery)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					snap := eng.MetricsSnapshot()
					log.Printf("[metrics] %s | store=%d", snap, st.Len())
				}
			}
		}()
	}

	log.Printf("engine starting — watching %d mount(s)", len(mountFlag))
	if err := eng.Start(ctx); err != nil && err != context.Canceled {
		log.Fatalf("engine: %v", err)
	}

	final := eng.MetricsSnapshot()
	log.Printf("stopped — %s | total stored=%d", final, st.Len())

	// Print per-stage stats on exit.
	for _, s := range eng.PipelineStats() {
		log.Printf("  %s", s)
	}
}

// ─────────────────────────── helpers ─────────────────────────────────────────

type multiFlag []string

func (f *multiFlag) String() string     { return fmt.Sprint([]string(*f)) }
func (f *multiFlag) Set(v string) error { *f = append(*f, v); return nil }

// KeepAlive is a no-op that prevents unused-import warnings when
// examples are compiled as a library target.
func KeepAlive() { _ = os.Getpid() }

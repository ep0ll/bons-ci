//go:build linux

// cmd/checksumctl/main.go – command-line interface for the checksum engine.
//
// Usage:
//
//	# Watch file accesses on overlay mounts via fanotify (requires CAP_SYS_ADMIN):
//	sudo checksumctl watch --mount /run/containerd/.../rootfs --mount /run/nydus/.../mnt
//
//	# Hash a single file:
//	checksumctl hash /path/to/file
//	checksumctl hash --json /path/to/file
//
//	# Recursively hash all files in a directory:
//	checksumctl scan /var/lib/containerd/snapshots/base
//	checksumctl scan --workers 8 --json /overlay/lower
//
//	# Print system capability report:
//	checksumctl bench-report
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/engine"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/fanotify"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/metrics"
)

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "watch":
		cmdWatch(os.Args[2:])
	case "hash":
		cmdHash(os.Args[2:])
	case "scan":
		cmdScan(os.Args[2:])
	case "bench-report":
		cmdBenchReport()
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `checksumctl – overlayfs-aware BLAKE3/SHA-256 checksum engine

Commands:
  watch        Watch file accesses via fanotify and compute hashes (requires root)
  hash         Hash a single file
  scan         Recursively hash all files in a directory
  bench-report Print system capabilities for benchmark tuning

Global flags vary by command.  Run: checksumctl <command> -help

`)
}

// ─────────────────────────── watch ───────────────────────────────────────────

func cmdWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)

	var mounts multiStringFlag
	fs.Var(&mounts, "mount", "Overlay merged view to watch (repeatable)")

	workers := fs.Int("workers", 16, "Event processing workers")
	pworkers := fs.Int("parallel-workers", runtime.NumCPU(), "Parallel IO workers for large files")
	pchunk := fs.Int64("parallel-chunk", 2<<20, "Parallel IO chunk size (bytes)")
	small := fs.Int64("small-threshold", 8<<20, "Max size for sequential hashing")
	medium := fs.Int64("medium-threshold", 128<<20, "Max size for mmap hashing")
	cacheMax := fs.Int("cache-max", 4096, "Max cache entries per shard")
	cacheTTL := fs.Duration("cache-ttl", 0, "Cache TTL (0=no expiry)")
	jsonOut := fs.Bool("json", false, "Output results as JSON lines")
	verbose := fs.Bool("v", false, "Log every event")
	statsEvery := fs.Duration("stats", 10*time.Second, "Print metrics every interval (0=disable)")
	disableHandles := fs.Bool("no-handles", false, "Disable file handles (use stat fallback)")

	_ = fs.Parse(args)

	if len(mounts) == 0 {
		log.Fatal("watch: at least one --mount path required (e.g. --mount /run/containerd/.../rootfs)")
	}

	// Assemble marks.
	var marks []fanotify.Mark
	for _, m := range mounts {
		marks = append(marks, fanotify.DefaultMark(m))
	}

	// Hooks and metrics.
	hs := hooks.NewHookSet()
	met := &metrics.Recorder{}

	if *verbose {
		hs.OnEvent.Register(hooks.NewHook("verbose", hooks.PriorityLast,
			func(ctx context.Context, p hooks.EventPayload) error {
				log.Printf("[event] pid=%d mask=0x%x fd=%d path=%s", p.Pid, p.Mask, p.Fd, p.Path)
				return nil
			}))
	}

	printer := resultPrinter(*jsonOut)

	b := engine.Build().
		WithHooks(hs).
		WithMetrics(met).
		WatchWorkers(*workers).
		ParallelWorkers(*pworkers).
		ParallelChunkSize(*pchunk).
		SmallFileThreshold(*small).
		MediumFileThreshold(*medium).
		CacheMaxEntries(*cacheMax).
		CacheTTL(*cacheTTL).
		OnResult(printer).
		OnError(func(key filekey.Key, path string, err error) {
			log.Printf("[error] key=%s path=%s: %v", key, path, err)
		})

	if *disableHandles {
		b = b.DisableFileHandles()
	}
	for _, m := range marks {
		b = b.WithMark(m)
	}

	eng, err := b.Engine()
	if err != nil {
		log.Fatalf("build engine: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Periodic stats printer.
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
					log.Printf("[stats] %s", snap.String())
					for _, s := range eng.PipelineStats() {
						log.Printf("  %s", s)
					}
					log.Printf("  cache: %s", eng.CacheStats())
				}
			}
		}()
	}

	log.Printf("checksumctl watch: monitoring %d mount(s). Press Ctrl+C to stop.", len(marks))
	if err := eng.Start(ctx); err != nil && err != context.Canceled {
		log.Fatalf("engine: %v", err)
	}

	final := eng.MetricsSnapshot()
	log.Printf("checksumctl watch: stopped. Final stats: %s", final)
}

// ─────────────────────────── hash ────────────────────────────────────────────

func cmdHash(args []string) {
	fset := flag.NewFlagSet("hash", flag.ExitOnError)
	jsonOut := fset.Bool("json", false, "Output as JSON")
	_ = fset.Parse(args)

	if fset.NArg() == 0 {
		log.Fatal("hash: path argument required")
	}

	eng, err := engine.Build().Engine()
	if err != nil {
		log.Fatalf("build engine: %v", err)
	}

	ctx := context.Background()
	path := fset.Arg(0)

	hash, err := eng.HashPath(ctx, path)
	if err != nil {
		log.Fatalf("hash %q: %v", path, err)
	}

	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(hashResult{
			Path: path,
			Hash: hex.EncodeToString(hash),
			Algo: "sha256-stub", // change to "blake3" when real backend is wired
		})
	} else {
		fmt.Printf("%s  %s\n", hex.EncodeToString(hash), path)
	}
}

// ─────────────────────────── scan ────────────────────────────────────────────

func cmdScan(args []string) {
	fset := flag.NewFlagSet("scan", flag.ExitOnError)
	workers := fset.Int("workers", runtime.NumCPU(), "Parallel scan workers")
	jsonOut := fset.Bool("json", false, "Output as JSON lines")
	_ = fset.Parse(args)

	if fset.NArg() == 0 {
		log.Fatal("scan: directory argument required")
	}
	root := fset.Arg(0)

	eng, err := engine.Build().
		WatchWorkers(*workers).
		Engine()
	if err != nil {
		log.Fatalf("build engine: %v", err)
	}

	ctx := context.Background()
	printer := resultPrinter(*jsonOut)

	start := time.Now()
	var count, errCount int64

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		hash, herr := eng.HashPath(ctx, path)
		if herr != nil {
			log.Printf("[warn] %s: %v", path, herr)
			errCount++
			return nil
		}
		info, _ := d.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		printer(filekey.Key{}, path, hash, size)
		count++
		return nil
	})
	if err != nil {
		log.Fatalf("scan: walk error: %v", err)
	}

	elapsed := time.Since(start)
	snap := eng.MetricsSnapshot()
	log.Printf("scan: %d files, %d errors, %s elapsed. %s", count, errCount, elapsed, snap)
}

// ─────────────────────────── bench-report ─────────────────────────────────────

func cmdBenchReport() {
	fmt.Println("System Capabilities Report")
	fmt.Println("══════════════════════════")
	fmt.Printf("  GOMAXPROCS  : %d\n", runtime.GOMAXPROCS(0))
	fmt.Printf("  NumCPU      : %d\n", runtime.NumCPU())
	fmt.Printf("  GOOS/GOARCH : %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println()
	fmt.Println("Recommended engine settings:")
	fmt.Printf("  --workers            %d   (2× CPU cores for event processing)\n", runtime.NumCPU()*2)
	fmt.Printf("  --parallel-workers   %d   (1× CPU cores for IO concurrency)\n", runtime.NumCPU())
	fmt.Printf("  --parallel-chunk     %d  (2 MiB; tune up for high-bandwidth NVMe)\n", 2<<20)
	fmt.Printf("  --small-threshold    %d (8 MiB; below this → sequential pread64)\n", 8<<20)
	fmt.Printf("  --medium-threshold   %d (128 MiB; below this → mmap)\n", 128<<20)
	fmt.Printf("  --cache-max          4096 (entries per shard, total=%d)\n", 4096*64)
	fmt.Println()
	fmt.Println("Run benchmarks:")
	fmt.Println("  go test ./bench/ -bench=. -benchmem -benchtime=5s -count=3")
	fmt.Println("  go test ./bench/ -bench=BenchmarkParallel -benchmem -benchtime=10s")
	fmt.Println("  go test ./bench/ -bench=BenchmarkAdaptive -benchmem -benchtime=5s")
}

// ─────────────────────────── helpers ─────────────────────────────────────────

type hashResult struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size,omitempty"`
	Algo string `json:"algo"`
}

type multiStringFlag []string

func (f *multiStringFlag) String() string     { return fmt.Sprint([]string(*f)) }
func (f *multiStringFlag) Set(v string) error { *f = append(*f, v); return nil }

func resultPrinter(asJSON bool) engine.ResultCallback {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		return func(key filekey.Key, path string, hash []byte, size int64) {
			_ = enc.Encode(hashResult{
				Path: path,
				Hash: hex.EncodeToString(hash),
				Size: size,
				Algo: "blake3",
			})
		}
	}
	return func(_ filekey.Key, path string, hash []byte, _ int64) {
		fmt.Printf("%s  %s\n", hex.EncodeToString(hash), path)
	}
}

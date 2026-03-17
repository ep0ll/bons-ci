// Command dirsync compares two directory trees and reports:
//   - Paths exclusive to the lower directory (candidates for deletion).
//   - Paths common to both, with fast metadata + incremental SHA-256 check.
//
// Usage:
//
//	dirsync -lower <dir> -upper <dir> [flags]
//
// Examples:
//
//	# Show what is only in lower:
//	dirsync -lower /var/lib/overlay/lower -upper /var/lib/overlay/upper
//
//	# Filter: only *.go files, excluding vendor/:
//	dirsync -lower ./a -upper ./b -wildcard -include '*.go' -exclude vendor
//
//	# Require certain paths to exist:
//	dirsync -lower ./a -upper ./b -require go.mod -require go.sum
//
//	# Show common paths and content diffs:
//	dirsync -lower ./a -upper ./b -common -hash-diff
//
//	# Dry-run then delete exclusive lower entries:
//	dirsync -lower ./a -upper ./b -dry-run
//	dirsync -lower ./a -upper ./b -delete-exclusive
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/bons/bons-ci/internal/dirsync"
)

// multiFlag is a flag.Value that accumulates repeated -flag value calls.
type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil {
		return ""
	}
	return fmt.Sprint([]string(*m))
}
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	log.SetFlags(0)
	log.SetPrefix("dirsync: ")

	// ── Flags ─────────────────────────────────────────────────────────────────
	lower := flag.String("lower", "", "lower directory `path` (required)")
	upper := flag.String("upper", "", "upper directory `path` (required)")

	followSymlinks := flag.Bool("follow-symlinks", false,
		"follow symbolic links when stating entries")
	allowWildcards := flag.Bool("wildcard", false,
		"treat -include / -exclude values as glob patterns (filepath.Match syntax)")
	hashWorkers := flag.Int("hash-workers", 0,
		"goroutines for content hashing (0 = GOMAXPROCS)")
	exclusiveBuf := flag.Int("exclusive-buf", 512,
		"ExclusivePath channel buffer depth")
	commonBuf := flag.Int("common-buf", 512,
		"CommonPath channel buffer depth")

	var includePatterns multiFlag
	var excludePatterns multiFlag
	var requiredPaths multiFlag
	flag.Var(&includePatterns, "include",
		"include only entries matching `pattern` (repeatable; empty = include all)")
	flag.Var(&excludePatterns, "exclude",
		"exclude entries matching `pattern` and prune dirs (repeatable)")
	flag.Var(&requiredPaths, "require",
		"relative `path` that must appear in output; error if absent (repeatable)")

	showExclusive := flag.Bool("exclusive", true,
		"print paths exclusive to lower")
	showCommon := flag.Bool("common", false,
		"print paths common to both directories")
	showHashDiff := flag.Bool("hash-diff", false,
		"print common paths whose content differs")

	deleteExclusive := flag.Bool("delete-exclusive", false,
		"delete exclusive-lower paths (pruned dirs use a single os.RemoveAll)")
	dryRun := flag.Bool("dry-run", false,
		"show what -delete-exclusive would remove, without deleting")

	flag.Parse()

	if *lower == "" || *upper == "" {
		fmt.Fprintf(os.Stderr, "Usage: dirsync -lower <dir> -upper <dir> [flags]\n\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// ── Context: graceful SIGINT / SIGTERM ────────────────────────────────────
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── Printer goroutine ─────────────────────────────────────────────────────
	// Funnel all output through a single goroutine to prevent interleaving from
	// the two concurrent consumer goroutines below.
	printCh := make(chan string, 1024)
	var printerWg sync.WaitGroup
	printerWg.Add(1)
	go func() {
		defer printerWg.Done()
		for line := range printCh {
			fmt.Println(line)
		}
	}()

	// ── Atomic counters ────────────────────────────────────────────────────────
	var (
		exclusiveCount atomic.Int64
		commonCount    atomic.Int64
		hashDiffCount  atomic.Int64
		deletedCount   atomic.Int64
		deleteErrCount atomic.Int64
	)

	// ── Build options ─────────────────────────────────────────────────────────
	opts := dirsync.Options{
		FollowSymlinks:  *followSymlinks,
		AllowWildcards:  *allowWildcards,
		IncludePatterns: []string(includePatterns),
		ExcludePatterns: []string(excludePatterns),
		RequiredPaths:   []string(requiredPaths),
		HashWorkers:     *hashWorkers,
		ExclusiveBuf:    *exclusiveBuf,
		CommonBuf:       *commonBuf,
	}

	// ── Start diff ────────────────────────────────────────────────────────────
	// Diff returns a synchronous error for invalid glob patterns before starting
	// the background goroutine.
	result, err := dirsync.Diff(ctx, *lower, *upper, opts)
	if err != nil {
		log.Fatalf("options error: %v", err)
	}

	// ── Consumer goroutines ───────────────────────────────────────────────────
	// BOTH channels MUST be fully drained — blocking either stalls the
	// background goroutine and causes a resource leak.
	var consumerWg sync.WaitGroup

	// Consumer 1: ExclusivePath channel
	consumerWg.Add(1)
	go func() {
		defer consumerWg.Done()
		for ep := range result.Exclusive {
			exclusiveCount.Add(1)

			tag := "EXCLUSIVE_FILE"
			if ep.Pruned {
				// Pruned dir: one os.RemoveAll removes entire subtree.
				tag = "EXCLUSIVE_DIR "
			}

			if *showExclusive || *dryRun {
				printCh <- fmt.Sprintf("%-15s %s", tag, ep.RelPath)
			}

			if *dryRun {
				printCh <- fmt.Sprintf("  → would: os.RemoveAll(%q)", ep.AbsPath)
				continue
			}

			if *deleteExclusive {
				// Single syscall for pruned root — no child enumeration needed.
				if err := os.RemoveAll(ep.AbsPath); err != nil {
					log.Printf("remove %q: %v", ep.AbsPath, err)
					deleteErrCount.Add(1)
				} else {
					deletedCount.Add(1)
				}
			}
		}
	}()

	// Consumer 2: CommonPath channel
	consumerWg.Add(1)
	go func() {
		defer consumerWg.Done()
		for cp := range result.Common {
			commonCount.Add(1)

			if cp.Err != nil {
				log.Printf("hash error %s: %v", cp.RelPath, cp.Err)
				continue
			}

			isDiff := cp.HashChecked && !cp.HashEqual
			if isDiff {
				hashDiffCount.Add(1)
			}

			status := ""
			switch {
			case cp.MetaEqual:
				status = "META_EQ  "
			case cp.HashChecked && cp.HashEqual:
				status = "HASH_EQ  "
			case cp.HashChecked && !cp.HashEqual:
				status = "HASH_DIFF"
			}

			if *showCommon || (*showHashDiff && isDiff) {
				lh := safePrefix(cp.LowerHash, 12)
				uh := safePrefix(cp.UpperHash, 12)
				printCh <- fmt.Sprintf("COMMON [%s] %-60s  lower=%s  upper=%s",
					status, cp.RelPath, lh, uh)
			}
		}
	}()

	// Wait for both consumers to finish draining their channels.
	consumerWg.Wait()

	// Shut down printer and flush remaining lines.
	close(printCh)
	printerWg.Wait()

	// Read Err ONLY after both consumers have fully drained (documented contract).
	if err := <-result.Err; err != nil {
		// MissingRequiredPathsError gets a specific exit message.
		var mErr *dirsync.MissingRequiredPathsError
		if errors.As(err, &mErr) {
			for _, p := range mErr.Paths {
				log.Printf("required path not found: %q", p)
			}
			os.Exit(2)
		}
		log.Fatalf("walk error: %v", err)
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("────────────────────────────────────────────")
	fmt.Printf("  Exclusive lower paths  : %d\n", exclusiveCount.Load())
	fmt.Printf("  Common paths           : %d\n", commonCount.Load())
	fmt.Printf("  Content differences    : %d\n", hashDiffCount.Load())
	if *deleteExclusive || *dryRun {
		if *dryRun {
			fmt.Printf("  Would delete           : %d  (dry-run)\n", exclusiveCount.Load())
		} else {
			fmt.Printf("  Deleted                : %d\n", deletedCount.Load())
			if n := deleteErrCount.Load(); n > 0 {
				fmt.Printf("  Delete errors          : %d\n", n)
				os.Exit(1)
			}
		}
	}
}

// safePrefix returns up to n bytes of s with an ellipsis if truncated.
// Returns "-" for empty strings (e.g. meta-equal entries have no hash).
func safePrefix(s string, n int) string {
	if s == "" {
		return "-"
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

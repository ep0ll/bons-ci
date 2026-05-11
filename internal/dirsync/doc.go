// Package dirsync compares two directory trees (lower and upper) and produces
// typed, concurrent streams of classified filesystem entries that can be acted
// on with minimal syscalls.
//
// # Motivation
//
// Container runtimes (BuildKit, containerd) maintain an overlay filesystem:
//
//   - lower  — base image layer (read-only)
//   - upper  — in-progress build layer (read-write diff)
//   - merged — union view presented to build processes
//
// After a build step completes, any file that lives only in lower is redundant
// in merged: the overlay kernel driver already serves it from lower. This
// package identifies those redundant entries and removes them from merged,
// shrinking the merged layer and reducing overlay lookup depth.
//
// # Core concepts
//
//	ExclusivePath — entry found only in lower; remove from merged.
//	CommonPath    — entry in both lower and upper; compare content to decide.
//
// # Three-stage pipeline
//
//	Classifier ──exclusive──▶ ExclusiveHandler ──▶ Batcher ──▶ MergedView
//	           ──common───▶ HashPipeline ──▶ CommonHandler ──▶ Batcher
//	           ──errs────▶ error collector
//
// # Performance design
//
// Content comparison uses a four-tier decision tree ordered by cost:
//
//  1. Inode identity (hard-link shortcut) — zero I/O, O(1)
//  2. Size mismatch — zero I/O, O(1)
//  3. Mtime equality — zero I/O, O(1), BuildKit DiffOp semantics
//  4. Content comparison — I/O required:
//     4S: sequential 64 KiB chunks with early exit (files < 2 MiB)
//     4P: parallel pread64 segments with early cancellation (files ≥ 2 MiB)
//
// Batch filesystem mutations on Linux 5.11+ are submitted via a single
// io_uring_enter(2) call, reducing kernel crossings from O(n) to O(1) per
// flush. On other platforms a bounded goroutine pool is used instead.
//
// # External libraries used
//
//   - github.com/moby/patternmatcher — Docker/BuildKit .dockerignore pattern engine
//   - github.com/cyphar/filepath-securejoin — path-traversal-safe SecureJoin
//   - github.com/zeebo/xxh3 — 128-bit xxHash3 for content fingerprinting
//
// # Quickstart
//
//	eng, err := dirsync.NewDeleteEngine(lowerDir, upperDir, mergedDir, nil, nil)
//	if err != nil { return err }
//	result := eng.Run(ctx)
//	if !result.OK() { return result.Err }
//
// # Extension points
//
// Every component is an interface. Swap implementations at the composition root:
//
//   - [Classifier]        — how trees are walked and entries classified
//   - [ExclusiveHandler]  — what to do with lower-only entries
//   - [CommonHandler]     — what to do with shared entries
//   - [ContentHasher]     — how two files are compared for equality
//   - [Batcher]           — how filesystem mutations are batched and applied
//   - [MergedView]        — target filesystem abstraction
//   - [Filter]            — which paths participate in the diff
//
// # Concurrency safety
//
// All exported types are safe for concurrent use unless documented otherwise.
package dirsync

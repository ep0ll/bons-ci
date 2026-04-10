// Package fshash computes reproducible, hermetic checksums of files and
// directory trees.
//
// # Design Goals
//
//   - Reproducible: identical content always produces identical digests,
//     regardless of traversal order or host OS. Entries are sorted by name at
//     every directory level before hashing.
//   - Hermetic: the digest depends only on content and (optionally) metadata;
//     never on timestamps or inode numbers unless explicitly requested.
//   - Efficient: a single os.ReadDir call per directory; file data is read
//     once with a pooled buffer; optional parallel workers bound I/O
//     concurrency to a configurable limit, minimising context switches.
//   - Extensible: every major concern (hashing algorithm, filesystem walking,
//     entry filtering, metadata inclusion) is expressed as a narrow interface
//     that callers can replace or compose.
//
// # Checksum model
//
// A file's digest is computed over a canonical header derived from selected
// metadata fields (mode bits, size, optional mtime) followed by the raw file
// content.  A directory's digest is computed over the sorted sequence of
//
//	<name>\0<child-digest>
//
// pairs for its direct children (files and subdirectories alike), so the
// digest is sensitive to both the names and the content of every descendant.
//
// # Quick start
//
//	cs := fshash.New()                          // SHA-256, no filter, 1 worker
//	res, err := cs.Sum(ctx, "/path/to/dir")
//	fmt.Println(res.Hex())
//
// Use [Options] and the With* helpers to tune the behaviour:
//
//	cs := fshash.New(
//	    fshash.WithAlgorithm(fshash.SHA512),
//	    fshash.WithWorkers(runtime.NumCPU()),
//	    fshash.WithFilter(myFilter),
//	    fshash.WithMetadata(fshash.MetaModeAndSize),
//	)
package fshash

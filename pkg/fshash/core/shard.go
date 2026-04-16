package core

import (
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"runtime"
	"sync"
)

// ── Shard constants ───────────────────────────────────────────────────────────
//
// SKILL §1+§3: mode selection is a function of file SIZE alone, guaranteeing
// digest idempotency across all worker configurations.
//
//   fileSize >= ShardThreshold  → ALWAYS shard mode
//   fileSize <  ShardThreshold  → sequential mode
//
// Workers control parallelism WITHIN shard mode, not WHICH mode is chosen.

const (
	// ShardThreshold is the file size (bytes) above which parallel sharding
	// is used. Compile-time constant; never changes at runtime.
	ShardThreshold = 4 * 1024 * 1024 // 4 MiB

	// ShardSize is the size of each parallel shard.
	ShardSize = 1 * 1024 * 1024 // 1 MiB
)

// ── ShardCombiner ─────────────────────────────────────────────────────────────
//
// ShardCombiner takes N independently-hashed shard digests (in index order)
// and combines them into a single deterministic digest using:
//
//   H(masterHash | 0xFE | nShards_BE32 | shardSize_BE32 | d_0 | d_1 … d_N)
//
// The 0xFE sentinel + chunk-count/size encoding ensures the combined digest
// is distinguishable from any sequential hash of the same bytes.
// BUG FIX NOTE: use binary.BigEndian.PutUint32 — byte(val>>8) overflows when
// val > 255 (ShardSize = 1<<20 = 1048576 would truncate).

// CombineShards writes the shard sentinel + all shard digests into masterHash
// and returns the final digest.
func CombineShards(masterHash hash.Hash, shardDigests [][]byte) []byte {
	var sentinel [9]byte
	sentinel[0] = 0xFE
	binary.BigEndian.PutUint32(sentinel[1:5], uint32(len(shardDigests)))
	binary.BigEndian.PutUint32(sentinel[5:9], uint32(ShardSize))
	MustWrite(masterHash, sentinel[:])
	for _, sd := range shardDigests {
		MustWrite(masterHash, sd)
	}
	var sink DigestSink
	return CloneDigest(sink.Sum(masterHash))
}

// ── ShardedReader ─────────────────────────────────────────────────────────────
//
// ShardedReader performs parallel pread(2)-based hashing of large files.
// It is safe to use only on *os.File on platforms where ReadAt is pread(2).
// All workers receive fresh hash.Hash instances from newHash.

// ReaderAt is the minimal interface for parallel shard reads (os.File satisfies it).
type ReaderAt interface {
	ReadAt(p []byte, off int64) (n int, err error)
}

// HashSharded hashes ra (a file of fileSize bytes) using up to maxWorkers
// parallel goroutines. It returns shard digests in index order.
//
//   1. Divide into ceil(fileSize/ShardSize) chunks.
//   2. ReadAt each chunk in parallel (pread-safe, no seek-position races).
//   3. Hash each chunk independently with newHash().
func HashSharded(
	ra ReaderAt,
	fileSize int64,
	newHash func() hash.Hash,
	pool BufPool,
	maxWorkers int,
) ([][]byte, error) {
	nShards := int((fileSize + ShardSize - 1) / ShardSize)
	shardDigests := make([][]byte, nShards)
	shardErrs := make([]error, nShards)

	concurrency := maxWorkers
	if concurrency > nShards {
		concurrency = nShards
	}
	if maxCPU := runtime.NumCPU(); concurrency > maxCPU {
		concurrency = maxCPU
	}
	if concurrency < 1 {
		concurrency = 1
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i := range nShards {
		i := i
		offset := int64(i) * ShardSize
		chunkLen := ShardSize
		if remaining := fileSize - offset; remaining < int64(chunkLen) {
			chunkLen = int(remaining)
		}

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()

			b := pool.Get(int64(chunkLen))
			buf := (*b)[:chunkLen]

			if _, readErr := ra.ReadAt(buf, offset); readErr != nil && readErr != io.EOF {
				shardErrs[i] = fmt.Errorf("fshash/core: shard %d read: %w", i, readErr)
				pool.Put(b)
				return
			}

			h := newHash()
			MustWrite(h, buf)
			pool.Put(b)

			var sink DigestSink
			shardDigests[i] = CloneDigest(sink.Sum(h))
		}()
	}
	wg.Wait()

	for _, e := range shardErrs {
		if e != nil {
			return nil, e
		}
	}
	return shardDigests, nil
}

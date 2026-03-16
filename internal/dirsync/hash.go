package dirsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

// ─── Buffer pool ──────────────────────────────────────────────────────────────

// bufPool recycles 64 KiB read buffers across hash workers to reduce GC churn.
// 64 KiB is large enough to saturate typical SSD throughput in a single read(2).
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64<<10) // 64 KiB
		return &b
	},
}

func getBuf() *[]byte  { return bufPool.Get().(*[]byte) }
func putBuf(b *[]byte) { bufPool.Put(b) }

// ─── Hash job ─────────────────────────────────────────────────────────────────

// hashJob carries a pair of file paths whose content must be hashed and compared.
type hashJob struct {
	relPath   string
	lowerAbs  string
	upperAbs  string
	lowerInfo fs.FileInfo
	upperInfo fs.FileInfo
}

// ─── Worker pool ──────────────────────────────────────────────────────────────

// hashPool manages a fixed set of goroutines that compute SHA-256 digests for
// file pairs whose metadata check failed.  Completed CommonPath values are
// written directly to the shared comCh channel.
//
// Lifecycle:
//  1. newHashPool  – start N workers.
//  2. submit(job)  – enqueue a job (blocks on ctx cancel if workers are busy).
//  3. drain()      – close jobs channel; wait for all workers to finish.
//     Must be called exactly once after all submit calls.
type hashPool struct {
	ctx  context.Context
	jobs chan hashJob
	out  chan<- CommonPath
	wg   sync.WaitGroup
}

func newHashPool(ctx context.Context, workers int, out chan<- CommonPath) *hashPool {
	p := &hashPool{
		ctx:  ctx,
		jobs: make(chan hashJob, workers*8), // backlog = 8× workers
		out:  out,
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.run()
	}
	return p
}

// submit enqueues a hash job.  Blocks if the job buffer is full; returns
// immediately if ctx is cancelled.
func (p *hashPool) submit(job hashJob) {
	select {
	case p.jobs <- job:
	case <-p.ctx.Done():
	}
}

// drain signals workers to stop, waits until all pending jobs are processed,
// and returns only after all results have been written to out.
func (p *hashPool) drain() {
	close(p.jobs) // signal: no more jobs
	p.wg.Wait()   // wait for all workers to exit their range loop
}

// run is the per-worker goroutine body.
func (p *hashPool) run() {
	defer p.wg.Done()

	for job := range p.jobs {
		if p.ctx.Err() != nil {
			// Context cancelled: drain remaining jobs without processing.
			continue
		}

		cp := processHashJob(job)

		select {
		case p.out <- cp:
		case <-p.ctx.Done():
			// Drop result; consumer is gone.
		}
	}
}

// ─── Hash job processing ──────────────────────────────────────────────────────

// processHashJob computes SHA-256 digests for both files and returns the
// enriched CommonPath.  Errors are embedded in cp.Err rather than returned
// so a single bad file does not abort the entire walk.
func processHashJob(job hashJob) CommonPath {
	cp := CommonPath{
		RelPath:     job.relPath,
		LowerAbs:    job.lowerAbs,
		UpperAbs:    job.upperAbs,
		LowerInfo:   job.lowerInfo,
		UpperInfo:   job.upperInfo,
		HashChecked: true,
	}

	lHash, err := hashFile(job.lowerAbs)
	if err != nil {
		cp.Err = fmt.Errorf("hash lower %q: %w", job.lowerAbs, err)
		return cp
	}

	uHash, err := hashFile(job.upperAbs)
	if err != nil {
		cp.Err = fmt.Errorf("hash upper %q: %w", job.upperAbs, err)
		return cp
	}

	cp.LowerHash = lHash
	cp.UpperHash = uHash
	cp.HashEqual = lHash == uHash
	return cp
}

// hashFile computes the SHA-256 digest of a regular file using incremental
// buffered reads.
//
// "Incremental" here means:
//   - File content is never fully loaded into memory.
//   - A pooled 64 KiB buffer is used for each read(2) call.
//   - The SHA-256 state is updated chunk-by-chunk (io.CopyBuffer).
//   - Memory usage is O(1) regardless of file size.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := getBuf()
	defer putBuf(buf)

	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, *buf); err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

//go:build linux

package differ

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// ─────────────────────────────────────────────────────────────────────────────
// io_uring constants (linux/io_uring.h)
// ─────────────────────────────────────────────────────────────────────────────

const (
	// Syscall numbers — identical across x86_64, aarch64, and riscv64.
	sysIOURingSetup uintptr = 425
	sysIOURingEnter uintptr = 426

	// io_uring_enter flags.
	ioRingEnterGetEvents uint32 = 0x1

	// Operation codes.
	ioRingOpUnlinkat uint8 = 36 // Linux 5.11+

	// AT_FDCWD — use process working directory as dirfd.
	atFDCWD int32 = -100
	// AT_REMOVEDIR — set in sqe->unlink_flags to remove a directory (NOT in Len).
	atRemoveDir uint32 = 0x200

	// mmap protection / flags for ring regions.
	mmapProt  = syscall.PROT_READ | syscall.PROT_WRITE
	mmapFlags = syscall.MAP_SHARED | syscall.MAP_POPULATE

	// Feature flags (linux/io_uring.h).
	//
	// BUG FIX C1: ioRingFeatNoDrop is 0x2, not 0x1.
	// 0x1 = IORING_FEAT_SINGLE_MMAP (Linux 5.4)  — rings share one mmap region.
	// 0x2 = IORING_FEAT_NODROP      (Linux 5.5)  — CQEs are never silently dropped.
	ioRingFeatNoDrop uint32 = 0x2

	// ioRingFeatSQPollNonFixed (0x80) was added in Linux 5.11.
	// Its presence serves as a proxy for IORING_OP_UNLINKAT availability
	// (also Linux 5.11), eliminating the need for a IORING_REGISTER_PROBE call.
	ioRingFeatSQPollNonFixed uint32 = 0x80

	// requiredFeatures: NoDrop (5.5) AND SQPollNonFixed (5.11) must both be set.
	requiredFeatures = ioRingFeatNoDrop | ioRingFeatSQPollNonFixed

	// io_uring mmap offsets (linux/io_uring.h IORING_OFF_*).
	ioRingOffSQRing int64 = 0
	ioRingOffCQRing int64 = 0x8000000
	ioRingOffSQEs   int64 = 0x10000000
)

// ─────────────────────────────────────────────────────────────────────────────
// io_uring kernel structures — layout must match the kernel ABI exactly.
// ─────────────────────────────────────────────────────────────────────────────

// ioUringParams is passed to io_uring_setup(2). Total size: 120 bytes.
type ioUringParams struct {
	SqEntries    uint32
	CqEntries    uint32
	Flags        uint32
	SqThreadCPU  uint32
	SqThreadIdle uint32
	Features     uint32
	WqFd         uint32
	Resv         [3]uint32
	SqOff        ioSqringOffsets // 40 bytes
	CqOff        ioCqringOffsets // 40 bytes
}

// ioSqringOffsets describes the byte offsets of SQ ring fields within the mmap.
type ioSqringOffsets struct {
	Head        uint32
	Tail        uint32
	RingMask    uint32
	RingEntries uint32
	Flags       uint32
	Dropped     uint32
	Array       uint32
	Resv1       uint32
	UserAddr    uint64
}

// ioCqringOffsets describes the byte offsets of CQ ring fields within the mmap.
type ioCqringOffsets struct {
	Head        uint32
	Tail        uint32
	RingMask    uint32
	RingEntries uint32
	Overflow    uint32
	Cqes        uint32
	Flags       uint32
	Resv1       uint32
	UserAddr    uint64
}

// ioUringSQE is a Submission Queue Entry (64 bytes, matches kernel layout).
//
// Field mapping for IORING_OP_UNLINKAT (from io_uring/fs.c::io_unlinkat_prep):
//
//	Fd          → sqe->fd            (dirfd; AT_FDCWD = -100)
//	Addr        → sqe->addr          (pointer to NUL-terminated pathname)
//	Len         → sqe->len           (MUST be 0; kernel returns EINVAL otherwise)
//	Off         → sqe->off           (MUST be 0; kernel returns EINVAL otherwise)
//	OpcodeFlags → sqe->unlink_flags  (set AT_REMOVEDIR here for directory removal)
//
// BUG FIX C2: AT_REMOVEDIR belongs in OpcodeFlags (sqe->unlink_flags), NOT Len.
// The kernel explicitly validates: if (sqe->off || sqe->len || sqe->buf_index)
// return -EINVAL. The original code put atRemoveDir in Len, causing every
// directory deletion to fail with EINVAL.
type ioUringSQE struct {
	Opcode      uint8
	Flags       uint8
	Ioprio      uint16
	Fd          int32    // dirfd (AT_FDCWD)
	Off         uint64   // must be 0 for unlinkat
	Addr        uint64   // pointer to NUL-terminated path string
	Len         uint32   // must be 0 for unlinkat
	OpcodeFlags uint32   // union: unlink_flags — put AT_REMOVEDIR here
	UserData    uint64   // echoed back in CQE for op attribution
	_           [24]byte // padding to 64 bytes
}

// ioUringCQE is a Completion Queue Entry (16 bytes).
type ioUringCQE struct {
	UserData uint64
	Res      int32  // syscall return value; negative errno on failure
	Flags    uint32
}

// ─────────────────────────────────────────────────────────────────────────────
// IOURingBatcher
// ─────────────────────────────────────────────────────────────────────────────

// IOURingBatcher implements [Batcher] using Linux io_uring to submit batched
// unlinkat(2) calls with a single io_uring_enter(2) syscall per Flush.
//
// # Advantage over GoroutineBatcher
//
// For N deletion operations, [GoroutineBatcher] issues N separate syscalls
// (each crossing the user→kernel boundary). IOURingBatcher prepares all N
// Submission Queue Entries in shared memory and issues a single
// io_uring_enter(2), cutting kernel crossings from N to 1 per batch.
//
// # Requirements
//
// Linux 5.11+ for IORING_OP_UNLINKAT. Falls back to [GoroutineBatcher] on
// older kernels or when io_uring_setup fails.
//
// # Path lifetime
//
// IORING_OP_UNLINKAT requires the path-string pointer to remain valid until
// the corresponding CQE is harvested. IOURingBatcher pins all path byte
// slices in batchedUnlink.pathBuf until Flush completes.
type IOURingBatcher struct {
	view    MergedView
	entries uint32 // ring capacity; must be a power of two

	mu      sync.Mutex
	pending []batchedUnlink // ops waiting for the next Flush
	closed  bool
}

// batchedUnlink holds the data for a single pending unlinkat SQE.
type batchedUnlink struct {
	op      BatchOp
	pathBuf []byte // NUL-terminated absolute path; pinned until CQE arrives
	isDir   bool
}

// IOURingBatcherOption is a functional option for [IOURingBatcher].
type IOURingBatcherOption func(*IOURingBatcher)

// WithRingEntries sets the io_uring submission-queue capacity (must be a power
// of two; clamped to [64, 32768]). Larger values reduce stalls when batches
// exceed the ring size. Default: 256.
func WithRingEntries(n uint32) IOURingBatcherOption {
	return func(b *IOURingBatcher) {
		if n >= 64 && n <= 32768 {
			b.entries = n
		}
	}
}

// NewIOURingBatcher constructs an [IOURingBatcher] targeting view.
//
// If io_uring is unavailable (kernel < 5.11, seccomp restriction, etc.) the
// function transparently returns a [GoroutineBatcher] so callers never need
// to branch. The returned Batcher always satisfies the interface contract.
//
// BUG FIX H4: The previous implementation unconditionally allocated a fallback
// GoroutineBatcher even on kernels where io_uring is fully available. That
// field was never called; it only wasted memory.
func NewIOURingBatcher(view MergedView, opts ...IOURingBatcherOption) (Batcher, error) {
	if !ioURingAvailable() {
		return NewGoroutineBatcher(view, WithBatchParallelism(runtime.NumCPU())), nil
	}
	b := &IOURingBatcher{
		view:    view,
		entries: 256,
	}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// ioURingAvailable probes whether the running kernel supports io_uring with
// IORING_OP_UNLINKAT (Linux 5.11+).
//
// BUG FIX H2: The previous implementation only checked that io_uring_setup
// succeeded (available since Linux 5.1). IORING_OP_UNLINKAT requires 5.11+.
// We now verify params.Features contains IORING_FEAT_SQPOLL_NONFIXED (0x80),
// a reliable 5.11 indicator, without requiring a IORING_REGISTER_PROBE call.
func ioURingAvailable() bool {
	var params ioUringParams
	fd, _, errno := syscall.Syscall(sysIOURingSetup, 1,
		uintptr(unsafe.Pointer(&params)), 0)
	if errno != 0 || fd == 0 {
		return false
	}
	// BUG FIX H3: exactly one close, via a direct call here. The old code had
	// both an explicit close in the NODROP fallback branch AND a deferred close,
	// causing a double-close of the ring file descriptor.
	_ = syscall.Close(int(fd))
	return params.Features&requiredFeatures == requiredFeatures
}

// Submit implements [Batcher].
func (b *IOURingBatcher) Submit(_ context.Context, op BatchOp) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errBatcherClosed
	}
	// Derive directory-ness from OpKind — keeps Submit O(1), no stat calls.
	isDir := op.Kind == OpRemoveAll

	// Build a NUL-terminated C string kept alive until its CQE is harvested.
	absPath := b.view.AbsPath(op.RelPath)
	buf := append([]byte(absPath), 0)

	b.pending = append(b.pending, batchedUnlink{op: op, pathBuf: buf, isDir: isDir})
	return nil
}

// Flush implements [Batcher].
//
// Prepares all pending unlinks as SQEs, submits them with a single
// io_uring_enter(2), then harvests all CQEs for error reporting.
//
// For OpRemoveAll, IORING_OP_UNLINKAT behaves like rmdir(2): it only removes
// empty directories. When ENOTEMPTY is returned, Flush falls back to
// view.RemoveAll which handles non-empty trees recursively.
func (b *IOURingBatcher) Flush(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errBatcherClosed
	}
	work := b.pending
	b.pending = nil // nil (not [:0]) to release backing array
	b.mu.Unlock()

	if len(work) == 0 {
		return nil
	}
	return b.flushWithRing(ctx, work)
}

// Close implements [Batcher]. Idempotent: a second call returns nil.
func (b *IOURingBatcher) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	err := b.Flush(ctx)

	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// flushWithRing — core io_uring submit+harvest cycle
// ─────────────────────────────────────────────────────────────────────────────

// flushWithRing creates a fresh io_uring ring, writes all work items as SQEs,
// submits them in ring-sized chunks via io_uring_enter(2), and harvests CQEs.
//
// Memory-ordering (BUG FIX H1):
//
//   - SQ tail must be written with a store-release so the kernel never observes
//     the tail advance before the SQE slots are fully populated.
//   - SQ/CQ mask and CQ head must be read with load-acquire.
//   - sync/atomic.StoreUint32/LoadUint32 provide these semantics on every
//     Go-supported architecture. The original code used a plain *p = v
//     assignment with no ordering guarantees whatsoever.
func (b *IOURingBatcher) flushWithRing(ctx context.Context, work []batchedUnlink) error {
	// Size the ring to min(len(work), b.entries); must be a power of two.
	ringSize := nextPow2(uint32(len(work)))
	if ringSize > b.entries {
		ringSize = b.entries
	}
	if ringSize < 1 {
		ringSize = 1
	}

	var params ioUringParams
	ringFD, _, errno := syscall.Syscall(sysIOURingSetup, uintptr(ringSize),
		uintptr(unsafe.Pointer(&params)), 0)
	if errno != 0 {
		return b.execFallback(ctx, work)
	}
	// BUG FIX H3: single deferred close — no other close in this function.
	defer syscall.Close(int(ringFD)) //nolint:errcheck

	if params.Features&requiredFeatures != requiredFeatures {
		return b.execFallback(ctx, work)
	}

	// Map SQ ring (head, tail, mask, and index array).
	sqSize := params.SqOff.Array + params.SqEntries*4
	sqRing, err := syscall.Mmap(int(ringFD), ioRingOffSQRing, int(sqSize), mmapProt, mmapFlags)
	if err != nil {
		return b.execFallback(ctx, work)
	}
	defer syscall.Munmap(sqRing) //nolint:errcheck

	// Map SQE array (the actual submission entries).
	sqeSize := uintptr(params.SqEntries) * unsafe.Sizeof(ioUringSQE{})
	sqeRaw, err := syscall.Mmap(int(ringFD), ioRingOffSQEs, int(sqeSize), mmapProt, mmapFlags)
	if err != nil {
		return b.execFallback(ctx, work)
	}
	defer syscall.Munmap(sqeRaw) //nolint:errcheck

	// Map CQ ring (head, tail, mask, and CQE array).
	cqSize := params.CqOff.Cqes + params.CqEntries*uint32(unsafe.Sizeof(ioUringCQE{}))
	cqRing, err := syscall.Mmap(int(ringFD), ioRingOffCQRing, int(cqSize), mmapProt, mmapFlags)
	if err != nil {
		return b.execFallback(ctx, work)
	}
	defer syscall.Munmap(cqRing) //nolint:errcheck

	// Cache stable ring pointers. Mask values never change after setup.
	// BUG FIX H1: atomic loads for all reads of kernel-shared memory.
	sqTailPtr  := (*uint32)(unsafe.Pointer(&sqRing[params.SqOff.Tail]))
	sqMask     := atomic.LoadUint32((*uint32)(unsafe.Pointer(&sqRing[params.SqOff.RingMask])))
	sqArrayPtr := (*uint32)(unsafe.Pointer(&sqRing[params.SqOff.Array]))
	cqHeadPtr  := (*uint32)(unsafe.Pointer(&cqRing[params.CqOff.Head]))
	cqMask     := atomic.LoadUint32((*uint32)(unsafe.Pointer(&cqRing[params.CqOff.RingMask])))

	var errs []error
	submitted := 0

	for i := 0; i < len(work); {
		chunk := work[i:]
		if uint32(len(chunk)) > ringSize {
			chunk = chunk[:ringSize]
		}

		// ── Fill SQEs ────────────────────────────────────────────────────────
		for j, w := range chunk {
			idx := (uint32(submitted) + uint32(j)) & sqMask

			sqePtr := (*ioUringSQE)(unsafe.Pointer(
				uintptr(unsafe.Pointer(&sqeRaw[0])) +
					uintptr(idx)*unsafe.Sizeof(ioUringSQE{}),
			))

			// BUG FIX C2: AT_REMOVEDIR in OpcodeFlags, never in Len.
			*sqePtr = ioUringSQE{
				Opcode:   ioRingOpUnlinkat,
				Fd:       atFDCWD,
				Addr:     uint64(uintptr(unsafe.Pointer(&w.pathBuf[0]))),
				UserData: uint64(i + j), // work-slice index for CQE attribution
				// Len and Off remain 0; kernel rejects non-zero values.
			}
			if w.isDir {
				sqePtr.OpcodeFlags = atRemoveDir // sqe->unlink_flags
			}

			// Publish this SQE's slot index into the SQ index array.
			(*[1 << 28]uint32)(unsafe.Pointer(sqArrayPtr))[idx] = idx
		}

		// BUG FIX H1: store-release — kernel must not see the tail advance
		// before the SQE slots above are fully visible in shared memory.
		curTail := atomic.LoadUint32(sqTailPtr)
		atomic.StoreUint32(sqTailPtr, curTail+uint32(len(chunk)))

		// ── Submit + wait ─────────────────────────────────────────────────────
		ret, _, enterErrno := syscall.Syscall6(sysIOURingEnter,
			ringFD,
			uintptr(len(chunk)),           // to_submit
			uintptr(len(chunk)),           // min_complete (wait for all)
			uintptr(ioRingEnterGetEvents), // flags
			0, 0)
		if enterErrno != 0 || int(ret) != len(chunk) {
			if ferr := executeBatch(ctx, b.view, opsFromWork(chunk), runtime.NumCPU()); ferr != nil {
				errs = append(errs, ferr)
			}
			i += len(chunk)
			submitted += len(chunk)
			continue
		}

		// ── Harvest CQEs ──────────────────────────────────────────────────────
		// BUG FIX H1: load-acquire on CQ head.
		cqHead := atomic.LoadUint32(cqHeadPtr)

		for j := 0; j < len(chunk); j++ {
			cqePtr := (*ioUringCQE)(unsafe.Pointer(
				uintptr(unsafe.Pointer(&cqRing[params.CqOff.Cqes])) +
					uintptr((cqHead+uint32(j))&cqMask)*unsafe.Sizeof(ioUringCQE{}),
			))

			if cqePtr.Res >= 0 {
				continue
			}

			eno := syscall.Errno(-cqePtr.Res)
			wi  := work[int(cqePtr.UserData)]

			switch eno {
			case syscall.ENOENT:
				// Already absent — idempotent, not an error.

			case syscall.ENOTEMPTY:
				// Non-empty directory: io_uring unlinkat = rmdir(2), which
				// rejects non-empty dirs. Fall back to recursive RemoveAll.
				if ferr := b.view.RemoveAll(ctx, wi.op.RelPath); ferr != nil {
					errs = append(errs, fmt.Errorf("removeAll fallback %q: %w", wi.op.RelPath, ferr))
				}

			case syscall.EINVAL:
				// Opcode restricted by seccomp, or malformed SQE. Fall back
				// synchronously so the deletion still occurs.
				if ferr := executeOp(ctx, b.view, wi.op); ferr != nil {
					errs = append(errs, fmt.Errorf("sync fallback %q: %w", wi.op.RelPath, ferr))
				}

			default:
				errs = append(errs, fmt.Errorf("io_uring unlinkat %q: %w", wi.op.RelPath, eno))
			}
		}

		// BUG FIX H1: store-release on CQ head to free consumed CQE slots.
		atomic.StoreUint32(cqHeadPtr, cqHead+uint32(len(chunk)))

		i += len(chunk)
		submitted += len(chunk)
	}

	// Prevent GC from freeing path buffers before all CQEs have been read.
	runtime.KeepAlive(work)

	return joinErrors(errs)
}

// execFallback routes all work items through the synchronous goroutine-pool path.
func (b *IOURingBatcher) execFallback(ctx context.Context, work []batchedUnlink) error {
	return executeBatch(ctx, b.view, opsFromWork(work), runtime.NumCPU())
}

// opsFromWork extracts the BatchOp from each batchedUnlink entry.
func opsFromWork(work []batchedUnlink) []BatchOp {
	ops := make([]BatchOp, len(work))
	for i, w := range work {
		ops[i] = w.op
	}
	return ops
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// nextPow2 returns the smallest power of two ≥ n.
func nextPow2(n uint32) uint32 {
	if n == 0 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	return n + 1
}

// IOURingAvailable reports whether the running kernel supports io_uring with
// IORING_OP_UNLINKAT (Linux 5.11+).
func IOURingAvailable() bool { return ioURingAvailable() }

// NewBestBatcher returns the highest-performance [Batcher] available on the
// current platform. On Linux 5.11+ this is an [IOURingBatcher]; on older
// kernels or if setup fails it is a [GoroutineBatcher].
func NewBestBatcher(view MergedView) (Batcher, error) {
	return NewIOURingBatcher(view)
}

// _ keeps the "os" import alive on this build-constrained file.
var _ = os.ErrNotExist

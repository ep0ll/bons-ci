//go:build linux

package dirsync

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
// io_uring kernel constants (linux/io_uring.h)
// ─────────────────────────────────────────────────────────────────────────────

// Syscall numbers for io_uring_setup(2) and io_uring_enter(2).
// These are stable across x86_64, aarch64, and riscv64.
const (
	sysIOURingSetup uintptr = 425
	sysIOURingEnter uintptr = 426
)

// io_uring_enter(2) flags.
const ioRingEnterGetEvents uint32 = 0x1

// IORING_OP_UNLINKAT is the io_uring operation that wraps unlinkat(2).
// Available since Linux 5.11.
const ioRingOpUnlinkat uint8 = 36

// AT_FDCWD tells the kernel to interpret the path relative to the calling
// process's working directory, equivalent to the standard unlinkat(AT_FDCWD, …).
const atFDCWD int32 = -100

// AT_REMOVEDIR is set in sqe->unlink_flags (OpcodeFlags) to remove a
// directory. It must NOT be placed in sqe->len — the kernel returns EINVAL
// for any non-zero value in sqe->len or sqe->off for IORING_OP_UNLINKAT.
const atRemoveDir uint32 = 0x200

// mmap protection and flags for the shared ring regions.
const (
	mmapProt  = syscall.PROT_READ | syscall.PROT_WRITE
	mmapFlags = syscall.MAP_SHARED | syscall.MAP_POPULATE
)

// IORING_FEAT_* bit flags returned in ioUringParams.Features.
//
//	0x01 = IORING_FEAT_SINGLE_MMAP   (Linux 5.4)  — rings share one mmap region
//	0x02 = IORING_FEAT_NODROP        (Linux 5.5)  — CQEs are never silently dropped
//	0x80 = IORING_FEAT_SQPOLL_NONFIXED (Linux 5.11) — used as 5.11 availability proxy
const (
	ioRingFeatNoDrop            uint32 = 0x2
	ioRingFeatSQPollNonFixed    uint32 = 0x80
	requiredFeatureFlags               = ioRingFeatNoDrop | ioRingFeatSQPollNonFixed
)

// mmap offsets for the three ring regions (linux/io_uring.h IORING_OFF_*).
const (
	ioRingOffSQRing int64 = 0
	ioRingOffCQRing int64 = 0x8000000
	ioRingOffSQEs   int64 = 0x10000000
)

// ─────────────────────────────────────────────────────────────────────────────
// Kernel ABI structures — layout must match exactly
// ─────────────────────────────────────────────────────────────────────────────

// ioUringParams is passed to io_uring_setup(2). Total size: 120 bytes.
// The kernel fills Features and the Sq/CqOff fields on return.
type ioUringParams struct {
	SqEntries    uint32
	CqEntries    uint32
	Flags        uint32
	SqThreadCPU  uint32
	SqThreadIdle uint32
	Features     uint32
	WqFd         uint32
	Resv         [3]uint32
	SqOff        ioSqRingOffsets // 40 bytes
	CqOff        ioCqRingOffsets // 40 bytes
}

// ioSqRingOffsets describes byte offsets of SQ ring fields within the SQ mmap.
type ioSqRingOffsets struct {
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

// ioCqRingOffsets describes byte offsets of CQ ring fields within the CQ mmap.
type ioCqRingOffsets struct {
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
// Field mapping for IORING_OP_UNLINKAT (io_uring/fs.c::io_unlinkat_prep):
//
//	Fd          → sqe->fd            (dirfd; AT_FDCWD = -100)
//	Addr        → sqe->addr          (pointer to NUL-terminated pathname)
//	Len         → sqe->len           (MUST be 0; kernel returns EINVAL otherwise)
//	Off         → sqe->off           (MUST be 0; kernel returns EINVAL otherwise)
//	OpcodeFlags → sqe->unlink_flags  (set AT_REMOVEDIR here for directory removal)
//
// AT_REMOVEDIR belongs in OpcodeFlags (sqe->unlink_flags), NOT in Len.
// The kernel explicitly validates sqe->off == 0 && sqe->len == 0 and returns
// -EINVAL when either is non-zero.
type ioUringSQE struct {
	Opcode      uint8
	Flags       uint8
	Ioprio      uint16
	Fd          int32  // dirfd (AT_FDCWD)
	Off         uint64 // must be 0 for unlinkat
	Addr        uint64 // pointer to NUL-terminated path string
	Len         uint32 // must be 0 for unlinkat
	OpcodeFlags uint32 // union: unlink_flags — put AT_REMOVEDIR here
	UserData    uint64 // echoed back in CQE for op attribution (index into work slice)
	_           [24]byte
}

// ioUringCQE is a Completion Queue Entry (16 bytes).
type ioUringCQE struct {
	UserData uint64
	Res      int32 // syscall return value; negative errno on failure
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
// io_uring_enter(2), reducing kernel crossings from N to 1 per batch.
//
// # Requirements
//
// Linux 5.11+ for IORING_OP_UNLINKAT. Transparent fallback to
// [GoroutineBatcher] on older kernels or when io_uring_setup fails.
//
// # Path lifetime guarantee
//
// IORING_OP_UNLINKAT requires the path-string pointer to remain valid until
// the corresponding CQE is harvested. All path byte slices are kept alive in
// pendingUnlink.pathBuf until flushWithRing returns. runtime.KeepAlive is
// called at the end of flushWithRing to prevent the GC from collecting them
// before the CQEs are read.
type IOURingBatcher struct {
	view    MergedView
	entries uint32 // SQ ring capacity; must be a power of two

	mu      sync.Mutex
	pending []pendingUnlink // ops waiting for the next Flush
	closed  bool
}

// pendingUnlink holds the data for one pending unlinkat SQE.
type pendingUnlink struct {
	op      BatchOp
	pathBuf []byte // NUL-terminated absolute path; pinned until CQE is harvested
	isDir   bool   // true → set AT_REMOVEDIR in sqe->unlink_flags
}

// IOURingBatcherOption is a functional option for [IOURingBatcher].
type IOURingBatcherOption func(*IOURingBatcher)

// WithRingEntries sets the io_uring SQ ring capacity.
// Must be a power of two; values outside [64, 32768] are ignored.
// Larger values reduce stalls when batches exceed the ring size. Default: 256.
func WithRingEntries(n uint32) IOURingBatcherOption {
	return func(b *IOURingBatcher) {
		if n >= 64 && n <= 32768 {
			b.entries = n
		}
	}
}

// NewIOURingBatcher constructs an [IOURingBatcher] targeting view.
//
// When io_uring with IORING_OP_UNLINKAT is unavailable (kernel < 5.11,
// seccomp restriction, etc.) the function transparently returns a
// [GoroutineBatcher] so callers never need to branch on platform capability.
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
// We verify that params.Features contains IORING_FEAT_SQPOLL_NONFIXED (0x80),
// which was added in Linux 5.11 — the same release as IORING_OP_UNLINKAT.
// This eliminates the need for a more expensive IORING_REGISTER_PROBE call.
//
// A fresh ring fd is opened and immediately closed; we never mmap it.
func ioURingAvailable() bool {
	var params ioUringParams
	fd, _, errno := syscall.Syscall(sysIOURingSetup, 1,
		uintptr(unsafe.Pointer(&params)), 0)
	if errno != 0 || fd == 0 {
		return false
	}
	// Exactly one close — no deferred close plus explicit close anywhere.
	_ = syscall.Close(int(fd))
	return params.Features&requiredFeatureFlags == requiredFeatureFlags
}

// Submit implements [Batcher].
// O(1): builds the NUL-terminated path buffer and appends to the pending list.
func (b *IOURingBatcher) Submit(_ context.Context, op BatchOp) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBatcherClosed
	}

	absPath := b.view.AbsPath(op.RelPath)
	pathBuf := append([]byte(absPath), 0) // NUL-terminated for the kernel

	b.pending = append(b.pending, pendingUnlink{
		op:      op,
		pathBuf: pathBuf,
		isDir:   op.Kind == OpRemoveAll,
	})
	return nil
}

// Flush implements [Batcher].
//
// Prepares all pending unlinks as SQEs, submits them in ring-sized chunks with
// a single io_uring_enter(2) per chunk, then harvests all CQEs for error
// reporting.
//
// For OpRemoveAll, IORING_OP_UNLINKAT behaves like rmdir(2) — it rejects
// non-empty directories with ENOTEMPTY. When that happens Flush falls back to
// view.RemoveAll, which handles non-empty trees recursively.
func (b *IOURingBatcher) Flush(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBatcherClosed
	}
	work := b.pending
	b.pending = nil // nil (not [:0]) to release the backing array
	b.mu.Unlock()

	if len(work) == 0 {
		return nil
	}
	return b.flushWithRing(ctx, work)
}

// Close implements [Batcher]. Idempotent: a second call returns nil immediately.
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
// flushWithRing — core submit+harvest cycle
// ─────────────────────────────────────────────────────────────────────────────

// flushWithRing creates a fresh io_uring ring, writes all work items as SQEs,
// submits them in ring-capacity chunks, and harvests CQEs.
//
// # Memory-ordering requirements
//
// The SQ tail pointer is shared between user-space and the kernel:
//   - All SQE slots must be fully written BEFORE the tail is advanced.
//   - atomic.StoreUint32 provides a store-release barrier on every
//     Go-supported architecture. Plain assignment (*p = v) provides no such
//     guarantee and can cause the kernel to observe an inconsistent ring state.
//
// Similarly, the CQ head must be read with load-acquire semantics and written
// back with store-release after consuming CQEs to free slots.
// atomic.LoadUint32 / atomic.StoreUint32 provide both.
func (b *IOURingBatcher) flushWithRing(ctx context.Context, work []pendingUnlink) error {
	// Size the ring: min(len(work), b.entries), rounded up to next power of two.
	ringSize := nextPowerOfTwo(uint32(len(work)))
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
		return b.fallbackToGoroutinePool(ctx, work)
	}
	// Single deferred close — no other close calls in this function.
	defer syscall.Close(int(ringFD)) //nolint:errcheck

	if params.Features&requiredFeatureFlags != requiredFeatureFlags {
		return b.fallbackToGoroutinePool(ctx, work)
	}

	// Map the three ring regions into user-space memory.
	sqRingSize := params.SqOff.Array + params.SqEntries*4
	sqRing, err := syscall.Mmap(int(ringFD), ioRingOffSQRing, int(sqRingSize), mmapProt, mmapFlags)
	if err != nil {
		return b.fallbackToGoroutinePool(ctx, work)
	}
	defer syscall.Munmap(sqRing) //nolint:errcheck

	sqeRegionSize := uintptr(params.SqEntries) * unsafe.Sizeof(ioUringSQE{})
	sqeRegion, err := syscall.Mmap(int(ringFD), ioRingOffSQEs, int(sqeRegionSize), mmapProt, mmapFlags)
	if err != nil {
		return b.fallbackToGoroutinePool(ctx, work)
	}
	defer syscall.Munmap(sqeRegion) //nolint:errcheck

	cqRingSize := params.CqOff.Cqes + params.CqEntries*uint32(unsafe.Sizeof(ioUringCQE{}))
	cqRing, err := syscall.Mmap(int(ringFD), ioRingOffCQRing, int(cqRingSize), mmapProt, mmapFlags)
	if err != nil {
		return b.fallbackToGoroutinePool(ctx, work)
	}
	defer syscall.Munmap(cqRing) //nolint:errcheck

	// Cache ring-control pointers. RingMask values never change after setup.
	sqTailPtr := (*uint32)(unsafe.Pointer(&sqRing[params.SqOff.Tail]))
	sqMask := atomic.LoadUint32((*uint32)(unsafe.Pointer(&sqRing[params.SqOff.RingMask])))
	sqArrayPtr := (*uint32)(unsafe.Pointer(&sqRing[params.SqOff.Array]))
	cqHeadPtr := (*uint32)(unsafe.Pointer(&cqRing[params.CqOff.Head]))
	cqMask := atomic.LoadUint32((*uint32)(unsafe.Pointer(&cqRing[params.CqOff.RingMask])))

	var (
		errs      []error
		submitted int
	)

	for chunkStart := 0; chunkStart < len(work); {
		chunk := work[chunkStart:]
		if uint32(len(chunk)) > ringSize {
			chunk = chunk[:ringSize]
		}

		// ── Populate SQEs ───────────────────────────────────────────────────
		for j, w := range chunk {
			slotIdx := (uint32(submitted) + uint32(j)) & sqMask

			sqePtr := (*ioUringSQE)(unsafe.Pointer(
				uintptr(unsafe.Pointer(&sqeRegion[0])) +
					uintptr(slotIdx)*unsafe.Sizeof(ioUringSQE{}),
			))

			// AT_REMOVEDIR goes in OpcodeFlags (sqe->unlink_flags), never in Len.
			// The kernel rejects non-zero Len or Off with EINVAL.
			*sqePtr = ioUringSQE{
				Opcode:   ioRingOpUnlinkat,
				Fd:       atFDCWD,
				Addr:     uint64(uintptr(unsafe.Pointer(&w.pathBuf[0]))),
				UserData: uint64(chunkStart + j), // index into work for CQE attribution
				// Len:  0  (zero-value, required)
				// Off:  0  (zero-value, required)
			}
			if w.isDir {
				sqePtr.OpcodeFlags = atRemoveDir
			}

			// Publish this SQE's slot index into the SQ index array.
			(*[1 << 28]uint32)(unsafe.Pointer(sqArrayPtr))[slotIdx] = slotIdx
		}

		// Store-release: advance the SQ tail only after all SQE slots above are
		// fully written. The kernel must never see the tail advance before the
		// SQEs themselves are visible in the shared memory region.
		curTail := atomic.LoadUint32(sqTailPtr)
		atomic.StoreUint32(sqTailPtr, curTail+uint32(len(chunk)))

		// ── Submit and wait ─────────────────────────────────────────────────
		ret, _, enterErrno := syscall.Syscall6(sysIOURingEnter,
			ringFD,
			uintptr(len(chunk)),           // to_submit
			uintptr(len(chunk)),           // min_complete (wait for all)
			uintptr(ioRingEnterGetEvents), // flags
			0, 0)
		if enterErrno != 0 || int(ret) != len(chunk) {
			// io_uring_enter failed: fall back to the goroutine pool for this chunk.
			if ferr := executeBatch(ctx, b.view, opsFromPending(chunk), runtime.NumCPU()); ferr != nil {
				errs = append(errs, ferr)
			}
			chunkStart += len(chunk)
			submitted += len(chunk)
			continue
		}

		// ── Harvest CQEs ────────────────────────────────────────────────────
		// Load-acquire: read the CQ head before reading CQE content.
		cqHead := atomic.LoadUint32(cqHeadPtr)

		for j := 0; j < len(chunk); j++ {
			cqePtr := (*ioUringCQE)(unsafe.Pointer(
				uintptr(unsafe.Pointer(&cqRing[params.CqOff.Cqes])) +
					uintptr((cqHead+uint32(j))&cqMask)*unsafe.Sizeof(ioUringCQE{}),
			))

			if cqePtr.Res >= 0 {
				continue // success
			}

			kernelErr := syscall.Errno(-cqePtr.Res)
			workItem := work[int(cqePtr.UserData)]

			switch kernelErr {
			case syscall.ENOENT:
				// Path already absent — idempotent, not an error.

			case syscall.ENOTEMPTY:
				// Non-empty directory: IORING_OP_UNLINKAT is equivalent to
				// rmdir(2) and rejects non-empty dirs. Fall back to the
				// recursive RemoveAll which descends the entire tree.
				if ferr := b.view.RemoveAll(ctx, workItem.op.RelPath); ferr != nil {
					errs = append(errs, fmt.Errorf("removeAll fallback %q: %w", workItem.op.RelPath, ferr))
				}

			case syscall.EINVAL:
				// The opcode was rejected by seccomp or the SQE was malformed.
				// Fall back synchronously so the deletion still occurs.
				if ferr := executeOp(ctx, b.view, workItem.op); ferr != nil {
					errs = append(errs, fmt.Errorf("sync fallback %q: %w", workItem.op.RelPath, ferr))
				}

			default:
				errs = append(errs, fmt.Errorf("io_uring unlinkat %q: %w", workItem.op.RelPath, kernelErr))
			}
		}

		// Store-release: advance the CQ head to free the consumed CQE slots,
		// signalling to the kernel that the ring space is available for new CQEs.
		atomic.StoreUint32(cqHeadPtr, cqHead+uint32(len(chunk)))

		chunkStart += len(chunk)
		submitted += len(chunk)
	}

	// Prevent the GC from collecting pathBuf slices before all CQEs have been
	// read above. The GC may otherwise reclaim them after the last Go reference
	// to work[i].pathBuf goes out of scope in the loop.
	runtime.KeepAlive(work)

	return joinErrors(errs)
}

// fallbackToGoroutinePool routes all work through the goroutine-pool path.
// Called when ring setup or mmap fails mid-Flush.
func (b *IOURingBatcher) fallbackToGoroutinePool(ctx context.Context, work []pendingUnlink) error {
	return executeBatch(ctx, b.view, opsFromPending(work), runtime.NumCPU())
}

// opsFromPending extracts the [BatchOp] from each [pendingUnlink].
func opsFromPending(work []pendingUnlink) []BatchOp {
	ops := make([]BatchOp, len(work))
	for i, w := range work {
		ops[i] = w.op
	}
	return ops
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// nextPowerOfTwo returns the smallest power of two ≥ n.
// Used to round ring sizes up to a valid power-of-two io_uring entry count.
func nextPowerOfTwo(n uint32) uint32 {
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
// IORING_OP_UNLINKAT (Linux 5.11+). Exported so callers can log or instrument
// which batcher path was selected at startup.
func IOURingAvailable() bool { return ioURingAvailable() }

// NewBestBatcher returns the highest-performance [Batcher] available on the
// current platform. On Linux 5.11+ this is an [IOURingBatcher]; on older
// kernels or when io_uring setup fails it is a [GoroutineBatcher].
func NewBestBatcher(view MergedView) (Batcher, error) {
	return NewIOURingBatcher(view)
}

// _ keeps the "os" import alive on this build-constrained file.
// os.ErrNotExist is referenced in error checks elsewhere in the package.
var _ = os.ErrNotExist

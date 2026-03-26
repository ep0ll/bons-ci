package cniprovider

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/trace"
)

// aboveTargetGracePeriod is the minimum idle time before a namespace above
// the target pool size is eligible for eviction.  Prevents thrashing when a
// build burst temporarily inflates the pool.
const aboveTargetGracePeriod = 5 * time.Minute

// fillRetry* bound the exponential backoff used by the pool filler.
const (
	fillRetryDelay    = 1 * time.Second
	fillRetryMaxDelay = 30 * time.Second
)

// nsAllocator is the narrow creation interface consumed by nsPool.
// Keeping it separate from cniProvider enables isolated unit tests.
type nsAllocator interface {
	newNS(ctx context.Context, hostname string) (*cniNS, error)
}

// nsPool is a LIFO, size-bounded cache of pre-created cniNS instances.
//
// # Ownership invariant
//
//	actualSize = len(available) + in_use_count
//
// "In-use" namespaces have been returned by get() but not yet returned via put().
// available is ordered LRU→MRU; get() pops from the tail (MRU, LIFO).
// LRU eviction trims from the head.
//
// # Lifecycle
//
//  1. newNSPool(targetSize, alloc) – create (no goroutines started).
//  2. startFill(ctx)               – start background prefill goroutine.
//  3. get(ctx) / put(ns)           – normal acquire / release path.
//  4. close()                      – drain, release all, wait for goroutine.
//
// All methods are goroutine-safe.
type nsPool struct {
	alloc      nsAllocator
	targetSize int

	mu         sync.Mutex
	actualSize int
	available  []*cniNS
	closed     bool

	// cleanupScheduled prevents concurrent cleanup goroutines.
	// A CAS in put() ensures at most one time.AfterFunc is pending at once,
	// eliminating the timer storm present in the original implementation.
	cleanupScheduled atomic.Bool

	// fillDone is closed when the background fill goroutine exits.
	fillDone chan struct{}
}

func newNSPool(targetSize int, alloc nsAllocator) *nsPool {
	return &nsPool{
		alloc:      alloc,
		targetSize: targetSize,
		fillDone:   make(chan struct{}),
	}
}

// startFill launches the background prefill goroutine.  It fills up to
// targetSize, backing off exponentially on error, and exits when the pool is
// full, ctx is cancelled, or the pool is closed.
func (p *nsPool) startFill(ctx context.Context) {
	go func() {
		defer close(p.fillDone)
		p.fillLoop(ctx)
	}()
}

func (p *nsPool) fillLoop(ctx context.Context) {
	delay := fillRetryDelay

	for {
		p.mu.Lock()
		closed := p.closed
		need := p.targetSize - p.actualSize
		p.mu.Unlock()

		if closed {
			return
		}
		if need <= 0 {
			// Target reached; the filler's job is done.  It does not
			// auto-restart after eviction — eviction is intentional.
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		ns, err := p.allocNew(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			bklog.G(ctx).Errorf("cniprovider: pool fill failed (retry in %s): %v", delay, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay *= 2; delay > fillRetryMaxDelay {
				delay = fillRetryMaxDelay
			}
			continue
		}

		delay = fillRetryDelay // reset on success
		p.put(ns)
	}
}

// get returns a namespace, reusing a pooled one (LIFO/MRU) when available, or
// allocating a fresh one on cache miss.
func (p *nsPool) get(ctx context.Context) (*cniNS, error) {
	p.mu.Lock()
	if n := len(p.available); n > 0 {
		ns := p.available[n-1]
		p.available = p.available[:n-1]
		p.mu.Unlock()
		trace.SpanFromContext(ctx).AddEvent("returning network namespace from pool")
		bklog.G(ctx).Debugf("cniprovider: reusing namespace %s from pool", ns.id)
		return ns, nil
	}
	p.mu.Unlock()

	return p.allocNew(ctx)
}

// allocNew creates a new namespace and registers it with the pool.
//
// BUG FIX — original implementation race:
// The original code created the namespace outside the lock, then re-acquired
// the lock to increment actualSize.  If the pool was closed in between, it
// returned an error without releasing the namespace, leaking an OS network
// namespace and its kernel memory.  This version detects the closed state and
// calls release() before returning the error.
func (p *nsPool) allocNew(ctx context.Context) (retNS *cniNS, retErr error) {
	var ns *cniNS

	err := withDetachedNetNSIfAny(ctx, func(ctx context.Context) error {
		var err error
		ns, err = p.alloc.newNS(ctx, "")
		return err
	})
	if err != nil {
		return nil, err
	}

	ns.pool = p

	p.mu.Lock()
	if p.closed {
		// Pool closed between namespace creation and this point.
		// We must not hold mu during release() (potentially slow kernel
		// teardown), so unlock, release, and report the error.
		p.mu.Unlock()
		if relErr := ns.release(); relErr != nil {
			bklog.L.WithError(relErr).Warnf("cniprovider: namespace %s leaked after pool closed", ns.id)
		}
		return nil, errors.New("cniprovider: pool is closed")
	}
	p.actualSize++
	p.mu.Unlock()

	return ns, nil
}

// put returns a namespace to the pool.  If the pool is closed, the namespace
// is released immediately.  Schedules a single LRU cleanup pass (at most one
// at a time) when the pool is above its target size.
func (p *nsPool) put(ns *cniNS) {
	ns.lastUsed = time.Now()

	p.mu.Lock()

	if p.closed {
		p.mu.Unlock()
		if err := ns.release(); err != nil {
			bklog.L.WithError(err).Warnf("cniprovider: failed to release namespace %s (pool closed)", ns.id)
		}
		return
	}

	p.available = append(p.available, ns)
	aboveTarget := p.actualSize > p.targetSize
	p.mu.Unlock()

	// Schedule cleanup at most once per grace period (CAS prevents storms).
	if aboveTarget && p.cleanupScheduled.CompareAndSwap(false, true) {
		time.AfterFunc(aboveTargetGracePeriod, p.runCleanup)
	}
}

// runCleanup is the time.AfterFunc callback.
func (p *nsPool) runCleanup() {
	defer p.cleanupScheduled.Store(false)
	p.cleanupToTargetSize()
}

// cleanupToTargetSize evicts the least-recently-used namespaces until
// actualSize ≤ targetSize, subject to the grace-period idleness constraint.
func (p *nsPool) cleanupToTargetSize() {
	// Collect under lock, release outside lock to avoid holding mu during
	// potentially slow kernel netns teardown.
	var toRelease []*cniNS

	p.mu.Lock()
	for p.actualSize > p.targetSize &&
		len(p.available) > 0 &&
		time.Since(p.available[0].lastUsed) >= aboveTargetGracePeriod {

		ns := p.available[0]
		p.available = p.available[1:]
		p.actualSize--
		toRelease = append(toRelease, ns)
		bklog.L.Debugf("cniprovider: evicting idle namespace %s", ns.id)
	}
	p.mu.Unlock()

	for _, ns := range toRelease {
		if err := ns.release(); err != nil {
			bklog.L.WithError(err).Warnf("cniprovider: error evicting namespace %s", ns.id)
		}
	}
}

// close marks the pool as closed, releases all available namespaces, and
// waits for the fill goroutine to exit.
//
// In-use namespaces are NOT released here; put() handles them when returned.
func (p *nsPool) close() {
	bklog.L.Debug("cniprovider: closing namespace pool")

	p.mu.Lock()
	p.closed = true
	toRelease := p.available
	p.available = nil
	p.actualSize -= len(toRelease)
	p.mu.Unlock()

	for _, ns := range toRelease {
		if err := ns.release(); err != nil {
			bklog.L.WithError(err).Warnf("cniprovider: error releasing namespace %s during pool close", ns.id)
		}
	}

	<-p.fillDone // wait for the fill goroutine
	bklog.L.Debug("cniprovider: namespace pool closed")
}

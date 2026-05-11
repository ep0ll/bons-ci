//go:build !linux

package dirsync

// IOURingAvailable always returns false on non-Linux platforms.
// io_uring is a Linux-specific kernel interface; this stub makes cross-platform
// builds compile cleanly without any conditional logic at call sites.
func IOURingAvailable() bool { return false }

// NewBestBatcher returns a [GoroutineBatcher] on non-Linux platforms.
// On Linux 5.11+ [NewBestBatcher] returns an [IOURingBatcher] that reduces
// kernel transitions to one io_uring_enter(2) call per Flush.
func NewBestBatcher(view MergedView) (Batcher, error) {
	return NewGoroutineBatcher(view), nil
}

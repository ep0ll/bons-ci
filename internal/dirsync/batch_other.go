//go:build !linux

package differ

// IOURingAvailable always returns false on non-Linux platforms.
func IOURingAvailable() bool { return false }

// NewBestBatcher returns a [GoroutineBatcher] on non-Linux platforms.
// io_uring is a Linux-specific facility.
func NewBestBatcher(view MergedView) (Batcher, error) {
	return NewGoroutineBatcher(view), nil
}

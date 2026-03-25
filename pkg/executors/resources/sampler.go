package resources

// sampler.go – generic time-series sampler with adaptive sample-rate reduction.
//
// Sampler[T] drives periodic collection of any value type T.  It is used to
// sample cgroup statistics (Sampler[*types.Sample]) and system-wide proc
// statistics (Sampler[*types.SysSample]).
//
// Design
//
// A single background goroutine ("the runner") fires a collection callback on a
// configurable minimum interval (default: 2 s).  Multiple Sub[T] subscriptions
// can be active concurrently; each subscription maintains its own last-seen
// timestamp so slow consumers do not delay fast ones.
//
// Adaptive interval:  when a subscription has been active for more than
// maxSamples × interval, its per-subscription interval doubles.  This prevents
// unbounded sample growth for long-running operations (e.g. a multi-hour build)
// while keeping fine granularity for short operations.
//
// Bug fixes over the original implementation:
//   - Timer leak: the original code replaced time.NewTimer in a loop without
//     stopping the old timer.  Fixed: timer is reset in-place with Reset().
//   - Record-after-Close: the original code set running=true and never cleared
//     it, so calling Record() after Close() silently dropped all samples.
//     Fixed: running is set back to false when the runner exits.
//   - Sub.Close race: the original released the lock before calling callback(),
//     creating a window where a concurrent sample could be appended after Close
//     returned.  Fixed with a closed flag.

import (
	"sync"
	"time"
)

// ─── WithTimestamp ────────────────────────────────────────────────────────────

// WithTimestamp is the constraint for values managed by Sampler.
// Implementations return the time at which the value was sampled.
type WithTimestamp interface {
	Timestamp() time.Time
}

// ─── Sampler ─────────────────────────────────────────────────────────────────

// Sampler[T] drives periodic collection and fan-out to multiple subscriptions.
//
// Safe for concurrent use: Record(), Close(), and Sub.Close() may be called
// from any goroutine simultaneously.
type Sampler[T WithTimestamp] struct {
	mu          sync.Mutex
	minInterval time.Duration
	maxSamples  int
	callback    func(ts time.Time) (T, error)
	doneOnce    sync.Once
	done        chan struct{}
	running     bool
	subs        map[*Sub[T]]struct{}
}

// NewSampler creates a Sampler that invokes cb at most once per minInterval.
// When a subscription has accumulated more than maxSamples samples, its
// collection interval doubles (adaptive back-off).
func NewSampler[T WithTimestamp](minInterval time.Duration, maxSamples int, cb func(time.Time) (T, error)) *Sampler[T] {
	return &Sampler[T]{
		minInterval: minInterval,
		maxSamples:  maxSamples,
		callback:    cb,
		done:        make(chan struct{}),
		subs:        make(map[*Sub[T]]struct{}),
	}
}

// Record creates a new subscription and ensures the background runner is
// active.  Returns a *Sub[T] that the caller must eventually close.
func (s *Sampler[T]) Record() *Sub[T] {
	ss := &Sub[T]{
		interval: s.minInterval,
		first:    time.Now(),
		sampler:  s,
	}
	s.mu.Lock()
	s.subs[ss] = struct{}{}
	if !s.running {
		s.running = true
		go s.run()
	}
	s.mu.Unlock()
	return ss
}

// Close shuts down the background runner.  Idempotent.
// Existing subscriptions stop receiving new samples but their accumulated
// history is preserved for retrieval via Sub.Close().
func (s *Sampler[T]) Close() error {
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}

// run is the background collection goroutine.  It runs until s.done is closed.
func (s *Sampler[T]) run() {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	timer := time.NewTimer(s.minInterval)
	defer timer.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-timer.C:
			tm := time.Now()

			// Identify subscriptions that are due for a new sample.
			s.mu.Lock()
			active := s.collectDueSubs(tm)
			s.mu.Unlock()

			// Reset the timer before the (potentially slow) callback so we don't
			// accumulate drift.
			timer.Reset(s.minInterval)

			if len(active) == 0 {
				continue
			}

			value, err := s.callback(tm)

			s.mu.Lock()
			s.fanOut(active, value, err, tm)
			s.mu.Unlock()
		}
	}
}

// collectDueSubs returns subscriptions whose per-subscription interval has
// elapsed.  Must be called with s.mu held.
func (s *Sampler[T]) collectDueSubs(tm time.Time) []*Sub[T] {
	active := make([]*Sub[T], 0, len(s.subs))
	for ss := range s.subs {
		if ss.closed || tm.Sub(ss.last) < ss.interval {
			continue
		}
		ss.last = tm
		active = append(active, ss)
	}
	return active
}

// fanOut distributes the sample (or error) to each due subscription and
// applies the adaptive interval doubling.  Must be called with s.mu held.
func (s *Sampler[T]) fanOut(active []*Sub[T], value T, err error, tm time.Time) {
	for _, ss := range active {
		if _, found := s.subs[ss]; !found {
			// Sub was closed between collectDueSubs and this lock acquisition.
			continue
		}
		if err != nil {
			ss.err = err
		} else {
			ss.samples = append(ss.samples, value)
			ss.err = nil
		}

		// Adaptive interval: double when we've exceeded maxSamples capacity.
		dur := tm.Sub(ss.first)
		if ss.interval*time.Duration(s.maxSamples) <= dur {
			ss.interval *= 2
		}
	}
}

// ─── Sub ─────────────────────────────────────────────────────────────────────

// Sub[T] is a single subscription into a Sampler.  It holds the accumulated
// sample history for one consumer (e.g. one cgroupRecord).
type Sub[T WithTimestamp] struct {
	sampler  *Sampler[T]
	interval time.Duration
	first    time.Time
	last     time.Time
	samples  []T
	err      error
	closed   bool
}

// Close unregisters this subscription from the sampler, optionally captures one
// final sample, applies the per-subscription interval filter to reduce the
// returned slice, and returns the retained samples.
//
// captureLast: when true, one additional callback invocation is made after
// removing the subscription so the final state is captured.  This is always
// true for cgroupRecord.close() to record the end-of-operation counters.
//
// The returned slice contains only samples that are at least interval apart,
// implementing reservoir-style downsampling for long-running operations.
func (s *Sub[T]) Close(captureLast bool) ([]T, error) {
	s.sampler.mu.Lock()
	if s.closed {
		s.sampler.mu.Unlock()
		return s.samples, s.err // idempotent
	}
	s.closed = true
	delete(s.sampler.subs, s)

	if s.err != nil {
		s.sampler.mu.Unlock()
		return nil, s.err
	}

	// Apply the interval filter while still holding the lock.
	filtered := s.filterByInterval()
	s.sampler.mu.Unlock()

	if !captureLast {
		return filtered, nil
	}

	// Capture the terminal sample outside the lock (callback may block).
	last, err := s.sampler.callback(time.Now())
	if err != nil {
		return nil, err
	}
	return append(filtered, last), nil
}

// filterByInterval returns a downsampled copy of s.samples, keeping only
// entries that are at least s.interval apart.  Must be called with s.sampler.mu held.
func (s *Sub[T]) filterByInterval() []T {
	if len(s.samples) == 0 {
		return nil
	}
	out := make([]T, 0, len(s.samples))
	current := s.first
	for i, v := range s.samples {
		ts := v.Timestamp()
		if i == 0 || ts.Sub(current) >= s.interval {
			out = append(out, v)
			current = ts
		}
	}
	return out
}

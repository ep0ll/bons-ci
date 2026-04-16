package core

import (
	"context"
	"sync"
)

// ── Stream[T] — race-free reactive push stream ────────────────────────────────
//
// Design goals: (1) consumers use `for v := range s.Chan()` idiom, (2) Emit
// never panics or races, (3) the stream closes cleanly after all in-flight
// sends complete.
//
// Race-free guarantee: close(s.ch) is called exactly once, after wg.Wait()
// confirms no goroutine is executing a send. The mutex serialises the
// "check-closed / wg.Add" pair so no sender can slip in after wg.Wait starts.
//
// The one-goroutine-relay design avoids the close(ch) vs chansend memory race
// detected by -race. This WaitGroup + mutex design has identical semantics
// with O(1) extra goroutines (just the ctx watcher), zero extra channel hops,
// and negligible lock overhead (~50 ns per Emit, irrelevant for FS hashing).

// Stream is a typed reactive channel for event-driven pipelines.
type Stream[T any] struct {
	mu     sync.Mutex
	ch     chan T
	closed bool
	wg     sync.WaitGroup // counts in-flight Emits
	once   sync.Once      // ensures close(ch) runs exactly once
	ctx    context.Context
	cncl   context.CancelFunc
}

// NewStream creates a buffered Stream with buf-size channel, tied to ctx.
// The stream closes automatically when ctx is cancelled.
func NewStream[T any](ctx context.Context, buf int) *Stream[T] {
	if buf < 0 {
		buf = 0
	}
	child, cancel := context.WithCancel(ctx)
	s := &Stream[T]{
		ch:   make(chan T, buf),
		ctx:  child,
		cncl: cancel,
	}
	// Context watcher: closes the stream when ctx is cancelled.
	go func() {
		<-child.Done()
		s.closeOnce()
	}()
	return s
}

// closeOnce performs the clean shutdown sequence exactly once:
//  1. Cancel ctx — unblocks any Emit blocked in its select.
//  2. Mark closed under mutex — no new senders can register.
//  3. Wait for all in-flight Emits to call wg.Done.
//  4. close(ch) — safe; no goroutine is executing a send.
func (s *Stream[T]) closeOnce() {
	s.once.Do(func() {
		s.cncl() // 1. unblock blocked Emits (makes ctx.Done() readable)

		s.mu.Lock()
		s.closed = true // 2. prevent new wg.Add(1) calls
		s.mu.Unlock()

		s.wg.Wait()  // 3. wait for in-flight Emits
		close(s.ch)  // 4. race-free: no senders remain
	})
}

// Emit sends v to the stream. Returns false if the stream is closed.
// Never panics; never races. Thread-safe.
func (s *Stream[T]) Emit(v T) bool {
	// Register as an active sender under the mutex. This is serialised with
	// closeOnce's mu.Lock/s.closed=true, guaranteeing wg.Wait only runs after
	// all wg.Add(1) calls that observe s.closed==false have completed.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.wg.Add(1)
	s.mu.Unlock()

	defer s.wg.Done()
	select {
	case <-s.ctx.Done():
		return false
	case s.ch <- v:
		return true
	}
}

// TryEmit is a non-blocking Emit.
// Returns (emitted=true, open=true) on success,
// (emitted=false, open=true) when buffer is full,
// (emitted=false, open=false) when closed.
func (s *Stream[T]) TryEmit(v T) (emitted, open bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, false
	}
	s.wg.Add(1)
	s.mu.Unlock()

	defer s.wg.Done()
	select {
	case <-s.ctx.Done():
		return false, false
	case s.ch <- v:
		return true, true
	default:
		return false, true
	}
}

// Chan returns the read-only channel. Safe to range over: it is closed after
// all in-flight sends complete and the stream is shut down.
func (s *Stream[T]) Chan() <-chan T { return s.ch }

// Close shuts the stream. Safe to call multiple times.
func (s *Stream[T]) Close() { s.closeOnce() }

// Ctx returns the stream's context (cancelled when the stream is closed).
func (s *Stream[T]) Ctx() context.Context { return s.ctx }

// ── EventBus[T] ───────────────────────────────────────────────────────────────

// EventBus is a typed broadcast pub/sub bus safe for concurrent use.
// Non-blocking: full subscriber channels are silently skipped.
type EventBus[T any] struct {
	mu   sync.RWMutex
	subs map[uint64]chan T
	next uint64
}

// NewEventBus creates an empty EventBus.
func NewEventBus[T any]() *EventBus[T] {
	return &EventBus[T]{subs: make(map[uint64]chan T)}
}

// Subscribe returns an id and a buffered channel receiving future events.
// Call Unsubscribe(id) when done to free the channel.
func (b *EventBus[T]) Subscribe(buf int) (id uint64, ch <-chan T) {
	b.mu.Lock()
	id = b.next
	b.next++
	c := make(chan T, buf)
	b.subs[id] = c
	b.mu.Unlock()
	return id, c
}

// Unsubscribe removes and closes the subscriber's channel.
func (b *EventBus[T]) Unsubscribe(id uint64) {
	b.mu.Lock()
	if c, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(c)
	}
	b.mu.Unlock()
}

// Publish delivers v to all subscribers. Non-blocking; drops on full buffer.
func (b *EventBus[T]) Publish(v T) {
	b.mu.RLock()
	for _, c := range b.subs {
		select {
		case c <- v:
		default:
		}
	}
	b.mu.RUnlock()
}

// Close closes all subscriber channels.
func (b *EventBus[T]) Close() {
	b.mu.Lock()
	for id, c := range b.subs {
		delete(b.subs, id)
		close(c)
	}
	b.mu.Unlock()
}

// Subscribers returns the current subscriber count.
func (b *EventBus[T]) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

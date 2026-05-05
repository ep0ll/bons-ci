// Package singleflight provides duplicate function-call suppression.
// It is a zero-dependency reimplementation of golang.org/x/sync/singleflight
// using only the Go standard library.
//
// Semantics are identical to the upstream package:
//   - Concurrent callers with the same key block until the first call completes.
//   - All blocked callers receive the same (value, error) result.
//   - The shared bool return is true for all callers that shared a result.
//   - Panics in the called function propagate to all callers of Do.
package singleflight

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
)

// errGoexit is a sentinel used to distinguish runtime.Goexit from a panic.
var errGoexit = fmt.Errorf("runtime.Goexit was called")

// panicError wraps a recovered panic value together with its stack trace.
type panicError struct {
	value any
	stack []byte
}

func (p *panicError) Error() string {
	return fmt.Sprintf("singleflight: do function panicked: %v\n\n%s", p.value, p.stack)
}

// call is an in-flight or completed singleflight call.
type call struct {
	wg sync.WaitGroup

	// These fields are written once (before wg.Done) and then read-only.
	val    any
	err    error
	forgot bool // set by Forget while in-flight
	dups   int  // number of extra callers that joined
}

// Group manages a set of in-flight calls, keyed by string.
// The zero value is ready to use. Must not be copied after first use.
type Group struct {
	mu sync.Mutex
	m  map[string]*call
}

// Result holds the result of a Do call.
type Result struct {
	Val    any
	Err    error
	Shared bool
}

// Do executes and returns the results of the given function, making sure that
// only one execution is in-flight for a given key at a time. If a duplicate
// call comes in, the duplicate caller waits for the original to complete and
// receives the same results. The return value shared indicates whether val
// was given to multiple callers.
func (g *Group) Do(key string, fn func() (any, error)) (v any, err error, shared bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		c.dups++
		g.mu.Unlock()
		c.wg.Wait()

		if e, ok := c.err.(*panicError); ok {
			panic(e)
		} else if c.err == errGoexit {
			runtime.Goexit()
		}
		return c.val, c.err, true
	}

	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	g.doCall(c, key, fn)
	return c.val, c.err, c.dups > 0
}

// DoChan is like Do but returns a channel that will receive the result when
// it is ready. The channel will not be closed.
func (g *Group) DoChan(key string, fn func() (any, error)) <-chan Result {
	ch := make(chan Result, 1)
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		c.dups++
		g.mu.Unlock()
		go func() {
			c.wg.Wait()
			if e, ok := c.err.(*panicError); ok {
				panic(e)
			} else if c.err == errGoexit {
				// Do nothing for Goexit in background goroutine.
				return
			}
			ch <- Result{c.val, c.err, true}
		}()
		return ch
	}

	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	go func() {
		g.doCall(c, key, fn)
		ch <- Result{c.val, c.err, c.dups > 0}
	}()
	return ch
}

func (g *Group) doCall(c *call, key string, fn func() (any, error)) {
	normalReturn := false
	recovered := false

	// Use a double-defer to detect whether fn called runtime.Goexit.
	defer func() {
		// The first defer handles the final cleanup.
		if !normalReturn && !recovered {
			c.err = errGoexit
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		c.wg.Done()
		if g.m[key] == c {
			delete(g.m, key)
		}
	}()

	func() {
		defer func() {
			if !normalReturn {
				if r := recover(); r != nil {
					c.err = &panicError{
						value: r,
						stack: debug.Stack(),
					}
				}
			}
		}()
		c.val, c.err = fn()
		normalReturn = true
	}()

	if !normalReturn {
		recovered = true
	}
}

// Forget tells the singleflight to forget about a key. Future calls to Do
// for this key will call the function rather than waiting for an existing call.
func (g *Group) Forget(key string) {
	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()
}



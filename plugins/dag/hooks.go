package reactdag

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ---------------------------------------------------------------------------
// HookRegistry
// ---------------------------------------------------------------------------

type hookEntry struct {
	id       uint64
	priority int // lower number runs first
	fn       HookFn
}

// HookRegistry is a thread-safe registry of named lifecycle hooks.
// Multiple hooks can be registered for each HookType and are executed in
// ascending priority order.
type HookRegistry struct {
	mu     sync.RWMutex
	hooks  map[HookType][]hookEntry
	nextID uint64
}

// NewHookRegistry constructs an empty HookRegistry.
func NewHookRegistry() *HookRegistry {
	return &HookRegistry{hooks: make(map[HookType][]hookEntry)}
}

// Register adds a hook function for the given type with a priority.
// Lower priority numbers execute first. Returns a deregister function.
func (r *HookRegistry) Register(hookType HookType, priority int, fn HookFn) (deregister func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	entry := hookEntry{id: id, priority: priority, fn: fn}
	r.hooks[hookType] = insertSorted(r.hooks[hookType], entry)
	return func() { r.deregister(hookType, id) }
}

// Execute runs all hooks registered for hookType in priority order.
// If any hook returns an error, execution stops and the error is returned.
func (r *HookRegistry) Execute(ctx context.Context, hookType HookType, v *Vertex, payload HookPayload) error {
	r.mu.RLock()
	entries := make([]hookEntry, len(r.hooks[hookType]))
	copy(entries, r.hooks[hookType])
	r.mu.RUnlock()

	var errs []error
	for _, e := range entries {
		if err := e.fn(ctx, v, payload); err != nil {
			errs = append(errs, fmt.Errorf("hook %s (id=%d): %w", hookType, e.id, err))
		}
	}
	return errors.Join(errs...)
}

// ExecuteAbortOnError runs hooks and stops at the first error.
// Use this for pre-condition hooks where failure should block the operation.
func (r *HookRegistry) ExecuteAbortOnError(ctx context.Context, hookType HookType, v *Vertex, payload HookPayload) error {
	r.mu.RLock()
	entries := make([]hookEntry, len(r.hooks[hookType]))
	copy(entries, r.hooks[hookType])
	r.mu.RUnlock()

	for _, e := range entries {
		if err := e.fn(ctx, v, payload); err != nil {
			return fmt.Errorf("hook %s (id=%d): %w", hookType, e.id, err)
		}
	}
	return nil
}

func (r *HookRegistry) deregister(hookType HookType, id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := r.hooks[hookType]
	for i, e := range entries {
		if e.id == id {
			r.hooks[hookType] = append(entries[:i], entries[i+1:]...)
			return
		}
	}
}

// insertSorted inserts e into a priority-ordered slice (ascending by priority).
func insertSorted(entries []hookEntry, e hookEntry) []hookEntry {
	i := 0
	for i < len(entries) && entries[i].priority <= e.priority {
		i++
	}
	entries = append(entries, hookEntry{})
	copy(entries[i+1:], entries[i:])
	entries[i] = e
	return entries
}

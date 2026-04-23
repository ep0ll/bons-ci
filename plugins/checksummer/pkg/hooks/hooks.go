// Package hooks provides a generic, priority-ordered, thread-safe hook registry.
// Every major operation in the engine exposes hook points so callers can inject
// custom logic without modifying core code (Open/Closed Principle).
package hooks

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// ─────────────────────────── Priority ────────────────────────────────────────

// Priority controls execution order within a registry. Lower numbers run first.
type Priority int

const (
	PriorityFirst  Priority = -100
	PriorityHigh   Priority = -50
	PriorityNormal Priority = 0
	PriorityLow    Priority = 50
	PriorityLast   Priority = 100
)

// ─────────────────────────── Error handling ───────────────────────────────────

// ErrorMode controls how Execute handles hook errors.
type ErrorMode int

const (
	StopOnError     ErrorMode = iota // abort on first error
	ContinueOnError                  // collect all errors
	IgnoreErrors                     // silently discard errors
)

// HookError wraps an error with hook identity.
type HookError struct {
	HookName string
	Err      error
}

func (e *HookError) Error() string { return fmt.Sprintf("hook %q: %v", e.HookName, e.Err) }
func (e *HookError) Unwrap() error { return e.Err }

// MultiError holds errors from multiple hooks (ContinueOnError mode).
type MultiError struct{ Errors []*HookError }

func (m *MultiError) Error() string {
	if len(m.Errors) == 0 {
		return "<no errors>"
	}
	s := fmt.Sprintf("%d hook error(s): %v", len(m.Errors), m.Errors[0])
	for _, e := range m.Errors[1:] {
		s += "; " + e.Error()
	}
	return s
}

// ─────────────────────────── Handler / Hook ───────────────────────────────────

// Handler is the typed hook function signature.
type Handler[T any] func(ctx context.Context, payload T) error

// Hook is a named, prioritised, togglable hook entry.
type Hook[T any] struct {
	Name     string
	Priority Priority
	handler  Handler[T]
	enabled  atomic.Bool
}

// NewHook creates an enabled hook.  Pass nil handler to create a no-op placeholder.
func NewHook[T any](name string, priority Priority, handler Handler[T]) *Hook[T] {
	h := &Hook[T]{Name: name, Priority: priority, handler: handler}
	h.enabled.Store(true)
	return h
}

func (h *Hook[T]) Enable()             { h.enabled.Store(true) }
func (h *Hook[T]) Disable()            { h.enabled.Store(false) }
func (h *Hook[T]) IsEnabled() bool     { return h.enabled.Load() }
func (h *Hook[T]) Handler() Handler[T] { return h.handler }

// ─────────────────────────── Registry ────────────────────────────────────────

// Registry manages an ordered, concurrent set of typed hooks.
type Registry[T any] struct {
	mu    sync.RWMutex
	hooks []*Hook[T]
	dirty atomic.Bool
}

// NewRegistry returns an empty Registry.
func NewRegistry[T any]() *Registry[T] { return &Registry[T]{} }

// Register adds h, replacing any existing hook with the same name.
func (r *Registry[T]) Register(h *Hook[T]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, ex := range r.hooks {
		if ex.Name == h.Name {
			r.hooks[i] = h
			r.dirty.Store(true)
			return
		}
	}
	r.hooks = append(r.hooks, h)
	r.dirty.Store(true)
}

// Unregister removes the named hook. Returns true if it existed.
func (r *Registry[T]) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, h := range r.hooks {
		if h.Name == name {
			r.hooks = append(r.hooks[:i], r.hooks[i+1:]...)
			return true
		}
	}
	return false
}

// SetEnabled toggles a named hook's enabled state.
func (r *Registry[T]) SetEnabled(name string, enabled bool) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, h := range r.hooks {
		if h.Name == name {
			if enabled {
				h.Enable()
			} else {
				h.Disable()
			}
			return true
		}
	}
	return false
}

// Len returns the number of registered hooks.
func (r *Registry[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.hooks)
}

// Execute runs all enabled hooks in priority order according to mode.
func (r *Registry[T]) Execute(ctx context.Context, payload T, mode ErrorMode) error {
	if r.dirty.Load() {
		r.sortLocked()
	}
	r.mu.RLock()
	snapshot := make([]*Hook[T], len(r.hooks))
	copy(snapshot, r.hooks)
	r.mu.RUnlock()

	var multi MultiError
	for _, h := range snapshot {
		if !h.IsEnabled() || h.handler == nil {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := h.handler(ctx, payload); err != nil {
			he := &HookError{HookName: h.Name, Err: err}
			switch mode {
			case StopOnError:
				return he
			case ContinueOnError:
				multi.Errors = append(multi.Errors, he)
			case IgnoreErrors:
				// discard
			}
		}
	}
	if len(multi.Errors) > 0 {
		return &multi
	}
	return nil
}

func (r *Registry[T]) sortLocked() {
	r.mu.Lock()
	sort.SliceStable(r.hooks, func(i, j int) bool {
		return r.hooks[i].Priority < r.hooks[j].Priority
	})
	r.dirty.Store(false)
	r.mu.Unlock()
}

// ─────────────────────────── Well-known payload types ────────────────────────

// EventPayload carries file-access event data to hooks.
type EventPayload struct {
	Fd      int
	Pid     int32
	Mask    uint64
	Path    string
	MountID int
}

// HashPayload is passed to pre/post hash hooks.
type HashPayload struct {
	Path     string
	Key      string
	FileSize int64
	Hash     []byte
	Duration interface{} // time.Duration; interface avoids import cycle
	Cached   bool
	Deduped  bool
}

// ChunkPayload is emitted per IO chunk in parallel hashing.
type ChunkPayload struct {
	Key      string
	ChunkIdx int
	Offset   int64
	Size     int64
}

// ErrorPayload wraps processing errors for error hooks.
type ErrorPayload struct {
	Op  string
	Key string
	Err error
}

// CachePayload is used for cache-hit/miss hooks.
type CachePayload struct {
	Key  string
	Hit  bool
	Hash []byte
}

// LayerPayload is emitted when an overlayfs layer is resolved.
type LayerPayload struct {
	MergedPath string
	BackingFd  int
	LayerRoot  string
	MountID    int
	IsUpper    bool
}

// ─────────────────────────── HookSet ─────────────────────────────────────────

// HookSet groups all well-known registries used by the engine.
type HookSet struct {
	OnEvent        *Registry[EventPayload]
	PreHash        *Registry[HashPayload]
	PostHash       *Registry[HashPayload]
	OnChunk        *Registry[ChunkPayload]
	OnError        *Registry[ErrorPayload]
	OnCacheHit     *Registry[CachePayload]
	OnCacheMiss    *Registry[CachePayload]
	OnDedup        *Registry[HashPayload]
	OnLayerResolve *Registry[LayerPayload]
	OnFilter       *Registry[EventPayload] // non-nil error skips the event
}

// NewHookSet returns a HookSet with all registries initialised.
func NewHookSet() *HookSet {
	return &HookSet{
		OnEvent:        NewRegistry[EventPayload](),
		PreHash:        NewRegistry[HashPayload](),
		PostHash:       NewRegistry[HashPayload](),
		OnChunk:        NewRegistry[ChunkPayload](),
		OnError:        NewRegistry[ErrorPayload](),
		OnCacheHit:     NewRegistry[CachePayload](),
		OnCacheMiss:    NewRegistry[CachePayload](),
		OnDedup:        NewRegistry[HashPayload](),
		OnLayerResolve: NewRegistry[LayerPayload](),
		OnFilter:       NewRegistry[EventPayload](),
	}
}

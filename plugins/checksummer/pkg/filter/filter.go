//go:build linux

// Package filter provides composable, thread-safe event filters that plug
// directly into the engine's OnFilter hook.
//
// Filters implement the Filter interface; they are composed with And/Or/Not
// combinators to form arbitrary boolean trees.  A filter returning an error
// tells the engine to skip that event entirely (no hash computation).
//
// Usage:
//
//	f := filter.And(
//	    filter.NotPaths("/proc", "/sys", "/dev"),
//	    filter.Extensions(".so", ".py", ".rb"),
//	    filter.MaxSize(512 << 20),
//	    filter.NotPID(os.Getpid()),
//	)
//	eng.Hooks().OnFilter.Register(hooks.NewHook("filter", hooks.PriorityFirst, f.Hook()))
package filter

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/hooks"
)

// ─────────────────────────── sentinel errors ─────────────────────────────────

// ErrSkip is returned by a filter to signal the engine should skip this event.
var ErrSkip = errors.New("filter: skip")

// ─────────────────────────── Filter interface ─────────────────────────────────

// Filter decides whether an event should be processed.
// Returning ErrSkip (or any non-nil error) drops the event.
// Implementations must be safe for concurrent use.
type Filter interface {
	// Evaluate returns nil to allow the event, or ErrSkip to drop it.
	Evaluate(ctx context.Context, e hooks.EventPayload) error
}

// FilterFunc adapts a bare function to the Filter interface.
type FilterFunc func(ctx context.Context, e hooks.EventPayload) error

func (f FilterFunc) Evaluate(ctx context.Context, e hooks.EventPayload) error {
	return f(ctx, e)
}

// Hook returns a hooks.Handler that delegates to this Filter.
// Register with: hooks.NewHook("name", priority, filter.Hook())
func (f FilterFunc) Hook() hooks.Handler[hooks.EventPayload] {
	return func(ctx context.Context, e hooks.EventPayload) error {
		return f(ctx, e)
	}
}

// ─────────────────────────── Hook adapter ────────────────────────────────────

// Hook converts any Filter to a hooks.Handler[EventPayload] for direct
// registration into the OnFilter registry.
func Hook(f Filter) hooks.Handler[hooks.EventPayload] {
	return func(ctx context.Context, e hooks.EventPayload) error {
		return f.Evaluate(ctx, e)
	}
}

// ─────────────────────────── Combinators ─────────────────────────────────────

// And returns a filter that passes only when ALL sub-filters pass.
// The first failure short-circuits evaluation.
func And(filters ...Filter) Filter {
	return FilterFunc(func(ctx context.Context, e hooks.EventPayload) error {
		for _, f := range filters {
			if err := f.Evaluate(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// Or returns a filter that passes when AT LEAST ONE sub-filter passes.
// Evaluation stops at the first pass.
func Or(filters ...Filter) Filter {
	return FilterFunc(func(ctx context.Context, e hooks.EventPayload) error {
		var lastErr error
		for _, f := range filters {
			if err := f.Evaluate(ctx, e); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		if lastErr != nil {
			return lastErr
		}
		return ErrSkip
	})
}

// Not inverts a filter: passes when f would skip, skips when f would pass.
func Not(f Filter) Filter {
	return FilterFunc(func(ctx context.Context, e hooks.EventPayload) error {
		if err := f.Evaluate(ctx, e); err != nil {
			return nil // was going to skip → now allow
		}
		return ErrSkip // was going to allow → now skip
	})
}

// ─────────────────────────── Path filters ────────────────────────────────────

// NotPaths skips events whose resolved path has any of the given prefixes.
// Useful for excluding /proc, /sys, /dev, /run, etc.
func NotPaths(prefixes ...string) Filter {
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		for _, p := range prefixes {
			if strings.HasPrefix(e.Path, p) {
				return ErrSkip
			}
		}
		return nil
	})
}

// OnlyPaths passes events whose path matches one of the given prefixes.
func OnlyPaths(prefixes ...string) Filter {
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		for _, p := range prefixes {
			if strings.HasPrefix(e.Path, p) {
				return nil
			}
		}
		return ErrSkip
	})
}

// Extensions passes only events whose file extension (lowercased) is in the set.
// Pass extensions with dot: ".so", ".py", ".rb"
func Extensions(exts ...string) Filter {
	set := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		set[strings.ToLower(e)] = struct{}{}
	}
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		ext := strings.ToLower(filepath.Ext(e.Path))
		if _, ok := set[ext]; ok {
			return nil
		}
		return ErrSkip
	})
}

// NotExtensions skips events with any of the given file extensions.
func NotExtensions(exts ...string) Filter {
	return Not(Extensions(exts...))
}

// GlobMatch passes only events whose base filename matches one of the given
// glob patterns (filepath.Match semantics).
//
// Example: GlobMatch("*.so", "*.so.*", "lib*.a")
func GlobMatch(patterns ...string) Filter {
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		base := filepath.Base(e.Path)
		for _, pat := range patterns {
			if ok, _ := filepath.Match(pat, base); ok {
				return nil
			}
		}
		return ErrSkip
	})
}

// ─────────────────────────── Process filters ─────────────────────────────────

// NotPID skips events triggered by the given process IDs.
func NotPID(pids ...int32) Filter {
	set := make(map[int32]struct{}, len(pids))
	for _, p := range pids {
		set[p] = struct{}{}
	}
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		if _, ok := set[e.Pid]; ok {
			return ErrSkip
		}
		return nil
	})
}

// OnlyPID passes only events triggered by one of the given PIDs.
func OnlyPID(pids ...int32) Filter {
	set := make(map[int32]struct{}, len(pids))
	for _, p := range pids {
		set[p] = struct{}{}
	}
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		if _, ok := set[e.Pid]; ok {
			return nil
		}
		return ErrSkip
	})
}

// ─────────────────────────── Mask filters ────────────────────────────────────

// MaskContains passes only events whose mask has all of the given bits set.
func MaskContains(bits uint64) Filter {
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		if e.Mask&bits == bits {
			return nil
		}
		return ErrSkip
	})
}

// MaskExcludes skips events whose mask has any of the given bits set.
func MaskExcludes(bits uint64) Filter {
	return FilterFunc(func(_ context.Context, e hooks.EventPayload) error {
		if e.Mask&bits != 0 {
			return ErrSkip
		}
		return nil
	})
}

// ─────────────────────────── Rate limiter ────────────────────────────────────

// Sampler passes every Nth event and drops the rest.  N=1 passes all events.
// Thread-safe via atomic counter.
type Sampler struct {
	every int64
	count atomic.Int64
}

// NewSampler creates a sampler that passes 1 out of every n events.
func NewSampler(n int) *Sampler {
	if n < 1 {
		n = 1
	}
	return &Sampler{every: int64(n)}
}

func (s *Sampler) Evaluate(_ context.Context, _ hooks.EventPayload) error {
	n := s.count.Add(1)
	if n%s.every == 0 {
		return nil
	}
	return ErrSkip
}

// ─────────────────────────── Dedup filter ────────────────────────────────────

// SeenPaths is a filter that passes each unique path only once per lifetime.
// Uses a lock-free sharded set for minimal contention.
type SeenPaths struct {
	shards [64]seenShard
}

type seenShard struct {
	mu   atomic.Uint32 // spinlock: 0=free, 1=locked
	seen map[string]struct{}
}

// NewSeenPaths creates a SeenPaths filter.
func NewSeenPaths() *SeenPaths {
	sp := &SeenPaths{}
	for i := range sp.shards {
		sp.shards[i].seen = make(map[string]struct{})
	}
	return sp
}

func (sp *SeenPaths) Evaluate(_ context.Context, e hooks.EventPayload) error {
	if e.Path == "" {
		return nil
	}
	h := fnv32(e.Path) & 63
	s := &sp.shards[h]

	// Spin-lock acquire.
	for !s.mu.CompareAndSwap(0, 1) {
		// busy-wait; contention is rare and brief
	}
	_, exists := s.seen[e.Path]
	if !exists {
		s.seen[e.Path] = struct{}{}
	}
	s.mu.Store(0)

	if exists {
		return ErrSkip
	}
	return nil
}

// Reset clears all seen paths.
func (sp *SeenPaths) Reset() {
	for i := range sp.shards {
		s := &sp.shards[i]
		for !s.mu.CompareAndSwap(0, 1) {
		}
		s.seen = make(map[string]struct{})
		s.mu.Store(0)
	}
}

func fnv32(s string) uint32 {
	const (
		offset = uint32(2166136261)
		prime  = uint32(16777619)
	)
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}

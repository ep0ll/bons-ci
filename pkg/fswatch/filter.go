package fanwatch

import (
	"context"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Filter interface
// ─────────────────────────────────────────────────────────────────────────────

// Filter decides whether an [EnrichedEvent] should continue through the
// pipeline. Returning false drops the event silently.
//
// All implementations must be safe for concurrent use from multiple goroutines
// and must not mutate the event.
type Filter interface {
	// Allow returns true when the event should be passed downstream.
	Allow(ctx context.Context, e *EnrichedEvent) bool
}

// FilterFunc is a function that implements [Filter].
type FilterFunc func(ctx context.Context, e *EnrichedEvent) bool

// Allow implements [Filter].
func (f FilterFunc) Allow(ctx context.Context, e *EnrichedEvent) bool { return f(ctx, e) }

// ─────────────────────────────────────────────────────────────────────────────
// Composite filters — chain, any-of, negate
// ─────────────────────────────────────────────────────────────────────────────

// AllFilters is a composite [Filter] that passes an event only when ALL
// constituent filters allow it (logical AND, short-circuits on first rejection).
type AllFilters []Filter

// Allow implements [Filter].
func (a AllFilters) Allow(ctx context.Context, e *EnrichedEvent) bool {
	for _, f := range a {
		if !f.Allow(ctx, e) {
			return false
		}
	}
	return true
}

// AnyFilter is a composite [Filter] that passes an event when ANY constituent
// filter allows it (logical OR, short-circuits on first acceptance).
type AnyFilter []Filter

// Allow implements [Filter].
func (a AnyFilter) Allow(ctx context.Context, e *EnrichedEvent) bool {
	for _, f := range a {
		if f.Allow(ctx, e) {
			return true
		}
	}
	return false
}

// Not negates a [Filter]. Events allowed by inner are rejected, and vice versa.
type Not struct{ Inner Filter }

// Allow implements [Filter].
func (n Not) Allow(ctx context.Context, e *EnrichedEvent) bool {
	return !n.Inner.Allow(ctx, e)
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in filters
// ─────────────────────────────────────────────────────────────────────────────

// ReadOnlyFilter passes only non-mutating events: ACCESS, OPEN, OPEN_EXEC,
// CLOSE_NOWRITE. All write/create/delete/rename events are dropped.
//
// This is the canonical filter for snapshot observation where callers care
// only about what is being read, not what is being written.
func ReadOnlyFilter() Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		return e.Mask.IsReadOnly()
	})
}

// MaskFilter passes events whose mask includes at least one of the given ops.
// Use to select specific event types from a broader subscription.
func MaskFilter(ops ...Op) Filter {
	var combined EventMask
	for _, op := range ops {
		combined |= EventMask(op)
	}
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		return e.Mask&combined != 0
	})
}

// ExactMaskFilter passes only events whose mask exactly matches mask.
func ExactMaskFilter(mask EventMask) Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		return e.Mask == mask
	})
}

// PathPrefixFilter passes events whose path starts with any of the given prefixes.
// Prefixes are cleaned via filepath.Clean before comparison.
func PathPrefixFilter(prefixes ...string) Filter {
	clean := make([]string, len(prefixes))
	for i, p := range prefixes {
		clean[i] = filepath.Clean(p)
	}
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		for _, p := range clean {
			if strings.HasPrefix(e.Path, p+string(filepath.Separator)) || e.Path == p {
				return true
			}
		}
		return false
	})
}

// PathExcludeFilter drops events whose path starts with any of the given prefixes.
// This is the inverse of [PathPrefixFilter].
func PathExcludeFilter(prefixes ...string) Filter {
	return Not{Inner: PathPrefixFilter(prefixes...)}
}

// ExtensionFilter passes events for files whose name ends with any of the
// given extensions (e.g. ".go", ".json"). Extensions must include the dot.
func ExtensionFilter(exts ...string) Filter {
	lower := make([]string, len(exts))
	for i, e := range exts {
		lower[i] = strings.ToLower(e)
	}
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		name := strings.ToLower(e.Name)
		for _, ext := range lower {
			if strings.HasSuffix(name, ext) {
				return true
			}
		}
		return false
	})
}

// PIDFilter passes only events triggered by the given process IDs.
func PIDFilter(pids ...int32) Filter {
	set := make(map[int32]struct{}, len(pids))
	for _, p := range pids {
		set[p] = struct{}{}
	}
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		_, ok := set[e.PID]
		return ok
	})
}

// ExcludePIDFilter drops events triggered by the given process IDs.
func ExcludePIDFilter(pids ...int32) Filter {
	return Not{Inner: PIDFilter(pids...)}
}

// NoOverflowFilter drops synthetic overflow events (FAN_Q_OVERFLOW).
// Include this filter when overflow events should be handled separately via
// the watcher's error channel rather than the event pipeline.
func NoOverflowFilter() Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		return !e.Mask.Has(OpOverflow)
	})
}

// ExternalFilter adapts any external function with the signature
// func(path string) bool into a [Filter]. Events are passed when the function
// returns true. Use to integrate path-allow-lists from other packages.
func ExternalFilter(fn func(path string) bool) Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		return fn(e.Path)
	})
}

// ExternalContextFilter is like [ExternalFilter] but the external function
// also receives the context, enabling cancellation-aware external filtering.
func ExternalContextFilter(fn func(ctx context.Context, path string) bool) Filter {
	return FilterFunc(func(ctx context.Context, e *EnrichedEvent) bool {
		return fn(ctx, e.Path)
	})
}

// UpperDirOnlyFilter passes only events for files that originate in the
// overlay upperdir (i.e. files written by the container, not read from image layers).
// Requires that the event has been enriched by [transform.OverlayEnricher].
func UpperDirOnlyFilter() Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		if e.SourceLayer == nil {
			return false
		}
		return e.SourceLayer.IsUpper
	})
}

// LowerDirOnlyFilter passes only events for files that originate in read-only
// image layers. Requires enrichment by [transform.OverlayEnricher].
func LowerDirOnlyFilter() Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		if e.SourceLayer == nil {
			return false
		}
		return !e.SourceLayer.IsUpper
	})
}

// AttrFilter passes events whose Attrs map contains a specific key (any value).
func AttrFilter(key string) Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		_, ok := e.Attrs[key]
		return ok
	})
}

// AttrValueFilter passes events where e.Attrs[key] == value.
func AttrValueFilter(key string, value any) Filter {
	return FilterFunc(func(_ context.Context, e *EnrichedEvent) bool {
		return e.Attrs[key] == value
	})
}

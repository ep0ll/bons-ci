package fanwatch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Transformer interface
// ─────────────────────────────────────────────────────────────────────────────

// Transformer enriches an [EnrichedEvent] in place. Multiple transformers are
// chained in the pipeline; each receives the event already modified by its
// predecessors.
//
// Returning a non-nil error forwards the error to the pipeline's error channel
// but does NOT drop the event — the event continues through the chain in a
// partially-enriched state. Use a [Filter] downstream if partially enriched
// events must be discarded.
//
// All implementations must be safe for concurrent use from multiple goroutines.
type Transformer interface {
	// Transform modifies e in place. Returns an error on partial failure.
	Transform(ctx context.Context, e *EnrichedEvent) error
}

// TransformerFunc is a function that implements [Transformer].
type TransformerFunc func(ctx context.Context, e *EnrichedEvent) error

// Transform implements [Transformer].
func (f TransformerFunc) Transform(ctx context.Context, e *EnrichedEvent) error {
	return f(ctx, e)
}

// ─────────────────────────────────────────────────────────────────────────────
// ChainTransformer — sequential composition
// ─────────────────────────────────────────────────────────────────────────────

// ChainTransformer applies transformers in sequence.
// An error from any transformer is recorded but processing continues so
// subsequent transformers still run. All errors are joined and returned together.
type ChainTransformer []Transformer

// Transform implements [Transformer].
func (c ChainTransformer) Transform(ctx context.Context, e *EnrichedEvent) error {
	var errs []error
	for _, t := range c {
		if err := t.Transform(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	// FIX Bug 8: use errors.Join (preserves error chain for errors.Is/As).
	return errors.Join(errs...)
}

// ─────────────────────────────────────────────────────────────────────────────
// ConditionalTransformer — apply only when predicate passes
// ─────────────────────────────────────────────────────────────────────────────

// ConditionalTransformer wraps a [Transformer] behind a predicate.
// The inner transformer runs only when Predicate returns true for the event.
type ConditionalTransformer struct {
	// Predicate decides whether Inner should run. Must not mutate the event.
	Predicate func(*EnrichedEvent) bool
	// Inner is the transformer to apply when Predicate returns true.
	Inner Transformer
}

// Transform implements [Transformer].
func (c ConditionalTransformer) Transform(ctx context.Context, e *EnrichedEvent) error {
	if c.Predicate(e) {
		return c.Inner.Transform(ctx, e)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// OverlayEnricher — adds overlay layer information to events
// ─────────────────────────────────────────────────────────────────────────────

// OverlayEnricher is a [Transformer] that populates [EnrichedEvent.Overlay]
// and [EnrichedEvent.SourceLayer] by resolving the event path against the
// overlay layer stack.
//
// FIX Bug 6: e.Overlay is set ONLY when the event path is inside the overlay's
// merged directory. Previously it was set unconditionally, causing events for
// paths outside the overlay (e.g. /proc, /sys) to appear as if they belonged
// to the overlay — a false positive.
//
// Construct with [NewOverlayEnricher]. Thread-safe; the overlay info is
// read-only after construction.
type OverlayEnricher struct {
	overlay *OverlayInfo
}

// NewOverlayEnricher returns an [OverlayEnricher] for the given overlay mount.
func NewOverlayEnricher(overlay *OverlayInfo) Transformer {
	return &OverlayEnricher{overlay: overlay}
}

// Transform implements [Transformer].
//
// Sets e.Overlay and resolves e.SourceLayer only when e.Path is inside the
// overlay's merged directory. Events for unrelated paths are left unchanged.
func (o *OverlayEnricher) Transform(_ context.Context, e *EnrichedEvent) error {
	relPath, err := o.overlay.RelPath(e.Path)
	if err != nil {
		// Path is not inside this overlay's merged directory — skip silently.
		// This is not an error; multiple overlays may be watched in parallel.
		return nil
	}

	// Only attach overlay metadata once we have confirmed path membership.
	e.Overlay = o.overlay
	e.SourceLayer = o.overlay.ResolveLayer(relPath)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ProcessEnricher — adds process metadata to events
// ─────────────────────────────────────────────────────────────────────────────

// ProcessEnricher is a [Transformer] that populates [EnrichedEvent.Process]
// by reading /proc/{pid}/ entries.
//
// FIX Bug 4: ProcessEnricher previously set e.Process to a non-nil struct even
// when the process had already exited and no useful fields could be populated
// (false positive). Now e.Process is set to nil when the process cannot be
// identified, so callers can reliably use `e.Process != nil` as a guard.
func ProcessEnricher() Transformer {
	return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
		info := readProcessInfo(e.PID)
		// info is nil when the process exited before any /proc fields could be read.
		e.Process = info
		return nil
	})
}

// readProcessInfo reads process metadata from /proc/{pid}/.
// Returns nil when the process cannot be identified (exited before any field
// could be read successfully). A partial struct (e.g. only Comm populated) is
// returned when at least one field was readable.
func readProcessInfo(pid int32) *ProcessInfo {
	info := &ProcessInfo{PID: pid}
	enriched := false

	// /proc/{pid}/comm — short name (max 15 chars + newline).
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		info.Comm = strings.TrimRight(string(data), "\n")
		enriched = true
	}

	// /proc/{pid}/exe — symlink to executable.
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		info.Exe = exe
		enriched = true
	}

	// /proc/{pid}/cmdline — NUL-separated arguments.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		parts := bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0})
		info.Cmdline = make([]string, 0, len(parts))
		for _, p := range parts {
			if len(p) > 0 {
				info.Cmdline = append(info.Cmdline, string(p))
			}
		}
		enriched = true
	}

	// /proc/{pid}/cgroup — extract container ID.
	if id := containerIDFromCgroup(pid); id != "" {
		info.ContainerID = id
		enriched = true
	}

	if !enriched {
		// Process exited before we could read any field.
		return nil
	}
	return info
}

// containerIDFromCgroup extracts a Docker/containerd container ID from
// /proc/{pid}/cgroup. Returns empty string when not detectable.
func containerIDFromCgroup(pid int32) string {
	f, err := os.Open(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: hierarchy-id:controller-list:cgroup-path
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		// Look for a 64-character hex segment (Docker/containerd container ID).
		for _, seg := range strings.Split(parts[2], "/") {
			if isContainerID(seg) {
				return seg
			}
		}
	}
	return ""
}

// isContainerID heuristically identifies a 64-character lowercase hex string
// as used by Docker and containerd for container IDs.
func isContainerID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// PathNormaliser — clean and validate event paths
// ─────────────────────────────────────────────────────────────────────────────

// PathNormaliser is a [Transformer] that cleans and validates event paths.
// It sets e.Dir and e.Name (filepath.Dir/Base) and returns [ErrPathEscapes]
// for paths that escape the watched root via ".." components.
func PathNormaliser(watchRoot string) Transformer {
	root := filepath.Clean(watchRoot)
	return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
		clean := filepath.Clean(e.Path)
		e.Path = clean
		e.Dir = filepath.Dir(clean)
		e.Name = filepath.Base(clean)

		rel, err := filepath.Rel(root, clean)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("%w: %q (root=%q)", ErrPathEscapes, clean, root)
		}
		return nil
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Attribute transformers
// ─────────────────────────────────────────────────────────────────────────────

// StaticAttrTransformer attaches fixed key-value attributes to every event.
// Useful for labelling events with deployment metadata (environment, region, etc.).
func StaticAttrTransformer(attrs map[string]any) Transformer {
	// Copy the map at construction time so later mutations by the caller
	// do not affect events already in-flight.
	frozen := make(map[string]any, len(attrs))
	for k, v := range attrs {
		frozen[k] = v
	}
	return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
		for k, v := range frozen {
			e.SetAttr(k, v)
		}
		return nil
	})
}

// DynamicAttrTransformer calls fn for each event and merges the returned map
// into e.Attrs. fn must be safe for concurrent use.
func DynamicAttrTransformer(fn func(*EnrichedEvent) map[string]any) Transformer {
	return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
		for k, v := range fn(e) {
			e.SetAttr(k, v)
		}
		return nil
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// FileStatTransformer — populate FileInfo for each event
// ─────────────────────────────────────────────────────────────────────────────

// FileStatTransformer is a [Transformer] that populates e.FileInfo via os.Lstat.
// The stat is best-effort; deleted files leave FileInfo nil without error.
func FileStatTransformer() Transformer {
	return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
		info, err := os.Lstat(e.Path)
		if err != nil {
			// File may have been deleted between event and stat — not an error.
			return nil
		}
		e.FileInfo = info
		return nil
	})
}

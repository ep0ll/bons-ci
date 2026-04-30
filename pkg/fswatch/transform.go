package fanwatch

import (
	"bufio"
	"bytes"
	"context"
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
// partially-enriched state. Use a [Filter] downstream to drop partially enriched
// events if needed.
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
// An error from any transformer is recorded but processing continues
// so subsequent transformers still run.
type ChainTransformer []Transformer

// Transform implements [Transformer].
func (c ChainTransformer) Transform(ctx context.Context, e *EnrichedEvent) error {
	var errs []error
	for _, t := range c {
		if err := t.Transform(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return joinTransformErrors(errs)
}

func joinTransformErrors(errs []error) error {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return fmt.Errorf("transform: %s", strings.Join(msgs, "; "))
}

// ─────────────────────────────────────────────────────────────────────────────
// ConditionalTransformer — apply only when predicate passes
// ─────────────────────────────────────────────────────────────────────────────

// ConditionalTransformer wraps a [Transformer] behind a predicate.
// The inner transformer runs only when the predicate returns true for the event.
type ConditionalTransformer struct {
	// Predicate decides whether Inner should run.
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
// known layer stack.
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
// Sets e.Overlay and resolves e.SourceLayer from the layer stack.
func (o *OverlayEnricher) Transform(_ context.Context, e *EnrichedEvent) error {
	e.Overlay = o.overlay

	relPath, err := o.overlay.RelPath(e.Path)
	if err != nil {
		// Path not under merged dir — this event is not for our overlay.
		return nil
	}

	e.SourceLayer = o.overlay.ResolveLayer(relPath)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ProcessEnricher — adds process metadata to events
// ─────────────────────────────────────────────────────────────────────────────

// ProcessEnricher is a [Transformer] that populates [EnrichedEvent.Process]
// by reading /proc/{pid}/ entries.
//
// All reads are best-effort; the process may exit between event delivery and
// the /proc read. Fields that cannot be populated are left empty.
func ProcessEnricher() Transformer {
	return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
		info := readProcessInfo(e.PID)
		e.Process = info
		return nil
	})
}

// readProcessInfo reads process metadata from /proc/{pid}/.
func readProcessInfo(pid int32) *ProcessInfo {
	info := &ProcessInfo{PID: pid}

	// /proc/{pid}/comm — short name (max 15 chars + newline)
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		info.Comm = strings.TrimRight(string(data), "\n")
	}

	// /proc/{pid}/exe — symlink to executable
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		info.Exe = exe
	}

	// /proc/{pid}/cmdline — NUL-separated arguments
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		parts := bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0})
		info.Cmdline = make([]string, 0, len(parts))
		for _, p := range parts {
			if len(p) > 0 {
				info.Cmdline = append(info.Cmdline, string(p))
			}
		}
	}

	// /proc/{pid}/cgroup — extract container ID
	info.ContainerID = containerIDFromCgroup(pid)

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
		// Container IDs appear as /docker/{64-hex-char-id} or similar.
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		cgroupPath := parts[2]
		// Look for a 64-character hex segment (Docker container ID).
		segments := strings.Split(cgroupPath, "/")
		for _, seg := range segments {
			if isContainerID(seg) {
				return seg
			}
		}
	}
	return ""
}

// isContainerID heuristically identifies a 64-character lowercase hex string.
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
// PathTransformer — normalise and validate event paths
// ─────────────────────────────────────────────────────────────────────────────

// PathNormaliser is a [Transformer] that cleans and validates event paths.
// It sets e.Dir and e.Name (filepath.Dir/Base) and rejects paths that escape
// the watched root by returning [ErrPathEscapes].
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
// AttrTransformer — attach custom attributes
// ─────────────────────────────────────────────────────────────────────────────

// StaticAttrTransformer attaches fixed key-value attributes to every event.
// Useful for labelling events with deployment metadata (environment, region, etc.).
func StaticAttrTransformer(attrs map[string]any) Transformer {
	return TransformerFunc(func(_ context.Context, e *EnrichedEvent) error {
		for k, v := range attrs {
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
// The stat is best-effort; deleted files return nil FileInfo without error.
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

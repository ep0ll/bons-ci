package layermerkle

import (
	"context"
	"strings"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// FanwatchAdapter — bridge between fanwatch EnrichedEvent and AccessEvent
// ─────────────────────────────────────────────────────────────────────────────

// EnrichedEventSource is the subset of fanwatch.EnrichedEvent fields needed by
// this adapter. Defined as an interface so the adapter compiles without a
// direct import of the fanwatch package (avoiding circular dependencies when
// packages are in the same module).
type EnrichedEventSource interface {
	// GetMask returns the fanotify event mask.
	GetMask() uint64

	// GetPID returns the triggering process ID.
	GetPID() int32

	// GetPath returns the absolute file path.
	GetPath() string

	// GetTimestamp returns when the event was observed.
	GetTimestamp() time.Time

	// GetAttr returns a value from the event's extension map.
	GetAttr(key string) any
}

// FanwatchAdapterFunc is a function that converts an arbitrary event source
// to an AccessEvent. Register it with EventSourceAdapter.
type FanwatchAdapterFunc func(ctx context.Context, src EnrichedEventSource) (*AccessEvent, error)

// ─────────────────────────────────────────────────────────────────────────────
// AccessEventFromEnriched — canonical conversion from fanwatch attrs
// ─────────────────────────────────────────────────────────────────────────────

// AccessEventFromEnriched converts an EnrichedEventSource to an AccessEvent by
// reading the layermerkle-specific attributes from GetAttr.
//
// The fanwatch pipeline must include a StaticAttrTransformer or
// DynamicAttrTransformer that populates:
//
//	AttrVertexID   = "layermerkle.vertex.id"   (digest string)
//	AttrLayerStack = "layermerkle.layer.stack" (colon-separated digest list)
//	AttrRelPath    = "layermerkle.rel.path"    (forward-slash relative path)
//
// Returns (nil, ErrInvalidLayerStack) when attrs are absent or malformed.
func AccessEventFromEnriched(src EnrichedEventSource) (*AccessEvent, error) {
	vertexStr, _ := src.GetAttr(AttrVertexID).(string)
	stackStr, _ := src.GetAttr(AttrLayerStack).(string)
	relPath, _ := src.GetAttr(AttrRelPath).(string)

	if vertexStr == "" || stackStr == "" || relPath == "" {
		return nil, ErrInvalidLayerStack
	}

	vertexID := digest.Digest(vertexStr)
	stack, err := parseLayerStack(stackStr)
	if err != nil {
		return nil, err
	}

	return &AccessEvent{
		VertexID:   vertexID,
		LayerStack: stack,
		RelPath:    normalizeRelPath(relPath),
		AbsPath:    src.GetPath(),
		Mask:       src.GetMask(),
		PID:        src.GetPID(),
		Timestamp:  src.GetTimestamp(),
	}, nil
}

// parseLayerStack decodes a colon-separated digest list into a LayerStack.
func parseLayerStack(raw string) (LayerStack, error) {
	parts := strings.Split(raw, ":")
	stack := make(LayerStack, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		d := digest.Digest(p)
		if err := d.Validate(); err != nil {
			return nil, ErrInvalidLayerStack
		}
		stack = append(stack, d)
	}
	if len(stack) == 0 {
		return nil, ErrInvalidLayerStack
	}
	return stack, nil
}

// EncodeLayerStack encodes a LayerStack into the colon-separated attr format.
func EncodeLayerStack(stack LayerStack) string {
	parts := make([]string, len(stack))
	for i, id := range stack {
		parts[i] = string(id)
	}
	return strings.Join(parts, ":")
}

// ─────────────────────────────────────────────────────────────────────────────
// AccessEventAttrs — helper for building the correct fanwatch attrs map
// ─────────────────────────────────────────────────────────────────────────────

// AccessEventAttrs returns the map[string]any suitable for passing to fanwatch's
// StaticAttrTransformer or DynamicAttrTransformer so that
// AccessEventFromEnriched can decode it correctly.
func AccessEventAttrs(vertexID VertexID, stack LayerStack, relPath string) map[string]any {
	return map[string]any{
		AttrVertexID:   string(vertexID),
		AttrLayerStack: EncodeLayerStack(stack),
		AttrRelPath:    relPath,
	}
}

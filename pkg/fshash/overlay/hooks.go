package overlay

import (
	"context"
)

// InterpreterHooks provides callbacks for events during overlay interpretation.
type InterpreterHooks struct {
	OnWhiteoutDetected func(ctx context.Context, entry OverlayEntry)
	OnOpaqueDetected   func(ctx context.Context, entry OverlayEntry)
	OnCopyUpDetected   func(ctx context.Context, entry OverlayEntry)
	OnMutationEmitted  func(ctx context.Context, mutation Mutation)
}

func (h InterpreterHooks) fireWhiteout(ctx context.Context, entry OverlayEntry) {
	if h.OnWhiteoutDetected != nil {
		h.OnWhiteoutDetected(ctx, entry)
	}
}

func (h InterpreterHooks) fireOpaque(ctx context.Context, entry OverlayEntry) {
	if h.OnOpaqueDetected != nil {
		h.OnOpaqueDetected(ctx, entry)
	}
}

func (h InterpreterHooks) fireCopyUp(ctx context.Context, entry OverlayEntry) {
	if h.OnCopyUpDetected != nil {
		h.OnCopyUpDetected(ctx, entry)
	}
}

func (h InterpreterHooks) fireMutation(ctx context.Context, mutation Mutation) {
	if h.OnMutationEmitted != nil {
		h.OnMutationEmitted(ctx, mutation)
	}
}

package overlay

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

func TestInterpreter_Interpret(t *testing.T) {
	ctx := context.Background()

	var emitted []Mutation
	hooks := InterpreterHooks{
		OnMutationEmitted: func(ctx context.Context, mutation Mutation) {
			emitted = append(emitted, mutation)
		},
	}

	interpreter := NewInterpreter(WithInterpreterHooks(hooks))
	layerID := core.NewLayerID("test-layer")

	tests := []struct {
		name     string
		path     string
		wantKind MutationKind
	}{
		{
			name:     "regular file mutation",
			path:     "/etc/hosts",
			wantKind: MutationModified,
		},
		{
			name:     "whiteout marker mutation",
			path:     "/etc/.wh.hosts",
			wantKind: MutationDeleted,
		},
		{
			name:     "opaque directory mutation",
			path:     "/etc/.wh..wh..opq",
			wantKind: MutationOpaqued,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emitted = nil // reset
			interpreter.Interpret(ctx, core.AccessEvent{
				LayerID: layerID,
				Path:    tt.path,
				Op:      core.OpClose, // Op normally from fanotify
			})

			if len(emitted) != 1 {
				t.Fatalf("expected 1 mutation, got %d", len(emitted))
			}

			mut := emitted[0]
			if mut.Kind != tt.wantKind {
				t.Errorf("got kind %v, want %v", mut.Kind, tt.wantKind)
			}
			if !mut.LayerID.Equal(layerID) {
				t.Errorf("wrong layerID")
			}
		})
	}
}

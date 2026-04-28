package overlay

import (
	"context"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
	"github.com/bons/bons-ci/pkg/fshash/layer"
)

// We mock the layer layer.Store to test VisibilityChecker
type mockStore struct {
	layer.Store
	deleted map[string]map[string]bool
	opaque  map[string]map[string]bool
}

func (m *mockStore) IsDeleted(id core.LayerID, path string) bool {
	if m.deleted == nil {
		return false
	}
	if fileMap, ok := m.deleted[id.String()]; ok {
		return fileMap[path]
	}
	return false
}

func (m *mockStore) IsOpaque(id core.LayerID, path string) bool {
	if m.opaque == nil {
		return false
	}
	if opMap, ok := m.opaque[id.String()]; ok {
		return opMap[path]
	}
	return false
}

func TestVisibilityChecker_IsVisible(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{
		deleted: make(map[string]map[string]bool),
		opaque:  make(map[string]map[string]bool),
	}
	checker := NewVisibilityChecker(store)

	l1 := core.NewLayerID("l1")
	l2 := core.NewLayerID("l2")
	l3 := core.NewLayerID("l3")

	chainBuilder := layer.NewChainBuilder()
	chainBuilder.Push(l1)
	chainBuilder.Push(l2)
	chainBuilder.Push(l3)
	chain := chainBuilder.Build()

	_ = ctx 

	tests := []struct {
		name    string
		setup   func()
		path    string
		layerID core.LayerID
		want    bool
	}{
		{
			name:    "file is visible when no overlays",
			setup:   func() {},
			path:    "/foo/bar",
			layerID: l3,
			want:    true,
		},
		{
			name: "file is invisible when deleted in same layer",
			setup: func() {
				store.deleted[l3.String()] = map[string]bool{"/foo/bar": true}
			},
			path:    "/foo/bar",
			layerID: l3,
			want:    false,
		},
		{
			name: "file is invisible when deleted in upper layer",
			setup: func() {
				store.deleted[l2.String()] = map[string]bool{"/foo/bar": true}
			},
			path:    "/foo/bar",
			layerID: l3, // Checking from top layer: L3 > L2 > L1
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store.deleted = make(map[string]map[string]bool)
			store.opaque = make(map[string]map[string]bool)
			tt.setup()
			got := checker.IsVisible(chain, tt.layerID, tt.path)
			if got != tt.want {
				t.Errorf("IsVisible() = %v, want %v", got, tt.want)
			}
		})
	}
}

package overlay

import (
	"testing"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

func TestParser_Parse(t *testing.T) {
	parser := NewParser()
	layerID := core.NewLayerID("test-layer")

	tests := []struct {
		name       string
		path       string
		wantKind   EntryKind
		wantTarget string
	}{
		{
			name:       "regular file",
			path:       "/etc/hosts",
			wantKind:   EntryRegular,
			wantTarget: "/etc/hosts",
		},
		{
			name:       "basic whiteout",
			path:       "/etc/.wh.hosts",
			wantKind:   EntryWhiteout,
			wantTarget: "/etc/hosts",
		},
		{
			name:       "opaque directory",
			path:       "/var/log/.wh..wh..opq",
			wantKind:   EntryOpaque,
			wantTarget: "/var/log",
		},
		{
			name:       "whiteout in root",
			path:       "/.wh.secret",
			wantKind:   EntryWhiteout,
			wantTarget: "/secret",
		},
		{
			name:       "file looking like whiteout prefix but not whiteout",
			path:       "/etc/hosts.wh.backup",
			wantKind:   EntryRegular,
			wantTarget: "/etc/hosts.wh.backup",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := parser.Parse(tt.path, layerID)

			if entry.Kind != tt.wantKind {
				t.Errorf("got kind %v, want %v", entry.Kind, tt.wantKind)
			}
			if entry.TargetPath != tt.wantTarget {
				t.Errorf("got target %v, want %v", entry.TargetPath, tt.wantTarget)
			}
			if !entry.LayerID.Equal(layerID) {
				t.Errorf("got layerID %v, want %v", entry.LayerID, layerID)
			}
		})
	}
}

func TestParser_Modifiers(t *testing.T) {
	parser := NewParser()

	if !parser.IsWhiteout("/foo/.wh.bar") {
		t.Error("expected /foo/.wh.bar to be whiteout")
	}

	if parser.IsWhiteout("/foo/.wh..wh..opq") {
		t.Error("IsWhiteout should return false for opaque markers")
	}

	if !parser.IsOpaque("/foo/.wh..wh..opq") {
		t.Error("expected /foo/.wh..wh..opq to be opaque")
	}

	if target := parser.WhiteoutTarget("/foo/.wh.bar"); target != "/foo/bar" {
		t.Errorf("got target %v, want /foo/bar", target)
	}
}

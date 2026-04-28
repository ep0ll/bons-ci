package overlay

import (
	"path/filepath"
	"strings"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// defaultWhiteoutPrefix is the standard OverlayFS whiteout prefix.
const defaultWhiteoutPrefix = ".wh."

// defaultOpaqueMarker is the standard OverlayFS opaque directory marker.
const defaultOpaqueMarker = ".wh..wh..opq"

// Parser interprets raw filesystem paths and classifies overlay metadata.
// It is a stateless, pure function component.
type Parser struct {
	whiteoutPrefix string
	opaqueMarker   string
}

// NewParser creates a new Parser with the provided options.
func NewParser(opts ...Option) *Parser {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	return &Parser{
		whiteoutPrefix: cfg.whiteoutPrefix,
		opaqueMarker:   cfg.opaqueMarker,
	}
}

// Parse classifies a path into an OverlayEntry.
func (p *Parser) Parse(path string, layerID core.LayerID) OverlayEntry {
	base := filepath.Base(path)
	dir := filepath.Dir(path)

	if base == p.opaqueMarker {
		return OverlayEntry{
			RawPath:    path,
			TargetPath: dir, // The target of an opaque marker is the directory it resides in
			Kind:       EntryOpaque,
			LayerID:    layerID,
		}
	}

	if strings.HasPrefix(base, p.whiteoutPrefix) {
		targetBase := strings.TrimPrefix(base, p.whiteoutPrefix)
		var targetPath string
		if dir == "." || dir == "/" {
			targetPath = filepath.Join(dir, targetBase)
		} else {
			targetPath = filepath.Join(dir, targetBase)
		}

		return OverlayEntry{
			RawPath:    path,
			TargetPath: targetPath,
			Kind:       EntryWhiteout,
			LayerID:    layerID,
		}
	}

	return OverlayEntry{
		RawPath:    path,
		TargetPath: path,
		Kind:       EntryRegular,
		LayerID:    layerID,
	}
}

// IsWhiteout reports whether the filename is a whiteout marker.
func (p *Parser) IsWhiteout(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, p.whiteoutPrefix) && base != p.opaqueMarker
}

// IsOpaque reports whether the filename is an opaque directory marker.
func (p *Parser) IsOpaque(path string) bool {
	return filepath.Base(path) == p.opaqueMarker
}

// WhiteoutTarget extracts the target path from a whiteout marker path.
// If the path is not a whiteout, it returns the original path.
func (p *Parser) WhiteoutTarget(path string) string {
	if p.IsWhiteout(path) {
		base := filepath.Base(path)
		dir := filepath.Dir(path)
		targetBase := strings.TrimPrefix(base, p.whiteoutPrefix)
		return filepath.Join(dir, targetBase)
	}
	return path
}

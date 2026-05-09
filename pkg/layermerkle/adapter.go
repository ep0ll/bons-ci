package layermerkle

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// PipelineAdapter — concrete fanwatch Handler bridge
// ─────────────────────────────────────────────────────────────────────────────

// PipelineAdapter converts fanwatch-style enriched events into AccessEvents
// and submits them to a layermerkle Engine.
//
// It implements a duck-typed Handler — any type with:
//
//	Handle(ctx context.Context, e EnrichedEventLike) error
//
// can delegate to PipelineAdapter without importing fanwatch directly.
//
// Wire-up example (with fanwatch):
//
//	adapter := layermerkle.NewPipelineAdapter(engine)
//	pipeline := fanwatch.NewPipeline(
//	    fanwatch.WithTransformer(layerAttrTransformer),
//	    fanwatch.WithHandler(fanwatch.HandlerFunc(adapter.HandleFanwatch)),
//	)
type PipelineAdapter struct {
	engine     *Engine
	onErr      func(error)
	dropOnFull bool
}

// PipelineAdapterOption configures a PipelineAdapter.
type PipelineAdapterOption func(*PipelineAdapter)

// WithAdapterErrorCallback sets a callback for non-fatal submission errors.
func WithAdapterErrorCallback(fn func(error)) PipelineAdapterOption {
	return func(a *PipelineAdapter) { a.onErr = fn }
}

// WithAdapterDropOnFull configures the adapter to silently drop events when
// the engine buffer is full instead of returning an error.
func WithAdapterDropOnFull(drop bool) PipelineAdapterOption {
	return func(a *PipelineAdapter) { a.dropOnFull = drop }
}

// NewPipelineAdapter constructs an adapter for the given engine.
func NewPipelineAdapter(engine *Engine, opts ...PipelineAdapterOption) *PipelineAdapter {
	a := &PipelineAdapter{engine: engine, onErr: func(error) {}}
	for _, o := range opts {
		o(a)
	}
	return a
}

// ConvertAndSubmit decodes layer context from an EnrichedEventSource and
// submits the resulting AccessEvent to the engine.
// Returns nil for write-class events (they are silently skipped).
func (a *PipelineAdapter) ConvertAndSubmit(ctx context.Context, src EnrichedEventSource) error {
	ev, err := AccessEventFromEnriched(src)
	if err != nil {
		// No layer attrs attached — not an error, just not our event.
		return nil
	}
	if !ev.IsReadAccess() {
		return nil
	}
	submitErr := a.engine.Submit(ctx, ev)
	if submitErr != nil && a.onErr != nil {
		a.onErr(submitErr)
		if a.dropOnFull {
			return nil
		}
	}
	return submitErr
}

// ─────────────────────────────────────────────────────────────────────────────
// DirectAccessEvent — builds AccessEvent without fanwatch dependency
// ─────────────────────────────────────────────────────────────────────────────

// DirectEventSource implements EnrichedEventSource from plain values.
// Use when integrating with event sources other than fanwatch.
type DirectEventSource struct {
	mask      uint64
	pid       int32
	path      string
	timestamp time.Time
	attrs     map[string]any
}

// NewDirectEventSource constructs a DirectEventSource.
func NewDirectEventSource(mask uint64, pid int32, path string, timestamp time.Time, attrs map[string]any) *DirectEventSource {
	return &DirectEventSource{
		mask: mask, pid: pid, path: path, timestamp: timestamp, attrs: attrs,
	}
}

// GetMask implements EnrichedEventSource.
func (d *DirectEventSource) GetMask() uint64 { return d.mask }

// GetPID implements EnrichedEventSource.
func (d *DirectEventSource) GetPID() int32 { return d.pid }

// GetPath implements EnrichedEventSource.
func (d *DirectEventSource) GetPath() string { return d.path }

// GetTimestamp implements EnrichedEventSource.
func (d *DirectEventSource) GetTimestamp() time.Time { return d.timestamp }

// GetAttr implements EnrichedEventSource.
func (d *DirectEventSource) GetAttr(key string) any { return d.attrs[key] }

// ─────────────────────────────────────────────────────────────────────────────
// MergedPathResolver — resolves relPath from an absolute merged-view path
// ─────────────────────────────────────────────────────────────────────────────

// MergedPathResolver strips the merged directory prefix from an absolute path
// to produce a forward-slash relative path suitable for AccessEvent.RelPath.
type MergedPathResolver struct {
	mergedDir string
}

// NewMergedPathResolver creates a resolver for the given merged directory.
func NewMergedPathResolver(mergedDir string) *MergedPathResolver {
	return &MergedPathResolver{mergedDir: filepath.Clean(mergedDir)}
}

// RelPath computes the relative path from absPath.
// Returns ("", ErrPathEscapes) when absPath is not under mergedDir.
func (r *MergedPathResolver) RelPath(absPath string) (string, error) {
	abs := filepath.Clean(absPath)
	rel, err := filepath.Rel(r.mergedDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%w: %q not under %q", ErrPathEscapes, absPath, r.mergedDir)
	}
	return filepath.ToSlash(rel), nil
}

// BuildAccessEvent creates an AccessEvent from an absolute merged-view path.
// Returns an error when the path escapes the merged directory.
func (r *MergedPathResolver) BuildAccessEvent(
	vertexID VertexID,
	stack LayerStack,
	absPath string,
	mask uint64,
	pid int32,
	ts time.Time,
) (*AccessEvent, error) {
	rel, err := r.RelPath(absPath)
	if err != nil {
		return nil, err
	}
	return &AccessEvent{
		VertexID:   vertexID,
		LayerStack: stack,
		RelPath:    rel,
		AbsPath:    absPath,
		Mask:       mask,
		PID:        pid,
		Timestamp:  ts,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WhiteoutDetector — layer-aware whiteout classification
// ─────────────────────────────────────────────────────────────────────────────

// WhiteoutType classifies the kind of overlay whiteout a path represents.
type WhiteoutType int

const (
	// WhiteoutNone means the path is a regular file (not a whiteout).
	WhiteoutNone WhiteoutType = iota
	// WhiteoutFile means the path is a per-file whiteout (.wh.<name>).
	WhiteoutFile
	// WhiteoutOpaque means the directory has an opaque whiteout (.wh..wh..opq).
	WhiteoutOpaque
	// WhiteoutMetadata means the path is a Docker metadata whiteout (.wh..wh.).
	WhiteoutMetadata
)

const (
	whiteoutPrefix  = ".wh."
	opaqueMarker    = ".wh..wh..opq"
	metaWhiteoutPfx = ".wh..wh."
)

// ClassifyWhiteout determines whether a filename or path component is an
// overlay whiteout marker. This is called on individual path segments, not
// full absolute paths.
func ClassifyWhiteout(name string) WhiteoutType {
	switch {
	case name == opaqueMarker:
		return WhiteoutOpaque
	case strings.HasPrefix(name, metaWhiteoutPfx):
		return WhiteoutMetadata
	case strings.HasPrefix(name, whiteoutPrefix):
		return WhiteoutFile
	default:
		return WhiteoutNone
	}
}

// OriginalName returns the file name that the whiteout masks.
// Returns ("", false) for non-file whiteouts.
func OriginalName(whiteoutName string) (string, bool) {
	if !strings.HasPrefix(whiteoutName, whiteoutPrefix) {
		return "", false
	}
	orig := strings.TrimPrefix(whiteoutName, whiteoutPrefix)
	if orig == "" || strings.HasPrefix(orig, ".wh.") {
		return "", false
	}
	return orig, true
}

// IsWhiteoutPath checks whether any component of relPath is a whiteout marker.
// This covers the full-path opaque whiteout case — e.g. if "usr/lib/.wh..wh..opq"
// exists, then "usr/lib/anything" is whited out.
func IsWhiteoutPath(relPath string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(relPath), "/") {
		if ClassifyWhiteout(segment) != WhiteoutNone {
			return true
		}
	}
	return false
}

// OpaqueWhiteoutPaths returns the set of paths that should be checked for
// opaque whiteouts when resolving relPath. The deepest directory is checked
// first (most specific wins).
func OpaqueWhiteoutPaths(relPath string) []string {
	parts := strings.Split(filepath.ToSlash(filepath.Dir(relPath)), "/")
	var paths []string
	for i := len(parts); i > 0; i-- {
		dir := strings.Join(parts[:i], "/")
		paths = append(paths, dir+"/"+opaqueMarker)
	}
	paths = append(paths, opaqueMarker) // root opaque
	return paths
}

// ─────────────────────────────────────────────────────────────────────────────
// RealFileInfo — wraps os.FileInfo for use as a FileInfo value
// ─────────────────────────────────────────────────────────────────────────────

// FileMetadata carries file metadata extracted alongside hashing.
type FileMetadata struct {
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
	IsLink  bool
}

// MetadataFromInfo extracts FileMetadata from an fs.FileInfo.
func MetadataFromInfo(info fs.FileInfo) FileMetadata {
	return FileMetadata{
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
		IsLink:  info.Mode()&fs.ModeSymlink != 0,
	}
}

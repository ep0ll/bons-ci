package export

import (
	"context"

	digest "github.com/opencontainers/go-digest"
)

// CacheExportMode controls how aggressively cache chains are exported.
type CacheExportMode int

const (
	// CacheExportModeMin exports only the minimal cache chain needed to
	// reproduce the top-level result.
	CacheExportModeMin CacheExportMode = iota
	// CacheExportModeMax exports the complete cache chain including all
	// intermediate results.
	CacheExportModeMax
	// CacheExportModeRemoteOnly uses only pre-existing remote results;
	// does not create new exports.
	CacheExportModeRemoteOnly
)

// CacheExportOpt configures a cache export operation.
type CacheExportOpt struct {
	// Mode controls export aggressiveness.
	Mode CacheExportMode
	// ExportRoots, if true, forces export of root cache records even
	// when no dependency links exist.
	ExportRoots bool
	// IgnoreBacklinks, if true, skips backlink traversal during export.
	IgnoreBacklinks bool
	// ResolveRemotes is called to convert a local result into remote descriptors.
	ResolveRemotes func(ctx context.Context, res any) ([]Remote, error)
}

// Remote is a reference to a result stored in a remote cache (registry, S3, etc).
type Remote struct {
	Descriptors []Descriptor
}

// Descriptor describes a single blob in a remote cache.
type Descriptor struct {
	MediaType string
	Digest    digest.Digest
	Size      int64
}

// CacheExportResult pairs a remote with metadata for export.
type CacheExportResult struct {
	CreatedAt  any
	Result     *Remote
	EdgeVertex digest.Digest
	EdgeIndex  int
}

// CacheLink represents a link from a source cache record through a selector.
type CacheLink struct {
	Src      CacheExporterRecord
	Selector string
}

// CacheExporterTarget receives exported cache records. Implementations
// buffer records and flush them to the remote store.
type CacheExporterTarget interface {
	// Add adds a cache record. links are per-input dependency chains.
	// Returns the created record, whether it was actually added, and any error.
	Add(dgst digest.Digest, links [][]CacheLink, results []CacheExportResult) (CacheExporterRecord, bool, error)
}

// CacheExporterRecord represents a single exported cache entry.
type CacheExporterRecord interface {
	// Digest returns the content-addressable identifier of this record.
	Digest() digest.Digest
	// AddResult associates a remote result with this record.
	AddResult(result CacheExportResult)
}

// Exporter serialises a cache chain to a CacheExporterTarget.
type Exporter struct {
	Key     any // CacheKey or equivalent
	Records []any
	Record  any
}

// ExportTo exports the cache chain rooted at this exporter's key to the target.
func (e *Exporter) ExportTo(ctx context.Context, t CacheExporterTarget, opt CacheExportOpt) ([]CacheExporterRecord, error) {
	// Placeholder — the real implementation walks the CacheKey dependency
	// graph and exports each level. For now, return empty.
	return nil, nil
}

// MergedExporter combines multiple exporters into one.
type MergedExporter struct {
	Exporters []*Exporter
}

// ExportTo delegates to each underlying exporter.
func (m *MergedExporter) ExportTo(ctx context.Context, t CacheExporterTarget, opt CacheExportOpt) ([]CacheExporterRecord, error) {
	var out []CacheExporterRecord
	for _, e := range m.Exporters {
		recs, err := e.ExportTo(ctx, t, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

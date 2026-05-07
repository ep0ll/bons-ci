package layermerkle

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// OCIProvenance — build provenance record in OCI-compatible format
// ─────────────────────────────────────────────────────────────────────────────

// OCIProvenance is a build provenance document that records the complete set
// of files accessed during a build, keyed by their content-addressable hash.
// The format mirrors OCI image manifests so it can be stored in OCI registries
// as an attestation layer.
//
// Reference: https://github.com/opencontainers/image-spec/blob/main/manifest.md
type OCIProvenance struct {
	// SchemaVersion is always 2 (OCI image manifest schema version).
	SchemaVersion int `json:"schemaVersion"`

	// MediaType identifies this as a build provenance record.
	MediaType string `json:"mediaType"`

	// CreatedAt is when this provenance record was generated.
	CreatedAt time.Time `json:"createdAt"`

	// BuildID is an optional identifier for the build that produced this record.
	BuildID string `json:"buildId,omitempty"`

	// Vertices lists one entry per ExecOp with its access fingerprint.
	Vertices []OCIProvenanceVertex `json:"vertices"`

	// TotalFiles is the total number of unique file accesses across all vertices.
	TotalFiles int `json:"totalFiles"`

	// TotalCacheHits is the number of file hashes served from cache.
	TotalCacheHits int `json:"totalCacheHits"`
}

// OCIProvenanceMediaType is the media type for layermerkle provenance records.
const OCIProvenanceMediaType = "application/vnd.layermerkle.provenance.v1+json"

// OCIProvenanceVertex is the per-ExecOp entry in an OCIProvenance record.
type OCIProvenanceVertex struct {
	// VertexDigest is the content-addressable digest of the ExecOp.
	VertexDigest string `json:"vertexDigest"`

	// MerkleRoot is the Merkle root of all file accesses for this vertex.
	MerkleRoot string `json:"merkleRoot"`

	// LayerStack lists the layer digests (bottommost first) for this ExecOp.
	LayerStack []string `json:"layerStack"`

	// FinalizedAt is when this vertex was sealed.
	FinalizedAt time.Time `json:"finalizedAt"`

	// LeafCount is the total number of accessed files.
	LeafCount int `json:"leafCount"`

	// CacheHitRate is the fraction of file hashes served from cache.
	CacheHitRate float64 `json:"cacheHitRate"`

	// Files lists every accessed file with its hash and owner layer.
	Files []OCIProvenanceFile `json:"files"`
}

// OCIProvenanceFile is one file access record within a vertex.
type OCIProvenanceFile struct {
	// RelPath is the path relative to the overlay merged directory.
	RelPath string `json:"relPath"`

	// ContentDigest is the SHA-256 digest of the file contents.
	ContentDigest string `json:"contentDigest"`

	// OwnerLayer is the layer that contains this file.
	OwnerLayer string `json:"ownerLayer"`

	// FromCache reports whether this hash was served from the dedup cache.
	FromCache bool `json:"fromCache"`
}

// ─────────────────────────────────────────────────────────────────────────────
// ExportProvenance — build an OCIProvenance from a MerkleForest
// ─────────────────────────────────────────────────────────────────────────────

// ExportProvenanceOption configures an ExportProvenance call.
type ExportProvenanceOption func(*exportProvenanceConfig)

type exportProvenanceConfig struct {
	buildID       string
	includeFiles  bool
	sortByRelPath bool
}

// WithProvenanceBuildID sets the build identifier in the provenance record.
func WithProvenanceBuildID(id string) ExportProvenanceOption {
	return func(c *exportProvenanceConfig) { c.buildID = id }
}

// WithProvenanceFiles includes per-file access details in each vertex.
// When false only vertex-level summaries are emitted.
func WithProvenanceFiles(include bool) ExportProvenanceOption {
	return func(c *exportProvenanceConfig) { c.includeFiles = include }
}

// ExportProvenance converts a MerkleForest into an OCIProvenance record.
// Vertices are sorted by VertexDigest for deterministic output.
func ExportProvenance(forest *MerkleForest, opts ...ExportProvenanceOption) *OCIProvenance {
	cfg := &exportProvenanceConfig{includeFiles: true, sortByRelPath: true}
	for _, o := range opts {
		o(cfg)
	}

	p := &OCIProvenance{
		SchemaVersion: 2,
		MediaType:     OCIProvenanceMediaType,
		CreatedAt:     time.Now().UTC(),
		BuildID:       cfg.buildID,
	}

	trees := forest.All() // already sorted by VertexID
	for _, t := range trees {
		v := treeToProvenanceVertex(t, cfg)
		p.Vertices = append(p.Vertices, v)
		p.TotalFiles += t.LeafCount
		p.TotalCacheHits += t.CacheHitCount
	}

	return p
}

// WriteProvenance serializes an OCIProvenance record as indented JSON to w.
func WriteProvenance(w io.Writer, p *OCIProvenance) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return fmt.Errorf("layermerkle: write provenance: %w", err)
	}
	return nil
}

// ReadProvenance deserializes an OCIProvenance record from r.
func ReadProvenance(r io.Reader) (*OCIProvenance, error) {
	var p OCIProvenance
	dec := json.NewDecoder(r)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("layermerkle: read provenance: %w", err)
	}
	if p.MediaType != OCIProvenanceMediaType {
		return nil, fmt.Errorf("layermerkle: unexpected media type %q", p.MediaType)
	}
	return &p, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ProvenanceDiff — compare two provenance records
// ─────────────────────────────────────────────────────────────────────────────

// ProvenanceDiffResult summarises the difference between two provenance records.
type ProvenanceDiffResult struct {
	AddedVertices   []string // vertex digests present in B but not A
	RemovedVertices []string // vertex digests present in A but not B
	ChangedVertices []string // vertex digests whose MerkleRoot changed
	NewFiles        int      // total new file accesses in B
	RemovedFiles    int      // total removed file accesses from A
}

// DiffProvenance computes the difference between two provenance records.
func DiffProvenance(a, b *OCIProvenance) *ProvenanceDiffResult {
	result := &ProvenanceDiffResult{}

	aIndex := indexProvenanceVertices(a)
	bIndex := indexProvenanceVertices(b)

	for digest, bVtx := range bIndex {
		if aVtx, ok := aIndex[digest]; ok {
			if aVtx.MerkleRoot != bVtx.MerkleRoot {
				result.ChangedVertices = append(result.ChangedVertices, digest)
				result.NewFiles += bVtx.LeafCount - aVtx.LeafCount
				if result.NewFiles < 0 {
					result.RemovedFiles += -result.NewFiles
					result.NewFiles = 0
				}
			}
		} else {
			result.AddedVertices = append(result.AddedVertices, digest)
			result.NewFiles += bVtx.LeafCount
		}
	}

	for digest, aVtx := range aIndex {
		if _, ok := bIndex[digest]; !ok {
			result.RemovedVertices = append(result.RemovedVertices, digest)
			result.RemovedFiles += aVtx.LeafCount
		}
	}

	sort.Strings(result.AddedVertices)
	sort.Strings(result.RemovedVertices)
	sort.Strings(result.ChangedVertices)

	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func treeToProvenanceVertex(t *MerkleTree, cfg *exportProvenanceConfig) OCIProvenanceVertex {
	v := OCIProvenanceVertex{
		VertexDigest: string(t.VertexID),
		MerkleRoot:   string(t.Root),
		FinalizedAt:  t.FinalizedAt,
		LeafCount:    t.LeafCount,
		CacheHitRate: t.CacheHitRate(),
	}
	for _, id := range t.LayerStack {
		v.LayerStack = append(v.LayerStack, string(id))
	}
	if cfg.includeFiles {
		leaves := t.Leaves
		if cfg.sortByRelPath {
			sorted := make([]*MerkleLeaf, len(leaves))
			copy(sorted, leaves)
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i].RelPath < sorted[j].RelPath
			})
			leaves = sorted
		}
		for _, l := range leaves {
			v.Files = append(v.Files, OCIProvenanceFile{
				RelPath:       l.RelPath,
				ContentDigest: string(l.Hash),
				OwnerLayer:    string(l.OwnerLayerID),
				FromCache:     l.FromCache,
			})
		}
	}
	return v
}

func indexProvenanceVertices(p *OCIProvenance) map[string]OCIProvenanceVertex {
	m := make(map[string]OCIProvenanceVertex, len(p.Vertices))
	for _, v := range p.Vertices {
		m[v.VertexDigest] = v
	}
	return m
}

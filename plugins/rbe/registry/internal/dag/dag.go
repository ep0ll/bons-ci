// Package dag provides a concurrent BFS traversal engine for OCI content DAGs.
//
// An OCI content DAG is rooted at either:
//   - An OCI Image Index (references multiple manifests via platform)
//   - An OCI Image Manifest (references a config blob and layer blobs)
//
// The traversal resolves each node's children concurrently using a bounded
// worker pool (golang.org/x/sync/errgroup), deduplicating by digest via a
// sync.Map visited set.
//
// Cycle detection: OCI content is content-addressed, so true cycles are
// impossible. However, an index can reference the same manifest twice
// (e.g. duplicate platforms). The visited map handles this.
//
// The traverser does NOT download layer blobs — it only checks existence
// and reads manifests/indexes to follow references.
package dag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	digest "github.com/opencontainers/go-digest"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/errgroup"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

const (
	defaultConcurrency = 16 // max goroutines for blob-existence checks
	maxDepth           = 8  // guard against malformed manifests with deep nesting
)

// Traverser implements types.DAGTraverser using concurrent BFS.
type Traverser struct {
	concurrency int
}

// New creates a Traverser with the default concurrency.
func New() *Traverser { return &Traverser{concurrency: defaultConcurrency} }

// WithConcurrency returns a copy of the traverser with a different concurrency.
func (t *Traverser) WithConcurrency(n int) *Traverser {
	return &Traverser{concurrency: n}
}

// Traverse implements types.DAGTraverser.
func (t *Traverser) Traverse(ctx context.Context, repo string, root ocispec.Descriptor, store types.ContentStore) (*types.DAGQueryResult, error) {
	var (
		visited   sync.Map // digest → *types.DAGNode
		totalN    int64
		existingN int64
	)

	rootNode, err := t.resolveNode(ctx, repo, root, store, &visited, &totalN, &existingN, 0)
	if err != nil {
		return nil, fmt.Errorf("dag traverse: %w", err)
	}

	total := int(atomic.LoadInt64(&totalN))
	existing := int(atomic.LoadInt64(&existingN))
	missing := total - existing

	// Collect AccelTypes by walking the fully-resolved tree.
	accelTypes := detectAccelTypes(rootNode)

	return &types.DAGQueryResult{
		RootDigest:    root.Digest,
		TotalNodes:    total,
		ExistingNodes: existing,
		MissingNodes:  missing,
		Root:          rootNode,
		IsComplete:    missing == 0,
		AccelTypes:    accelTypes,
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Node resolution
// ────────────────────────────────────────────────────────────────────────────

func (t *Traverser) resolveNode(
	ctx context.Context,
	repo string,
	desc ocispec.Descriptor,
	store types.ContentStore,
	visited *sync.Map,
	totalN, existingN *int64,
	depth int,
) (*types.DAGNode, error) {
	if depth > maxDepth {
		return &types.DAGNode{
			Digest:    desc.Digest,
			MediaType: desc.MediaType,
			Size:      desc.Size,
			Metadata:  map[string]string{"error": "max depth exceeded"},
		}, nil
	}

	// Deduplication
	if existing, loaded := visited.LoadOrStore(desc.Digest, (*types.DAGNode)(nil)); loaded {
		if existing != nil {
			return existing.(*types.DAGNode), nil
		}
		// Another goroutine is resolving this node — return a stub.
		return &types.DAGNode{
			Digest:    desc.Digest,
			MediaType: desc.MediaType,
			Size:      desc.Size,
		}, nil
	}

	atomic.AddInt64(totalN, 1)

	// Check blob existence
	exists, err := store.Exists(ctx, desc.Digest)
	if err != nil {
		exists = false
	}
	if exists {
		atomic.AddInt64(existingN, 1)
	}

	node := &types.DAGNode{
		Digest:    desc.Digest,
		MediaType: desc.MediaType,
		Size:      desc.Size,
		Exists:    exists,
		Depth:     depth,
	}
	visited.Store(desc.Digest, node)

	// Only follow references for manifests and indexes.
	if !isManifestMediaType(desc.MediaType) {
		return node, nil
	}
	if !exists {
		return node, nil // can't read children without the manifest blob
	}

	children, err := t.resolveChildren(ctx, repo, desc.Digest, store, visited, totalN, existingN, depth)
	if err != nil {
		node.Metadata = map[string]string{"childError": err.Error()}
		return node, nil
	}
	node.Children = children
	return node, nil
}

// resolveChildren reads the manifest/index at dgst and concurrently resolves
// all referenced descriptors.
func (t *Traverser) resolveChildren(
	ctx context.Context,
	repo string,
	dgst digest.Digest,
	store types.ContentStore,
	visited *sync.Map,
	totalN, existingN *int64,
	depth int,
) ([]*types.DAGNode, error) {
	rc, err := store.Get(ctx, dgst)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", dgst, err)
	}
	defer rc.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, fmt.Errorf("reading manifest content: %w", err)
	}

	descs, err := extractDescriptors(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("extracting descriptors from %s: %w", dgst, err)
	}
	if len(descs) == 0 {
		return nil, nil
	}

	children := make([]*types.DAGNode, len(descs))
	g, gctx := errgroup.WithContext(ctx)
	// Limit parallelism to avoid overwhelming the store.
	sem := make(chan struct{}, t.concurrency)

	for i, desc := range descs {
		i, desc := i, desc
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()
			child, err := t.resolveNode(gctx, repo, desc, store, visited, totalN, existingN, depth+1)
			if err != nil {
				return err
			}
			children[i] = child
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return children, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Descriptor extraction from manifest/index bytes
// ────────────────────────────────────────────────────────────────────────────

// extractDescriptors parses raw manifest/index bytes and returns all
// child descriptors (layers, config, manifests in an index).
func extractDescriptors(data []byte) ([]ocispec.Descriptor, error) {
	// Sniff the schemaVersion and mediaType to decide parser.
	var probe struct {
		SchemaVersion int             `json:"schemaVersion"`
		MediaType     string          `json:"mediaType"`
		ArtifactType  string          `json:"artifactType"`
		Manifests     json.RawMessage `json:"manifests"`
		Config        json.RawMessage `json:"config"`
		Layers        json.RawMessage `json:"layers"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("probe unmarshal: %w", err)
	}

	var descs []ocispec.Descriptor

	// OCI Image Index / Docker Manifest List
	if probe.Manifests != nil {
		var idx ocispec.Index
		if err := json.Unmarshal(data, &idx); err == nil && len(idx.Manifests) > 0 {
			for _, m := range idx.Manifests {
				descs = append(descs, m)
			}
			return descs, nil
		}
	}

	// OCI Image Manifest / Docker Manifest v2
	if probe.Layers != nil || probe.Config != nil {
		var mf ocispec.Manifest
		if err := json.Unmarshal(data, &mf); err == nil {
			// Config
			if mf.Config.Digest != "" {
				descs = append(descs, mf.Config)
			}
			// Layers
			descs = append(descs, mf.Layers...)
			// Subject (OCI 1.1)
			if mf.Subject != nil && mf.Subject.Digest != "" {
				descs = append(descs, *mf.Subject)
			}
			return descs, nil
		}
	}

	return nil, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func isManifestMediaType(mt string) bool {
	switch mt {
	case ocispec.MediaTypeImageManifest,
		ocispec.MediaTypeImageIndex,

		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		types.SOCIArtifactType,
		"": // unspecified — try to parse
		return true
	}
	return false
}

// detectAccelTypes walks the resolved DAG tree and returns any AccelTypes
// hinted by layer media types in the metadata.
func detectAccelTypes(root *types.DAGNode) []types.AccelType {
	seen := make(map[types.AccelType]struct{})
	var walk func(*types.DAGNode)
	walk = func(n *types.DAGNode) {
		if n == nil {
			return
		}
		switch n.MediaType {
		case types.NydusLayerMediaType, types.NydusBootstrapMediaType:
			seen[types.AccelNydus] = struct{}{}
		case types.OverlayBDLayerMediaType:
			seen[types.AccelOverlayBD] = struct{}{}
		case types.SOCIArtifactType:
			seen[types.AccelSOCI] = struct{}{}
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	out := make([]types.AccelType, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	return out
}

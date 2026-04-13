// Package dag manages DAG lifecycle, vertex tracking, dependency resolution,
// and content-addressed cache key computation for RBE build graphs.
package dag

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/google/uuid"
)

// Key scheme:
//   dag/<id>               → JSON DAG
//   dag/<dag_id>/vertex/<vertex_id> → JSON Vertex
//   build/<build_id>/dags  → space-separated list of dag IDs

const (
	keyDAG    = "dag/%s"
	keyVertex = "dag/%s/vertex/%s"
	keyBuild  = "build/%s/dags"
)

// Service manages DAGs and vertices.
type Service struct {
	meta metadata.Store
}

// New creates a DAG Service.
func New(meta metadata.Store) *Service {
	return &Service{meta: meta}
}

// ─────────────────────────────────────────────────────────────────────────────
// DAG operations
// ─────────────────────────────────────────────────────────────────────────────

// CreateDAG creates a new build DAG.
func (s *Service) CreateDAG(ctx context.Context, buildID, name string, labels map[string]string, platform *models.Platform, description, createdBy string) (*models.DAG, error) {
	dag := &models.DAG{
		ID:          uuid.New().String(),
		BuildID:     buildID,
		Name:        name,
		Status:      models.DAGStatusPending,
		Labels:      labels,
		Platform:    platform,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.putDAG(ctx, dag); err != nil {
		return nil, err
	}
	// Register under build index.
	if buildID != "" {
		s.appendToBuildIndex(ctx, buildID, dag.ID) //nolint:errcheck
	}
	return dag, nil
}

// GetDAG retrieves a DAG by ID.
func (s *Service) GetDAG(ctx context.Context, id string) (*models.DAG, error) {
	data, err := s.meta.Get(ctx, []byte(fmt.Sprintf(keyDAG, id)))
	if err != nil {
		if err == metadata.ErrKeyNotFound {
			return nil, errors.ErrDAGNotFound
		}
		return nil, err
	}
	var dag models.DAG
	return &dag, json.Unmarshal(data, &dag)
}

// ListDAGs lists DAGs, optionally filtered by build ID and/or status.
func (s *Service) ListDAGs(ctx context.Context, buildID string, status models.DAGStatus, limit int) ([]*models.DAG, error) {
	if buildID != "" {
		return s.listDAGsByBuild(ctx, buildID, status, limit)
	}
	pairs, err := s.meta.ScanPrefix(ctx, []byte("dag/"), limit)
	if err != nil {
		return nil, err
	}
	var dags []*models.DAG
	for _, p := range pairs {
		// Skip vertex keys
		if strings.Count(string(p.Key), "/") > 1 {
			continue
		}
		var dag models.DAG
		if err := json.Unmarshal(p.Value, &dag); err == nil {
			if status == "" || dag.Status == status {
				dags = append(dags, &dag)
			}
		}
	}
	return dags, nil
}

func (s *Service) listDAGsByBuild(ctx context.Context, buildID string, status models.DAGStatus, limit int) ([]*models.DAG, error) {
	raw, err := s.meta.Get(ctx, []byte(fmt.Sprintf(keyBuild, buildID)))
	if err != nil {
		return nil, nil
	}
	ids := strings.Fields(string(raw))
	var dags []*models.DAG
	for _, id := range ids {
		dag, err := s.GetDAG(ctx, id)
		if err != nil {
			continue
		}
		if status == "" || dag.Status == status {
			dags = append(dags, dag)
		}
		if limit > 0 && len(dags) >= limit {
			break
		}
	}
	return dags, nil
}

// UpdateDAGStatus updates the status of a DAG.
func (s *Service) UpdateDAGStatus(ctx context.Context, id string, status models.DAGStatus, errMsg string) (*models.DAG, error) {
	dag, err := s.GetDAG(ctx, id)
	if err != nil {
		return nil, err
	}
	dag.Status = status
	dag.Error = errMsg
	dag.UpdatedAt = time.Now()
	if status == models.DAGStatusSucceeded || status == models.DAGStatusFailed || status == models.DAGStatusCancelled {
		t := time.Now()
		dag.CompletedAt = &t
	}
	return dag, s.putDAG(ctx, dag)
}

// DeleteDAG removes a DAG and all its vertices.
func (s *Service) DeleteDAG(ctx context.Context, id string) error {
	// Remove all vertex keys.
	prefix := []byte(fmt.Sprintf("dag/%s/vertex/", id))
	pairs, _ := s.meta.ScanPrefix(ctx, prefix, 0)
	for _, p := range pairs {
		_ = s.meta.Delete(ctx, p.Key)
	}
	return s.meta.Delete(ctx, []byte(fmt.Sprintf(keyDAG, id)))
}

// ─────────────────────────────────────────────────────────────────────────────
// Vertex operations
// ─────────────────────────────────────────────────────────────────────────────

// AddVertex adds a vertex to a DAG, computing its cache key if missing.
func (s *Service) AddVertex(ctx context.Context, v *models.Vertex) (*models.Vertex, error) {
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now()
	}
	// Compute cache key if not provided.
	if v.CacheKey == "" {
		v.CacheKey = ComputeCacheKey(v)
	}
	dag, err := s.GetDAG(ctx, v.DAGID)
	if err != nil {
		return nil, errors.Wrapf(err, "dag not found: %s", v.DAGID)
	}
	// If vertex has no inputs, it is a root — add to DAG.
	if len(v.Inputs) == 0 {
		dag.RootVertexIDs = appendUniq(dag.RootVertexIDs, v.ID)
		_ = s.putDAG(ctx, dag)
	}
	return v, s.putVertex(ctx, v)
}

// GetVertex retrieves a vertex.
func (s *Service) GetVertex(ctx context.Context, dagID, vertexID string) (*models.Vertex, error) {
	data, err := s.meta.Get(ctx, []byte(fmt.Sprintf(keyVertex, dagID, vertexID)))
	if err != nil {
		if err == metadata.ErrKeyNotFound {
			return nil, errors.ErrVertexNotFound
		}
		return nil, err
	}
	var v models.Vertex
	return &v, json.Unmarshal(data, &v)
}

// ListVertices returns all vertices in a DAG, optionally filtered by status.
func (s *Service) ListVertices(ctx context.Context, dagID string, status models.VertexStatus, limit int) ([]*models.Vertex, error) {
	prefix := []byte(fmt.Sprintf("dag/%s/vertex/", dagID))
	pairs, err := s.meta.ScanPrefix(ctx, prefix, limit)
	if err != nil {
		return nil, err
	}
	var vertices []*models.Vertex
	for _, p := range pairs {
		var v models.Vertex
		if err := json.Unmarshal(p.Value, &v); err == nil {
			if status == "" || v.Status == status {
				vertices = append(vertices, &v)
			}
		}
	}
	return vertices, nil
}

// UpdateVertexStatus updates a vertex's execution state.
func (s *Service) UpdateVertexStatus(ctx context.Context, dagID, vertexID string, status models.VertexStatus, errMsg, errDetail string, outputFiles []models.FileRef, resources *models.ResourceUsage) (*models.Vertex, error) {
	v, err := s.GetVertex(ctx, dagID, vertexID)
	if err != nil {
		return nil, err
	}
	v.Status = status
	v.Error = errMsg
	v.ErrorDetails = errDetail
	if len(outputFiles) > 0 {
		v.OutputFiles = outputFiles
	}
	if resources != nil {
		v.Resources = resources
	}
	now := time.Now()
	switch status {
	case models.VertexStatusRunning:
		v.StartedAt = &now
	case models.VertexStatusSucceeded, models.VertexStatusFailed,
		models.VertexStatusCancelled, models.VertexStatusCached, models.VertexStatusSkipped:
		v.CompletedAt = &now
	}
	return v, s.putVertex(ctx, v)
}

// GetVertexDependencyTree returns the full dependency tree rooted at a vertex.
// maxDepth=0 means unlimited.
func (s *Service) GetVertexDependencyTree(ctx context.Context, dagID, vertexID string, maxDepth int) (*models.DependencyNode, error) {
	return s.buildTree(ctx, dagID, vertexID, 0, maxDepth, map[string]struct{}{})
}

func (s *Service) buildTree(ctx context.Context, dagID, vertexID string, depth, maxDepth int, visited map[string]struct{}) (*models.DependencyNode, error) {
	if _, ok := visited[vertexID]; ok {
		return nil, nil // cycle guard
	}
	visited[vertexID] = struct{}{}

	v, err := s.GetVertex(ctx, dagID, vertexID)
	if err != nil {
		return nil, err
	}
	node := &models.DependencyNode{
		Vertex:        v,
		ProvidedFiles: v.OutputFiles,
	}
	if maxDepth > 0 && depth >= maxDepth {
		return node, nil
	}
	for _, inp := range v.Inputs {
		child, err := s.buildTree(ctx, dagID, inp.VertexID, depth+1, maxDepth, visited)
		if err != nil {
			continue
		}
		if child != nil {
			node.Deps = append(node.Deps, *child)
		}
	}
	return node, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache key computation
// ─────────────────────────────────────────────────────────────────────────────

// ComputeCacheKey produces a deterministic, content-addressed cache key for a
// vertex based on its op definition, input file hashes, and dependency keys.
func ComputeCacheKey(v *models.Vertex) string {
	h := sha256.New()

	// 1. Op type + payload
	fmt.Fprintf(h, "op:%s
", v.OpType)
	if len(v.OpPayload) > 0 {
		h.Write(v.OpPayload)
		h.Write([]byte("
"))
	}

	// 2. Environment variables (sorted for determinism)
	var envKeys []string
	for k := range v.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		fmt.Fprintf(h, "env:%s=%s
", k, v.Env[k])
	}

	// 3. Platform
	if v.Platform != nil {
		fmt.Fprintf(h, "platform:%s/%s/%s
", v.Platform.OS, v.Platform.Arch, v.Platform.Variant)
		var propKeys []string
		for k := range v.Platform.Properties {
			propKeys = append(propKeys, k)
		}
		sort.Strings(propKeys)
		for _, k := range propKeys {
			fmt.Fprintf(h, "prop:%s=%s
", k, v.Platform.Properties[k])
		}
	}

	// 4. Input files (sorted by path for determinism)
	files := make([]models.FileRef, len(v.InputFiles))
	copy(files, v.InputFiles)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		fmt.Fprintf(h, "file:%s=%s
", f.Path, f.Digest)
	}

	// 5. Dependency vertex cache keys (sorted)
	var depKeys []string
	for _, inp := range v.Inputs {
		depKeys = append(depKeys, inp.VertexID)
		// Include specific files consumed from this dependency
		for _, f := range inp.Files {
			depKeys = append(depKeys, fmt.Sprintf("depfile:%s:%s=%s", inp.VertexID, f.Path, f.Digest))
		}
	}
	sort.Strings(depKeys)
	for _, k := range depKeys {
		fmt.Fprintf(h, "dep:%s
", k)
	}

	// 6. Mount cache IDs (stable identity)
	for _, m := range v.Mounts {
		if m.Type == "cache" {
			fmt.Fprintf(h, "mount:cache:%s:%s
", m.CacheID, m.Sharing)
		}
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// ComputeCacheKeyFromParts computes a cache key from explicit inputs
// (used by the gRPC ComputeCacheKey RPC).
func ComputeCacheKeyFromParts(opDigest string, inputFileHashes []string, depCacheKeys []string, platform models.Platform, selector string) string {
	h := sha256.New()
	fmt.Fprintf(h, "op:%s
", opDigest)
	sort.Strings(inputFileHashes)
	for _, fh := range inputFileHashes {
		fmt.Fprintf(h, "file:%s
", fh)
	}
	sort.Strings(depCacheKeys)
	for _, dk := range depCacheKeys {
		fmt.Fprintf(h, "dep:%s
", dk)
	}
	fmt.Fprintf(h, "platform:%s/%s/%s
", platform.OS, platform.Arch, platform.Variant)
	if selector != "" {
		fmt.Fprintf(h, "selector:%s
", selector)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// ─────────────────────────────────────────────────────────────────────────────
// internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func (s *Service) putDAG(ctx context.Context, dag *models.DAG) error {
	data, err := json.Marshal(dag)
	if err != nil {
		return err
	}
	return s.meta.Put(ctx, []byte(fmt.Sprintf(keyDAG, dag.ID)), data)
}

func (s *Service) putVertex(ctx context.Context, v *models.Vertex) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.meta.Put(ctx, []byte(fmt.Sprintf(keyVertex, v.DAGID, v.ID)), data)
}

func (s *Service) appendToBuildIndex(ctx context.Context, buildID, dagID string) error {
	key := []byte(fmt.Sprintf(keyBuild, buildID))
	return s.meta.Txn(ctx, func(txn metadata.Txn) error {
		existing, err := txn.Get(key)
		if err != nil && err != metadata.ErrKeyNotFound {
			return err
		}
		cur := strings.TrimSpace(string(existing))
		if cur != "" {
			cur += " "
		}
		cur += dagID
		return txn.Put(key, []byte(cur))
	})
}

func appendUniq(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

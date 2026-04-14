// Package memory provides a dagstore.Store implementation that holds all data
// in process memory.  It is suitable for unit tests and embedded use cases
// where durability is not required.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dagstore "github.com/bons/bons-ci/plugins/rbe/dag"
)

// ——— Store ——————————————————————————————————————————————————————————————————

// Store is an in-memory implementation of dagstore.Store.
// All operations are guarded by a single RWMutex; it is safe for concurrent use.
type Store struct {
	mu sync.RWMutex

	// dags holds DAGMeta keyed by ID.
	dags map[string]*dagstore.DAGMeta
	// dagsByHash is the hash → ID index.
	dagsByHash map[string]string

	// vertices holds VertexMeta keyed by vertex hash.
	vertices map[string]*dagstore.VertexMeta
	// dagVertices maps dagID → set of vertex hashes.
	dagVertices map[string]map[string]struct{}
	// verticesByID maps ID → vertex hash.
	verticesByID map[string]string
	// verticesByTree maps "dagID/treeHash" → vertex hash.
	verticesByTree map[string]string

	// streams holds raw bytes keyed by "vertexHash/streamType".
	streams map[string][]byte

	closed atomic.Bool
	now    func() time.Time // injectable for deterministic tests
}

// New creates an empty in-memory Store.
func New() *Store {
	return &Store{
		dags:           make(map[string]*dagstore.DAGMeta),
		dagsByHash:     make(map[string]string),
		vertices:       make(map[string]*dagstore.VertexMeta),
		dagVertices:    make(map[string]map[string]struct{}),
		verticesByID:   make(map[string]string),
		verticesByTree: make(map[string]string),
		streams:        make(map[string][]byte),
		now:            time.Now,
	}
}

// WithClock overrides the clock used for CreatedAt/UpdatedAt, enabling
// deterministic tests.
func (s *Store) WithClock(fn func() time.Time) *Store {
	s.now = fn
	return s
}

// Ping always succeeds for the in-memory backend.
func (s *Store) Ping(_ context.Context) error {
	if s.closed.Load() {
		return dagstore.ErrClosed
	}
	return nil
}

// Close marks the store as unusable.
func (s *Store) Close() error {
	s.closed.Store(true)
	return nil
}

// ——— DAGStore ————————————————————————————————————————————————————————————————

func (s *Store) PutDAG(_ context.Context, dag *dagstore.DAGMeta) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if dag == nil || dag.ID == "" {
		return &dagstore.InvalidArgumentError{Field: "dag", Reason: "non-nil with non-empty ID required"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := copyDAG(dag)
	s.dags[dag.ID] = cp
	if dag.Hash != "" {
		s.dagsByHash[dag.Hash] = dag.ID
	}
	return nil
}

func (s *Store) GetDAG(_ context.Context, dagID string) (*dagstore.DAGMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	dag, ok := s.dags[dagID]
	if !ok {
		return nil, &dagstore.NotFoundError{Kind: "dag", ID: dagID}
	}
	return copyDAG(dag), nil
}

func (s *Store) GetDAGByHash(_ context.Context, hash string) (*dagstore.DAGMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.dagsByHash[hash]
	if !ok {
		return nil, &dagstore.NotFoundError{Kind: "dag(hash)", ID: hash}
	}
	dag := s.dags[id]
	return copyDAG(dag), nil
}

func (s *Store) ListDAGs(_ context.Context, opts dagstore.ListOptions) (*dagstore.ListResult[*dagstore.DAGMeta], error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*dagstore.DAGMeta, 0, len(s.dags))
	for _, dag := range s.dags {
		if opts.Prefix != "" && !strings.HasPrefix(dag.ID, opts.Prefix) {
			continue
		}
		if !matchLabels(dag.Labels, opts.Labels) {
			continue
		}
		result = append(result, copyDAG(dag))
	}
	return &dagstore.ListResult[*dagstore.DAGMeta]{Items: result, TotalCount: int64(len(result))}, nil
}

func (s *Store) DeleteDAG(_ context.Context, dagID string, cascade bool) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dag, ok := s.dags[dagID]
	if !ok {
		return &dagstore.NotFoundError{Kind: "dag", ID: dagID}
	}

	if cascade {
		for vh := range s.dagVertices[dagID] {
			v := s.vertices[vh]
			if v != nil && v.ID != "" {
				delete(s.verticesByID, v.ID)
			}
			if v != nil && v.TreeHash != "" {
				delete(s.verticesByTree, treeKey(dagID, v.TreeHash))
			}
			delete(s.vertices, vh)
			for _, st := range dagstore.AllStreams {
				delete(s.streams, streamKey(vh, st))
			}
		}
		delete(s.dagVertices, dagID)
	}

	if dag.Hash != "" {
		delete(s.dagsByHash, dag.Hash)
	}
	delete(s.dags, dagID)
	return nil
}

// ——— VertexStore ————————————————————————————————————————————————————————————

func (s *Store) PutVertex(_ context.Context, v *dagstore.VertexMeta) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if v == nil || v.Hash == "" || v.DAGID == "" {
		return &dagstore.InvalidArgumentError{Field: "vertex", Reason: "non-nil, non-empty Hash and DAGID required"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := copyVertex(v)
	s.vertices[v.Hash] = cp

	if s.dagVertices[v.DAGID] == nil {
		s.dagVertices[v.DAGID] = make(map[string]struct{})
	}
	s.dagVertices[v.DAGID][v.Hash] = struct{}{}

	if v.ID != "" {
		s.verticesByID[v.ID] = v.Hash
	}
	if v.TreeHash != "" {
		s.verticesByTree[treeKey(v.DAGID, v.TreeHash)] = v.Hash
	}
	return nil
}

func (s *Store) GetVertex(_ context.Context, dagID, vertexHash string) (*dagstore.VertexMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	v, ok := s.vertices[vertexHash]
	if !ok {
		return nil, &dagstore.NotFoundError{Kind: "vertex", ID: vertexHash}
	}
	return copyVertex(v), nil
}

func (s *Store) GetVertexByID(_ context.Context, id string) (*dagstore.VertexMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	hash, ok := s.verticesByID[id]
	if !ok {
		return nil, &dagstore.NotFoundError{Kind: "vertex(id)", ID: id}
	}
	v := s.vertices[hash]
	return copyVertex(v), nil
}

func (s *Store) GetVertexByTreeHash(_ context.Context, dagID, treeHash string) (*dagstore.VertexMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Fast path: treeHash IS the vertex hash.
	if v, ok := s.vertices[treeHash]; ok {
		return copyVertex(v), nil
	}
	hash, ok := s.verticesByTree[treeKey(dagID, treeHash)]
	if !ok {
		return nil, &dagstore.NotFoundError{Kind: "vertex(tree)", ID: treeHash}
	}
	v := s.vertices[hash]
	return copyVertex(v), nil
}

func (s *Store) ListVertices(_ context.Context, dagID string, opts dagstore.ListOptions) (*dagstore.ListResult[*dagstore.VertexMeta], error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	set := s.dagVertices[dagID]
	result := make([]*dagstore.VertexMeta, 0, len(set))
	for vh := range set {
		v := s.vertices[vh]
		if v == nil {
			continue
		}
		if !matchLabels(v.Labels, opts.Labels) {
			continue
		}
		result = append(result, copyVertex(v))
	}
	return &dagstore.ListResult[*dagstore.VertexMeta]{Items: result, TotalCount: int64(len(result))}, nil
}

func (s *Store) DeleteVertex(_ context.Context, dagID, vertexHash string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.vertices[vertexHash]
	if !ok {
		return &dagstore.NotFoundError{Kind: "vertex", ID: vertexHash}
	}
	if v.ID != "" {
		delete(s.verticesByID, v.ID)
	}
	if v.TreeHash != "" {
		delete(s.verticesByTree, treeKey(dagID, v.TreeHash))
	}
	delete(s.vertices, vertexHash)
	if set := s.dagVertices[dagID]; set != nil {
		delete(set, vertexHash)
	}
	return nil
}

// ——— StreamStore ————————————————————————————————————————————————————————————

func (s *Store) PutStream(_ context.Context, dagID, vertexHash string, st dagstore.StreamType, r io.Reader, _ int64) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("memory put stream: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams[streamKey(vertexHash, st)] = data
	return nil
}

func (s *Store) GetStream(_ context.Context, dagID, vertexHash string, st dagstore.StreamType) (io.ReadCloser, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, ok := s.streams[streamKey(vertexHash, st)]
	if !ok {
		return nil, &dagstore.NotFoundError{Kind: "stream", ID: fmt.Sprintf("%s/%s", vertexHash, st)}
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *Store) DeleteStream(_ context.Context, dagID, vertexHash string, st dagstore.StreamType) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streams, streamKey(vertexHash, st))
	return nil
}

// ——— compound operations ————————————————————————————————————————————————————

func (s *Store) PutVertexWithStreams(ctx context.Context, v *dagstore.VertexMeta, streams map[dagstore.StreamType]dagstore.StreamPayload) error {
	for st, payload := range streams {
		if err := s.PutStream(ctx, v.DAGID, v.Hash, st, payload.Reader, payload.Size); err != nil {
			return err
		}
		switch st {
		case dagstore.StreamStdin:
			v.HasStdin = true
			v.StdinHash = payload.Hash
		case dagstore.StreamStdout:
			v.HasStdout = true
			v.StdoutHash = payload.Hash
		case dagstore.StreamStderr:
			v.HasStderr = true
			v.StderrHash = payload.Hash
		}
	}
	v.UpdatedAt = s.now()
	return s.PutVertex(ctx, v)
}

func (s *Store) GetVertexWithStreams(ctx context.Context, dagID, vertexHash string) (*dagstore.VertexStream, error) {
	v, err := s.GetVertex(ctx, dagID, vertexHash)
	if err != nil {
		return nil, err
	}
	vs := &dagstore.VertexStream{Meta: v}

	open := func(st dagstore.StreamType, has bool, ptr *io.ReadCloser) error {
		if !has {
			return nil
		}
		rc, err := s.GetStream(ctx, dagID, vertexHash, st)
		if err != nil {
			return err
		}
		*ptr = rc
		return nil
	}
	if err := open(dagstore.StreamStdin, v.HasStdin, &vs.Stdin); err != nil {
		return nil, err
	}
	if err := open(dagstore.StreamStdout, v.HasStdout, &vs.Stdout); err != nil {
		vs.Close()
		return nil, err
	}
	if err := open(dagstore.StreamStderr, v.HasStderr, &vs.Stderr); err != nil {
		vs.Close()
		return nil, err
	}
	return vs, nil
}

func (s *Store) VerifyVertex(ctx context.Context, dagID, vertexHash string, h dagstore.Hasher) error {
	v, err := s.GetVertex(ctx, dagID, vertexHash)
	if err != nil {
		return err
	}
	inputHashes := make([]string, len(v.Inputs))
	for i, inp := range v.Inputs {
		inputHashes[i] = inp.VertexHash
	}
	computed, err := dagstore.ComputeVertexHash(h, v.OperationHash, inputHashes)
	if err != nil {
		return err
	}
	if computed != v.Hash {
		return &dagstore.IntegrityError{
			Kind:     "vertex",
			ID:       vertexHash,
			Expected: v.Hash,
			Got:      computed,
		}
	}
	return nil
}

// ——— internal helpers ————————————————————————————————————————————————————————

func (s *Store) checkOpen() error {
	if s.closed.Load() {
		return dagstore.ErrClosed
	}
	return nil
}

func streamKey(vertexHash string, st dagstore.StreamType) string {
	return vertexHash + "/" + st.String()
}

func treeKey(dagID, treeHash string) string {
	return dagID + "/" + treeHash
}

func matchLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// deep-copy helpers prevent callers from mutating stored state.

func copyDAG(d *dagstore.DAGMeta) *dagstore.DAGMeta {
	if d == nil {
		return nil
	}
	cp := *d
	cp.RootHashes = cloneStrings(d.RootHashes)
	cp.LeafHashes = cloneStrings(d.LeafHashes)
	cp.Labels = cloneLabels(d.Labels)
	return &cp
}

func copyVertex(v *dagstore.VertexMeta) *dagstore.VertexMeta {
	if v == nil {
		return nil
	}
	cp := *v
	cp.Inputs = make([]dagstore.VertexInput, len(v.Inputs))
	for i, inp := range v.Inputs {
		cp.Inputs[i] = inp
		cp.Inputs[i].Files = make([]dagstore.FileRef, len(inp.Files))
		copy(cp.Inputs[i].Files, inp.Files)
	}
	cp.Labels = cloneLabels(v.Labels)
	return &cp
}

func cloneStrings(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	copy(out, ss)
	return out
}

func cloneLabels(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Verify the Store satisfies the interface at compile time.
var _ dagstore.Store = (*Store)(nil)

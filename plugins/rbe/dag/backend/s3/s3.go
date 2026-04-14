// Package s3 provides a dagstore.Store implementation backed by an
// S3-compatible object store (AWS S3, MinIO, GCS with S3 interop, etc.).
// Build with: go build -tags s3
package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	dagstore "github.com/bons/bons-ci/plugins/rbe/dag"
)

// ——— Store ——————————————————————————————————————————————————————————————————

// Store is the S3/MinIO implementation of dagstore.Store.
type Store struct {
	cfg    Config
	client *minio.Client
	keys   dagstore.KeySchema
	codec  dagstore.Codec
	pool   *dagstore.WorkerPool
	closed atomic.Bool
}

// New creates and validates a new S3 Store.
func New(cfg Config, codec dagstore.Codec) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: create minio client: %w", err)
	}

	if codec == nil {
		codec = dagstore.DefaultCodec
	}

	return &Store{
		cfg:    cfg,
		client: client,
		keys:   dagstore.NewKeySchema(cfg.KeyPrefix),
		codec:  codec,
		pool:   dagstore.NewWorkerPool(cfg.Workers),
	}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	if s.closed.Load() {
		return dagstore.ErrClosed
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	exists, err := s.client.BucketExists(ctx, s.cfg.Bucket)
	if err != nil {
		return fmt.Errorf("s3 ping: %w", err)
	}
	if !exists {
		return fmt.Errorf("s3 ping: bucket %q does not exist", s.cfg.Bucket)
	}
	return nil
}

func (s *Store) Close() error {
	s.closed.Store(true)
	return nil
}

// ——— DAGStore ————————————————————————————————————————————————————————————————

func (s *Store) PutDAG(ctx context.Context, dag *dagstore.DAGMeta) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if dag == nil || dag.ID == "" {
		return &dagstore.InvalidArgumentError{Field: "dag", Reason: "must be non-nil with non-empty ID"}
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	return s.pool.RunAll(ctx, []func() error{
		func() error { return s.putMeta(ctx, s.keys.DAGMeta(dag.ID), dag) },
		func() error {
			if dag.Hash == "" {
				return nil
			}
			return s.putRaw(ctx, s.keys.DAGHashIndex(dag.Hash), []byte(dag.ID))
		},
	})
}

func (s *Store) GetDAG(ctx context.Context, dagID string) (*dagstore.DAGMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	var dag dagstore.DAGMeta
	if err := s.getMeta(ctx, s.keys.DAGMeta(dagID), &dag); err != nil {
		return nil, wrapNotFound(err, "dag", dagID)
	}
	return &dag, nil
}

func (s *Store) GetDAGByHash(ctx context.Context, hash string) (*dagstore.DAGMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	raw, err := s.getRaw(ctx, s.keys.DAGHashIndex(hash))
	if err != nil {
		return nil, wrapNotFound(err, "dag(hash)", hash)
	}
	return s.GetDAG(ctx, strings.TrimSpace(string(raw)))
}

func (s *Store) ListDAGs(ctx context.Context, opts dagstore.ListOptions) (*dagstore.ListResult[*dagstore.DAGMeta], error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	prefix := s.keys.DAGsPrefix()
	if opts.Prefix != "" {
		prefix += opts.Prefix
	}

	objectCh := s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:     prefix,
		Recursive:  false,
		StartAfter: opts.PageToken,
		MaxKeys:    pageSize(opts.PageSize, s.cfg.ListPageSize),
	})

	var dagIDs []string
	var lastKey string
	for obj := range objectCh {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list dags: %w", obj.Err)
		}
		id := extractDAGID(obj.Key, s.keys)
		if id == "" || !strings.HasSuffix(obj.Key, "/meta") {
			continue
		}
		dagIDs = append(dagIDs, id)
		lastKey = obj.Key
	}

	if len(dagIDs) == 0 {
		return &dagstore.ListResult[*dagstore.DAGMeta]{TotalCount: -1}, nil
	}

	metas := make([]*dagstore.DAGMeta, len(dagIDs))
	tasks := make([]func() error, len(dagIDs))
	for i, id := range dagIDs {
		i, id := i, id
		tasks[i] = func() error {
			m, err := s.GetDAG(ctx, id)
			if err != nil {
				return err
			}
			metas[i] = m
			return nil
		}
	}
	if err := s.pool.RunAll(ctx, tasks); err != nil {
		return nil, err
	}

	pz := opts.PageSize
	if pz <= 0 {
		pz = s.cfg.ListPageSize
	}
	var nextToken string
	if len(dagIDs) >= pz {
		nextToken = lastKey
	}
	return &dagstore.ListResult[*dagstore.DAGMeta]{Items: metas, NextPageToken: nextToken, TotalCount: -1}, nil
}

func (s *Store) DeleteDAG(ctx context.Context, dagID string, cascade bool) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	dag, err := s.GetDAG(ctx, dagID)
	if err != nil {
		return err
	}

	if cascade {
		result, err := s.ListVertices(ctx, dagID, dagstore.ListOptions{})
		if err != nil {
			return err
		}
		tasks := make([]func() error, 0, len(result.Items)*4)
		for _, v := range result.Items {
			v := v
			tasks = append(tasks, func() error { return s.DeleteVertex(ctx, dagID, v.Hash) })
			for _, st := range dagstore.AllStreams {
				st := st
				tasks = append(tasks, func() error {
					_ = s.DeleteStream(ctx, dagID, v.Hash, st)
					return nil
				})
			}
		}
		if err := s.pool.RunAll(ctx, tasks); err != nil {
			return err
		}
	}

	delTasks := []func() error{
		func() error { return s.deleteObject(ctx, s.keys.DAGMeta(dagID)) },
	}
	if dag.Hash != "" {
		delTasks = append(delTasks, func() error {
			return s.deleteObject(ctx, s.keys.DAGHashIndex(dag.Hash))
		})
	}
	return s.pool.RunAll(ctx, delTasks)
}

// ——— VertexStore ————————————————————————————————————————————————————————————

func (s *Store) PutVertex(ctx context.Context, v *dagstore.VertexMeta) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if v == nil || v.Hash == "" || v.DAGID == "" {
		return &dagstore.InvalidArgumentError{Field: "vertex", Reason: "non-nil, non-empty Hash and DAGID required"}
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	tasks := []func() error{
		func() error { return s.putMeta(ctx, s.keys.VertexMeta(v.Hash), v) },
		func() error { return s.putRaw(ctx, s.keys.DAGVertexMembership(v.DAGID, v.Hash), nil) },
	}
	if v.ID != "" {
		tasks = append(tasks, func() error {
			return s.putRaw(ctx, s.keys.IDIndex(v.ID), dagstore.EncodeIDIndexValue(v.DAGID, v.Hash))
		})
	}
	if v.TreeHash != "" && v.TreeHash != v.Hash {
		tasks = append(tasks, func() error {
			return s.putRaw(ctx, s.keys.TreeHashIndex(v.DAGID, v.TreeHash), []byte(v.Hash))
		})
	}
	return s.pool.RunAll(ctx, tasks)
}

func (s *Store) GetVertex(ctx context.Context, _, vertexHash string) (*dagstore.VertexMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	var v dagstore.VertexMeta
	if err := s.getMeta(ctx, s.keys.VertexMeta(vertexHash), &v); err != nil {
		return nil, wrapNotFound(err, "vertex", vertexHash)
	}
	return &v, nil
}

func (s *Store) GetVertexByID(ctx context.Context, id string) (*dagstore.VertexMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	raw, err := s.getRaw(ctx, s.keys.IDIndex(id))
	if err != nil {
		return nil, wrapNotFound(err, "vertex(id)", id)
	}
	dagID, vertexHash, err := dagstore.DecodeIDIndexValue(raw)
	if err != nil {
		return nil, fmt.Errorf("s3: decode id index: %w", err)
	}
	return s.GetVertex(ctx, dagID, vertexHash)
}

func (s *Store) GetVertexByTreeHash(ctx context.Context, dagID, treeHash string) (*dagstore.VertexMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	// Fast path: treeHash IS the vertex hash.
	v, err := s.GetVertex(ctx, dagID, treeHash)
	if err == nil {
		return v, nil
	}
	raw, err2 := s.getRaw(ctx, s.keys.TreeHashIndex(dagID, treeHash))
	if err2 != nil {
		return nil, wrapNotFound(err, "vertex(tree)", treeHash)
	}
	return s.GetVertex(ctx, dagID, strings.TrimSpace(string(raw)))
}

func (s *Store) ListVertices(ctx context.Context, dagID string, opts dagstore.ListOptions) (*dagstore.ListResult[*dagstore.VertexMeta], error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	objectCh := s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:     s.keys.DAGVerticesPrefix(dagID),
		Recursive:  true,
		StartAfter: opts.PageToken,
		MaxKeys:    pageSize(opts.PageSize, s.cfg.ListPageSize),
	})

	var hashes []string
	var lastKey string
	for obj := range objectCh {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list vertices: %w", obj.Err)
		}
		if h := extractVertexHash(obj.Key); h != "" {
			hashes = append(hashes, h)
			lastKey = obj.Key
		}
	}

	if len(hashes) == 0 {
		return &dagstore.ListResult[*dagstore.VertexMeta]{TotalCount: -1}, nil
	}

	metas := make([]*dagstore.VertexMeta, len(hashes))
	tasks := make([]func() error, len(hashes))
	for i, h := range hashes {
		i, h := i, h
		tasks[i] = func() error {
			m, err := s.GetVertex(ctx, dagID, h)
			if err != nil {
				return err
			}
			metas[i] = m
			return nil
		}
	}
	if err := s.pool.RunAll(ctx, tasks); err != nil {
		return nil, err
	}

	pz := opts.PageSize
	if pz <= 0 {
		pz = s.cfg.ListPageSize
	}
	var nextToken string
	if len(hashes) >= pz {
		nextToken = lastKey
	}
	return &dagstore.ListResult[*dagstore.VertexMeta]{Items: metas, NextPageToken: nextToken, TotalCount: -1}, nil
}

func (s *Store) DeleteVertex(ctx context.Context, dagID, vertexHash string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	v, err := s.GetVertex(ctx, dagID, vertexHash)
	if err != nil {
		return err
	}
	tasks := []func() error{
		func() error { return s.deleteObject(ctx, s.keys.VertexMeta(vertexHash)) },
		func() error { return s.deleteObject(ctx, s.keys.DAGVertexMembership(dagID, vertexHash)) },
	}
	if v.ID != "" {
		tasks = append(tasks, func() error { return s.deleteObject(ctx, s.keys.IDIndex(v.ID)) })
	}
	if v.TreeHash != "" && v.TreeHash != v.Hash {
		tasks = append(tasks, func() error {
			return s.deleteObject(ctx, s.keys.TreeHashIndex(dagID, v.TreeHash))
		})
	}
	return s.pool.RunAll(ctx, tasks)
}

// ——— StreamStore ————————————————————————————————————————————————————————————

func (s *Store) PutStream(ctx context.Context, _, vertexHash string, st dagstore.StreamType, r io.Reader, size int64) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	_, err := s.client.PutObject(ctx, s.cfg.Bucket, s.keys.VertexStream(vertexHash, st), r, size,
		minio.PutObjectOptions{ContentType: "application/octet-stream", PartSize: uint64(s.cfg.PartSize)})
	return err
}

func (s *Store) GetStream(ctx context.Context, _, vertexHash string, st dagstore.StreamType) (io.ReadCloser, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	key := s.keys.VertexStream(vertexHash, st)
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, wrapNotFound(err, "stream", key)
	}
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, wrapNotFound(err, "stream", key)
	}
	return obj, nil
}

func (s *Store) DeleteStream(ctx context.Context, _, vertexHash string, st dagstore.StreamType) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	return s.deleteObject(ctx, s.keys.VertexStream(vertexHash, st))
}

// ——— Compound ops ————————————————————————————————————————————————————————————

func (s *Store) PutVertexWithStreams(ctx context.Context, v *dagstore.VertexMeta, streams map[dagstore.StreamType]dagstore.StreamPayload) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()

	type result struct {
		st   dagstore.StreamType
		hash dagstore.Hash
		size int64
	}
	var mu sync.Mutex
	var results []result

	tasks := make([]func() error, 0, len(streams))
	for st, payload := range streams {
		st, payload := st, payload
		tasks = append(tasks, func() error {
			if err := s.PutStream(ctx, v.DAGID, v.Hash, st, payload.Reader, payload.Size); err != nil {
				return err
			}
			mu.Lock()
			results = append(results, result{st: st, hash: payload.Hash, size: payload.Size})
			mu.Unlock()
			return nil
		})
	}
	if err := s.pool.RunAll(ctx, tasks); err != nil {
		return err
	}

	for _, r := range results {
		switch r.st {
		case dagstore.StreamStdin:
			v.HasStdin, v.StdinHash = true, r.hash
			if r.size >= 0 {
				v.StdinSize = r.size
			}
		case dagstore.StreamStdout:
			v.HasStdout, v.StdoutHash = true, r.hash
			if r.size >= 0 {
				v.StdoutSize = r.size
			}
		case dagstore.StreamStderr:
			v.HasStderr, v.StderrHash = true, r.hash
			if r.size >= 0 {
				v.StderrSize = r.size
			}
		}
	}
	v.UpdatedAt = time.Now()
	return s.PutVertex(ctx, v)
}

func (s *Store) GetVertexWithStreams(ctx context.Context, dagID, vertexHash string) (*dagstore.VertexStream, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	v, err := s.GetVertex(ctx, dagID, vertexHash)
	if err != nil {
		return nil, err
	}
	vs := &dagstore.VertexStream{Meta: v}

	type slot struct {
		st  dagstore.StreamType
		ptr *io.ReadCloser
		has bool
	}
	slots := []slot{
		{dagstore.StreamStdin, &vs.Stdin, v.HasStdin},
		{dagstore.StreamStdout, &vs.Stdout, v.HasStdout},
		{dagstore.StreamStderr, &vs.Stderr, v.HasStderr},
	}
	tasks := make([]func() error, 0, 3)
	for _, sl := range slots {
		if !sl.has {
			continue
		}
		sl := sl
		tasks = append(tasks, func() error {
			rc, err := s.GetStream(ctx, dagID, vertexHash, sl.st)
			if err != nil {
				return err
			}
			*sl.ptr = rc
			return nil
		})
	}
	if err := s.pool.RunAll(ctx, tasks); err != nil {
		vs.Close()
		return nil, err
	}
	return vs, nil
}

func (s *Store) VerifyVertex(ctx context.Context, dagID, vertexHash string, h dagstore.Hasher) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
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
		return &dagstore.IntegrityError{Kind: "vertex", ID: vertexHash, Expected: v.Hash, Got: computed}
	}
	return nil
}

// ——— internals ——————————————————————————————————————————————————————————————

func (s *Store) putMeta(ctx context.Context, key string, v any) error {
	data, err := s.codec.Marshal(v)
	if err != nil {
		return fmt.Errorf("s3 marshal %q: %w", key, err)
	}
	return s.putRaw(ctx, key, data)
}

func (s *Store) putRaw(ctx context.Context, key string, data []byte) error {
	if data == nil {
		data = []byte{}
	}
	opts := minio.PutObjectOptions{ContentType: s.codec.ContentType()}
	if enc := s.codec.ContentEncoding(); enc != "" {
		opts.ContentEncoding = enc
	}
	return s.withRetry(ctx, func() error {
		r := bytes.NewReader(data)
		_, err := s.client.PutObject(ctx, s.cfg.Bucket, key, r, int64(len(data)), opts)
		return err
	})
}

func (s *Store) getMeta(ctx context.Context, key string, v any) error {
	data, err := s.getRaw(ctx, key)
	if err != nil {
		return err
	}
	return s.codec.Unmarshal(data, v)
}

func (s *Store) getRaw(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := s.withRetry(ctx, func() error {
		obj, err := s.client.GetObject(ctx, s.cfg.Bucket, key, minio.GetObjectOptions{})
		if err != nil {
			return err
		}
		defer obj.Close()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(obj); err != nil {
			return err
		}
		data = buf.Bytes()
		return nil
	})
	return data, err
}

func (s *Store) deleteObject(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.cfg.Bucket, key, minio.RemoveObjectOptions{})
}

func (s *Store) withRetry(ctx context.Context, fn func() error) error {
	delay := s.cfg.RetryBaseDelay
	var err error
	for attempt := 0; attempt <= s.cfg.RetryMax; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay = minDuration(delay*2, s.cfg.RetryMaxDelay)
		}
		err = fn()
		if err == nil || !isRetriable(err) {
			return err
		}
	}
	return err
}

func (s *Store) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if s.cfg.OpTimeout <= 0 {
		return parent, func() {}
	}
	if _, ok := parent.Deadline(); ok {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.cfg.OpTimeout)
}

func (s *Store) checkOpen() error {
	if s.closed.Load() {
		return dagstore.ErrClosed
	}
	return nil
}

func isRetriable(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	switch resp.StatusCode {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func wrapNotFound(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.StatusCode == 404 {
		return &dagstore.NotFoundError{Kind: kind, ID: id}
	}
	return err
}

func extractDAGID(key string, _ dagstore.KeySchema) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		if p == "dags" && i+2 < len(parts) && parts[i+2] == "meta" {
			return parts[i+1]
		}
	}
	return ""
}

func extractVertexHash(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		if p == "vertices" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func pageSize(requested, defaultSize int) int {
	if requested > 0 {
		return requested
	}
	return defaultSize
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// expBackoff is reserved for future jitter use.
func expBackoff(base time.Duration, attempt int, cap time.Duration) time.Duration {
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	if d > cap {
		return cap
	}
	return d
}

var _ dagstore.Store = (*Store)(nil)

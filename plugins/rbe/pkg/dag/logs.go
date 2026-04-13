package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/metadata"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
	"github.com/google/uuid"
)

// Key scheme:
//   logs/stream/<stream_id>               → JSON LogStream
//   logs/vertex/<dag_id>/<vertex_id>      → space-separated stream IDs
//   logs/chunk/<stream_id>/<seq_hex>      → raw chunk bytes (in blob store)
//   logs/chunkMeta/<stream_id>/<seq_hex>  → JSON LogChunk meta

const (
	keyLogStream    = "logs/stream/%s"
	keyLogVertex    = "logs/vertex/%s/%s"
	keyLogChunkMeta = "logs/chunkMeta/%s/%016x"
)

// chunkStorageDigest computes the storage key for a log chunk blob.
func chunkStorageDigest(streamID string, seq int64) string {
	return fmt.Sprintf("log:%s:%016x", streamID, seq)
}

// LogService manages log streams and chunks for DAG vertices.
type LogService struct {
	meta  metadata.Store
	store storage.Store
	// In-memory fan-out for live tail subscribers.
	mu        sync.RWMutex
	tailChans map[string][]chan models.LogChunk // stream_id → subscribers
}

// NewLogService creates a LogService.
func NewLogService(meta metadata.Store, store storage.Store) *LogService {
	return &LogService{
		meta:      meta,
		store:     store,
		tailChans: make(map[string][]chan models.LogChunk),
	}
}

// CreateLogStream opens a new log stream for a vertex FD.
func (l *LogService) CreateLogStream(ctx context.Context, dagID, vertexID string, fdType models.FDType, fdNum int, metadata2 map[string]string) (*models.LogStream, error) {
	stream := &models.LogStream{
		ID:        uuid.New().String(),
		VertexID:  vertexID,
		DAGID:     dagID,
		FDType:    fdType,
		FDNum:     fdNum,
		Metadata:  metadata2,
		CreatedAt: time.Now(),
	}
	if err := l.putStream(ctx, stream); err != nil {
		return nil, err
	}
	// Append to vertex index.
	l.appendStreamToVertex(ctx, dagID, vertexID, stream.ID) //nolint:errcheck
	return stream, nil
}

// GetLogStream retrieves stream metadata.
func (l *LogService) GetLogStream(ctx context.Context, streamID string) (*models.LogStream, error) {
	data, err := l.meta.Get(ctx, []byte(fmt.Sprintf(keyLogStream, streamID)))
	if err != nil {
		if err == metadata.ErrKeyNotFound {
			return nil, errors.ErrNotFound
		}
		return nil, err
	}
	var stream models.LogStream
	return &stream, json.Unmarshal(data, &stream)
}

// ListLogStreams returns all streams for a vertex.
func (l *LogService) ListLogStreams(ctx context.Context, dagID, vertexID string) ([]*models.LogStream, error) {
	raw, err := l.meta.Get(ctx, []byte(fmt.Sprintf(keyLogVertex, dagID, vertexID)))
	if err != nil {
		return nil, nil
	}
	var streams []*models.LogStream
	for _, id := range strings.Fields(string(raw)) {
		stream, err := l.GetLogStream(ctx, id)
		if err == nil {
			streams = append(streams, stream)
		}
	}
	return streams, nil
}

// UploadChunk stores a log chunk both in blob store and metadata index.
func (l *LogService) UploadChunk(ctx context.Context, streamID string, seq int64, data []byte, ts time.Time, fdType models.FDType, fdNum int) error {
	stream, err := l.GetLogStream(ctx, streamID)
	if err != nil {
		return err
	}
	if stream.Closed {
		return errors.ErrStreamClosed
	}

	chunk := models.LogChunk{
		StreamID:  streamID,
		Sequence:  seq,
		Data:      data,
		Timestamp: ts,
		FDType:    fdType,
		FDNum:     fdNum,
	}

	// Store raw bytes in blob store.
	storageKey := chunkStorageDigest(streamID, seq)
	if err := l.store.Put(ctx, storageKey, strings.NewReader(string(data)), int64(len(data)), storage.PutOptions{}); err != nil {
		return errors.Wrapf(err, "store log chunk")
	}

	// Store chunk metadata.
	meta, _ := json.Marshal(struct {
		StreamID  string    `json:"stream_id"`
		Sequence  int64     `json:"sequence"`
		Size      int       `json:"size"`
		Timestamp time.Time `json:"timestamp"`
		FDType    int       `json:"fd_type"`
		FDNum     int       `json:"fd_num"`
	}{streamID, seq, len(data), ts, int(fdType), fdNum})
	metaKey := []byte(fmt.Sprintf(keyLogChunkMeta, streamID, seq))
	if err := l.meta.Put(ctx, metaKey, meta); err != nil {
		return err
	}

	// Update stream counters.
	stream.TotalBytes += int64(len(data))
	stream.ChunkCount++
	_ = l.putStream(ctx, stream)

	// Fan-out to live tail subscribers.
	l.broadcast(streamID, chunk)
	return nil
}

// GetLogs returns buffered log chunks in the range [fromSeq, toSeq).
func (l *LogService) GetLogs(ctx context.Context, streamID string, fromSeq, toSeq int64, maxChunks int) ([]models.LogChunk, bool, int64, error) {
	startKey := []byte(fmt.Sprintf(keyLogChunkMeta, streamID, fromSeq))
	var endKey []byte
	if toSeq > 0 {
		endKey = []byte(fmt.Sprintf(keyLogChunkMeta, streamID, toSeq))
	} else {
		endKey = []byte(fmt.Sprintf(keyLogChunkMeta, streamID, int64(^uint64(0)>>1)))
	}

	pairs, err := l.meta.Scan(ctx, startKey, endKey, maxChunks)
	if err != nil {
		return nil, false, 0, err
	}

	var chunks []models.LogChunk
	var nextSeq int64
	for _, p := range pairs {
		var m struct {
			Sequence  int64     `json:"sequence"`
			Size      int       `json:"size"`
			Timestamp time.Time `json:"timestamp"`
			FDType    int       `json:"fd_type"`
			FDNum     int       `json:"fd_num"`
		}
		if err := json.Unmarshal(p.Value, &m); err != nil {
			continue
		}
		// Fetch raw data from blob store.
		storageKey := chunkStorageDigest(streamID, m.Sequence)
		rc, _, err := l.store.Get(ctx, storageKey, storage.GetOptions{})
		if err != nil {
			continue
		}
		var buf strings.Builder
		buf.ReadFrom(rc)
		rc.Close()

		chunks = append(chunks, models.LogChunk{
			StreamID:  streamID,
			Sequence:  m.Sequence,
			Data:      []byte(buf.String()),
			Timestamp: m.Timestamp,
			FDType:    models.FDType(m.FDType),
			FDNum:     m.FDNum,
		})
		nextSeq = m.Sequence + 1
	}
	hasMore := maxChunks > 0 && len(pairs) == maxChunks
	return chunks, hasMore, nextSeq, nil
}

// TailLogs returns a channel that streams live log chunks.
// The channel is closed when the stream is closed or ctx is done.
func (l *LogService) TailLogs(ctx context.Context, streamID string, fromSeq int64, follow bool) (<-chan models.LogChunk, error) {
	out := make(chan models.LogChunk, 256)

	go func() {
		defer close(out)

		// First, replay buffered chunks from fromSeq.
		chunks, _, nextSeq, err := l.GetLogs(ctx, streamID, fromSeq, 0, 0)
		if err == nil {
			for _, c := range chunks {
				select {
				case <-ctx.Done():
					return
				case out <- c:
				}
			}
		}

		if !follow {
			return
		}

		// Subscribe to live fan-out.
		sub := make(chan models.LogChunk, 256)
		l.subscribe(streamID, sub)
		defer l.unsubscribe(streamID, sub)

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-sub:
				if !ok {
					return
				}
				if chunk.Sequence < nextSeq {
					continue // already sent
				}
				out <- chunk
			}
		}
	}()

	return out, nil
}

// CloseLogStream marks a stream as complete.
func (l *LogService) CloseLogStream(ctx context.Context, streamID string) (*models.LogStream, error) {
	stream, err := l.GetLogStream(ctx, streamID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	stream.Closed = true
	stream.ClosedAt = &now
	if err := l.putStream(ctx, stream); err != nil {
		return nil, err
	}
	// Close fan-out channel for this stream.
	l.closeStream(streamID)
	return stream, nil
}

// GetVertexLogs returns all log chunks for a vertex, optionally merged across FDs.
func (l *LogService) GetVertexLogs(ctx context.Context, dagID, vertexID string, fds []models.FDType, interleaved bool) ([]models.LogChunk, []*models.LogStream, error) {
	streams, err := l.ListLogStreams(ctx, dagID, vertexID)
	if err != nil {
		return nil, nil, err
	}

	fdSet := map[models.FDType]struct{}{}
	for _, fd := range fds {
		fdSet[fd] = struct{}{}
	}

	var allChunks []models.LogChunk
	for _, stream := range streams {
		if len(fdSet) > 0 {
			if _, ok := fdSet[stream.FDType]; !ok {
				continue
			}
		}
		chunks, _, _, err := l.GetLogs(ctx, stream.ID, 0, 0, 0)
		if err == nil {
			allChunks = append(allChunks, chunks...)
		}
	}

	if interleaved {
		sort.Slice(allChunks, func(i, j int) bool {
			return allChunks[i].Timestamp.Before(allChunks[j].Timestamp)
		})
	}

	return allChunks, streams, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Fan-out helpers
// ─────────────────────────────────────────────────────────────────────────────

func (l *LogService) broadcast(streamID string, chunk models.LogChunk) {
	l.mu.RLock()
	subs := l.tailChans[streamID]
	l.mu.RUnlock()
	for _, sub := range subs {
		select {
		case sub <- chunk:
		default:
			// Slow consumer — drop chunk (tail is best-effort).
		}
	}
}

func (l *LogService) subscribe(streamID string, ch chan models.LogChunk) {
	l.mu.Lock()
	l.tailChans[streamID] = append(l.tailChans[streamID], ch)
	l.mu.Unlock()
}

func (l *LogService) unsubscribe(streamID string, ch chan models.LogChunk) {
	l.mu.Lock()
	subs := l.tailChans[streamID]
	for i, s := range subs {
		if s == ch {
			l.tailChans[streamID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	l.mu.Unlock()
}

func (l *LogService) closeStream(streamID string) {
	l.mu.Lock()
	for _, ch := range l.tailChans[streamID] {
		close(ch)
	}
	delete(l.tailChans, streamID)
	l.mu.Unlock()
}

func (l *LogService) putStream(ctx context.Context, stream *models.LogStream) error {
	data, _ := json.Marshal(stream)
	return l.meta.Put(ctx, []byte(fmt.Sprintf(keyLogStream, stream.ID)), data)
}

func (l *LogService) appendStreamToVertex(ctx context.Context, dagID, vertexID, streamID string) error {
	key := []byte(fmt.Sprintf(keyLogVertex, dagID, vertexID))
	return l.meta.Txn(ctx, func(txn metadata.Txn) error {
		raw, _ := txn.Get(key)
		cur := strings.TrimSpace(string(raw))
		if !strings.Contains(cur, streamID) {
			if cur != "" {
				cur += " "
			}
			cur += streamID
		}
		return txn.Put(key, []byte(cur))
	})
}

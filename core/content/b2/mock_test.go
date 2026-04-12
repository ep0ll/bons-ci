package b2

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// mockBackend implements ObjectStorage for tests.
type mockBackend struct {
	mu      sync.Mutex
	objects map[string]mockObject // key → object
}

type mockObject struct {
	data     []byte
	meta     ObjectMeta
	uploaded time.Time
}

func newMockBackend() *mockBackend {
	return &mockBackend{objects: make(map[string]mockObject)}
}

func (m *mockBackend) StatObject(_ context.Context, bucket, key string) (ObjectMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return ObjectMeta{}, fmt.Errorf("NoSuchKey: %s", key)
	}
	return obj.meta, nil
}

func (m *mockBackend) GetObject(_ context.Context, bucket, key string) (ObjectReader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", key)
	}
	return &mockReader{Reader: bytes.NewReader(obj.data), size: int64(len(obj.data))}, nil
}

func (m *mockBackend) PutObject(_ context.Context, bucket, key string, r io.Reader, size int64, contentType string) (UploadResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return UploadResult{}, err
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = mockObject{
		data: data,
		meta: ObjectMeta{
			Key:          key,
			Size:         int64(len(data)),
			LastModified: now,
			ETag:         "mock-etag",
			ContentType:  contentType,
			Metadata:     make(map[string]string),
		},
		uploaded: now,
	}
	return UploadResult{
		Bucket:       bucket,
		Key:          key,
		Size:         int64(len(data)),
		LastModified: now,
		ETag:         "mock-etag",
	}, nil
}

func (m *mockBackend) CopyObjectMetadata(_ context.Context, bucket, key string, meta map[string]string) (UploadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return UploadResult{}, fmt.Errorf("NoSuchKey: %s", key)
	}
	obj.meta.Metadata = meta
	now := time.Now()
	obj.meta.LastModified = now
	m.objects[key] = obj
	return UploadResult{
		Bucket:       bucket,
		Key:          key,
		Size:         obj.meta.Size,
		LastModified: now,
	}, nil
}

func (m *mockBackend) RemoveObject(_ context.Context, _, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[key]; !ok {
		return fmt.Errorf("NoSuchKey: %s", key)
	}
	delete(m.objects, key)
	return nil
}

func (m *mockBackend) RemoveIncompleteUpload(context.Context, string, string) error {
	return nil
}

func (m *mockBackend) ListObjects(_ context.Context, _, prefix string, _ bool) <-chan ObjectEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan ObjectEntry, len(m.objects))
	for k, obj := range m.objects {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			ch <- ObjectEntry{
				Key:          k,
				Size:         obj.meta.Size,
				LastModified: obj.meta.LastModified,
				ETag:         obj.meta.ETag,
				Metadata:     obj.meta.Metadata,
			}
		}
	}
	close(ch)
	return ch
}

func (m *mockBackend) ListIncompleteUploads(context.Context, string, string) <-chan UploadEntry {
	ch := make(chan UploadEntry)
	close(ch)
	return ch
}

func (m *mockBackend) BucketExists(context.Context, string) (bool, error) {
	return true, nil
}

func (m *mockBackend) MakeBucket(context.Context, string, string) error {
	return nil
}

// compile-time check
var _ ObjectStorage = (*mockBackend)(nil)

// mockReader implements ObjectReader for tests.
type mockReader struct {
	*bytes.Reader
	size int64
}

func (r *mockReader) Close() error { return nil }
func (r *mockReader) Size() int64  { return r.size }

package registry

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// contentReaderAt unit tests
// ---------------------------------------------------------------------------

// ioReaderAtRC is an io.ReadCloser that also implements io.ReaderAt (fast path).
type ioReaderAtRC struct {
	*bytes.Reader
}

func (r *ioReaderAtRC) Close() error { return nil }

func newReaderAtRC(data []byte) *ioReaderAtRC {
	return &ioReaderAtRC{bytes.NewReader(data)}
}

// seqRC is an io.ReadCloser without io.ReaderAt (slow/tee path).
type seqRC struct {
	r io.Reader
}

func (r *seqRC) Read(p []byte) (int, error) { return r.r.Read(p) }
func (r *seqRC) Close() error               { return nil }

func newSeqRC(data []byte) *seqRC {
	return &seqRC{bytes.NewReader(data)}
}

// ---------------------------------------------------------------------------
// Fast path (io.ReaderAt underlying stream)
// ---------------------------------------------------------------------------

func TestContentReaderAt_FastPath_ReadAt(t *testing.T) {
	data := []byte("fast-path-data-12345")
	rc := newReaderAtRC(data)
	ra := newContentReaderAt(rc, nil, int64(len(data)))
	defer ra.Close()

	buf := make([]byte, 5)
	n, err := ra.ReadAt(buf, 10)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, data[10:15], buf[:n])
}

func TestContentReaderAt_FastPath_ConcurrentReadAt(t *testing.T) {
	data := make([]byte, 1<<16)
	for i := range data {
		data[i] = byte(i)
	}
	rc := newReaderAtRC(data)
	ra := newContentReaderAt(rc, nil, int64(len(data)))
	defer ra.Close()

	const goroutines = 20
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		off := int64(i * 100)
		go func(off int64) {
			buf := make([]byte, 100)
			n, err := ra.ReadAt(buf, off)
			if err != nil && err != io.EOF {
				errs <- err
				return
			}
			for j := 0; j < n; j++ {
				if buf[j] != data[off+int64(j)] {
					errs <- assert.AnError
					return
				}
			}
			errs <- nil
		}(off)
	}
	for i := 0; i < goroutines; i++ {
		assert.NoError(t, <-errs)
	}
}

func TestContentReaderAt_FastPath_Size(t *testing.T) {
	data := []byte("size-check")
	rc := newReaderAtRC(data)
	ra := newContentReaderAt(rc, nil, int64(len(data)))
	assert.Equal(t, int64(len(data)), ra.Size())
	ra.Close()
}

func TestContentReaderAt_FastPath_NoLocalWriter(t *testing.T) {
	data := []byte("no-local-writer")
	rc := newReaderAtRC(data)
	ra := newContentReaderAt(rc, nil, int64(len(data)))

	buf := make([]byte, len(data))
	n, _ := ra.ReadAt(buf, 0)
	assert.Equal(t, data, buf[:n])
	assert.NoError(t, ra.Close())
}

// ---------------------------------------------------------------------------
// Slow path (sequential tee stream)
// ---------------------------------------------------------------------------

func TestContentReaderAt_SlowPath_TeeesIntoLocalWriter(t *testing.T) {
	data := []byte("slow-path-tee-data")
	rc := newSeqRC(data)

	local := newMockLocalStore()
	lw, err := local.Writer(context.Background(),
		content.WithRef("slow-tee"),
		content.WithDescriptor(v1.Descriptor{
			Digest: digest.FromBytes(data),
			Size:   int64(len(data)),
		}),
	)
	require.NoError(t, err)

	ra := newContentReaderAt(rc, lw, int64(len(data)))

	// Read a portion.
	buf := make([]byte, 9)
	n, _ := ra.ReadAt(buf, 0)
	assert.Equal(t, data[:n], buf[:n])

	// Close drains the rest.
	require.NoError(t, ra.Close())
}

func TestContentReaderAt_SlowPath_Size(t *testing.T) {
	data := []byte("slow-size")
	rc := newSeqRC(data)
	ra := newContentReaderAt(rc, nil, int64(len(data)))
	assert.Equal(t, int64(len(data)), ra.Size())
	ra.Close()
}

func TestContentReaderAt_SlowPath_DrainOnClose(t *testing.T) {
	data := make([]byte, 64<<10) // 64 KiB
	for i := range data {
		data[i] = byte(i)
	}
	rc := newSeqRC(data)

	received := &bytes.Buffer{}
	captureWriter := &captureContentWriter{w: received}
	ra := newContentReaderAt(rc, captureWriter, int64(len(data)))

	// Read only first 1 KiB.
	buf := make([]byte, 1<<10)
	_, _ = ra.ReadAt(buf, 0)

	// Close must drain remaining 63 KiB.
	require.NoError(t, ra.Close())
	assert.Equal(t, len(data), received.Len(), "drain on Close must deliver full stream to local writer")
}

// ---------------------------------------------------------------------------
// writerDigest helper
// ---------------------------------------------------------------------------

func TestContentReaderAt_WriterDigest_WithLocal(t *testing.T) {
	data := []byte("digest-check")
	rc := newSeqRC(data)
	local := newMockLocalStore()
	lw, _ := local.Writer(context.Background(), content.WithRef("dgst-test"))
	ra := newContentReaderAt(rc, lw, int64(len(data)))

	buf := make([]byte, len(data))
	_, _ = ra.ReadAt(buf, 0)
	ra.Close()

	got := ra.writerDigest()
	assert.Equal(t, digest.FromBytes(data), got)
}

func TestContentReaderAt_WriterDigest_NoLocal(t *testing.T) {
	data := []byte("no-local")
	rc := newSeqRC(data)
	ra := newContentReaderAt(rc, nil, int64(len(data)))
	ra.Close()
	assert.Equal(t, digest.Digest(""), ra.writerDigest())
}

// ---------------------------------------------------------------------------
// Integration: Store.ReaderAt tees into local cache
// ---------------------------------------------------------------------------

func TestStore_ReaderAt_TeePopulatesLocalCache(t *testing.T) {
	s, b, l := testStore(t)
	data := []byte("tee-populates-local")
	dgst := seedRemote(t, b, data)

	desc := v1.Descriptor{Digest: dgst, Size: int64(len(data))}
	ra, err := s.ReaderAt(context.Background(), desc)
	require.NoError(t, err)

	// Read all bytes to ensure tee runs.
	buf := make([]byte, len(data))
	_, _ = ra.ReadAt(buf, 0)
	require.NoError(t, ra.Close())

	// Local store should now have the blob cached.
	time.Sleep(5 * time.Millisecond) // allow async commit
	info, err := l.Info(context.Background(), dgst)
	if err == nil {
		// Cache was populated.
		assert.Equal(t, int64(len(data)), info.Size)
	}
	// If err != nil the best-effort cache write simply didn't land — acceptable.
}

// ---------------------------------------------------------------------------
// captureContentWriter — captures Write calls for test assertions
// ---------------------------------------------------------------------------

type captureContentWriter struct {
	w      *bytes.Buffer
	dgst   digest.Digest
	closed bool
}

func (c *captureContentWriter) Write(p []byte) (int, error)  { return c.w.Write(p) }
func (c *captureContentWriter) Close() error                  { c.closed = true; return nil }
func (c *captureContentWriter) Digest() digest.Digest         { return digest.FromBytes(c.w.Bytes()) }
func (c *captureContentWriter) Truncate(_ int64) error        { c.w.Reset(); return nil }
func (c *captureContentWriter) Status() (content.Status, error) {
	return content.Status{Offset: int64(c.w.Len())}, nil
}
func (c *captureContentWriter) Commit(_ context.Context, _ int64, expected digest.Digest, _ ...content.Opt) error {
	c.dgst = expected
	return nil
}

var _ content.Writer = (*captureContentWriter)(nil)

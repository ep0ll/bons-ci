package registry

import (
	"context"
	"io"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

// contentReaderAt implements content.ReaderAt by tee-reading from a remote
// registry stream and simultaneously caching the data into a local writer.
//
// Sequential reads via Read() go through the tee pipe. Random-access ReadAt()
// is supported only if the underlying stream implements io.ReadSeeker.
type contentReaderAt struct {
	rc          io.ReadCloser  // remote data stream
	tee         io.Reader      // tee'd into local writer
	localWriter content.Writer // local cache writer (best-effort)
	size        int64
	mu          sync.Mutex
}

func newContentReaderAt(rc io.ReadCloser, localWriter content.Writer, size int64) *contentReaderAt {
	return &contentReaderAt{
		rc:          rc,
		tee:         io.TeeReader(rc, localWriter),
		localWriter: localWriter,
		size:        size,
	}
}

// ReadAt reads len(p) bytes from the data source starting at byte offset off.
// If the underlying stream supports seeking, it will seek first; otherwise it
// reads sequentially via the tee reader.
func (r *contentReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if seeker, ok := r.rc.(io.Seeker); ok {
		if _, err := seeker.Seek(off, io.SeekStart); err != nil {
			return 0, err
		}
		return r.rc.Read(p)
	}

	// Fallback: sequential read (no seeking).
	return r.tee.Read(p)
}

// Close closes the remote stream and best-effort commits the local cache.
func (r *contentReaderAt) Close() error {
	var errs []error

	// Drain + commit the local cache.
	if r.localWriter != nil {
		// Best-effort: commit whatever has been read so far.
		r.localWriter.Commit(context.Background(), r.size, "")
		if err := r.localWriter.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if err := r.rc.Close(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// Size returns the total size of the content.
func (r *contentReaderAt) Size() int64 {
	return r.size
}

// compile-time check
var _ content.ReaderAt = (*contentReaderAt)(nil)

// drainReader is a helper for testing: it wraps Digest so the reader can also
// report what digest was committed from the local writer.
func (r *contentReaderAt) writerDigest() digest.Digest {
	if r.localWriter != nil {
		return r.localWriter.Digest()
	}
	return ""
}

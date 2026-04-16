// Package snapshot provides periodic persistence for the AccelIndex.
//
// The snapshot manager runs a background goroutine that calls
// AccelIndex.Snapshot() on a configurable interval and writes the result
// atomically to a file on disk (write-to-tmp then os.Rename to avoid
// partial reads on crash).
//
// On startup, call Restore() to reload the index from the latest snapshot.
//
// Design:
//   - Single background writer goroutine — no write lock on the index
//   - Atomic write: snapshot → tmp file → os.Rename
//   - gzip compression to reduce I/O and disk footprint
//   - Configurable interval (default: 5 minutes)
//   - Graceful shutdown via context cancellation
//   - Optional S3/GCS upload hook (implement the SnapshotUploader interface)
package snapshot

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ────────────────────────────────────────────────────────────────────────────
// Snapshotable — implemented by ShardedIndex
// ────────────────────────────────────────────────────────────────────────────

// Snapshotable is the minimal interface required of the AccelIndex.
type Snapshotable interface {
	Snapshot() ([]byte, error)
	Restore(ctx context.Context, data []byte) error
}

// SnapshotUploader is an optional hook for uploading snapshots to remote
// storage (S3, GCS, etc.).
type SnapshotUploader interface {
	Upload(ctx context.Context, data []byte) error
	Download(ctx context.Context) ([]byte, error)
}

// ────────────────────────────────────────────────────────────────────────────
// Config
// ────────────────────────────────────────────────────────────────────────────

// Config configures the Manager.
type Config struct {
	// Dir is the directory where snapshots are written.
	Dir string

	// Filename is the base filename for the snapshot (default: "accelindex.snap.gz").
	Filename string

	// Interval is how often to snapshot (default: 5 minutes).
	Interval time.Duration

	// Uploader is an optional remote storage hook. If set, each snapshot is
	// uploaded after being written to disk.
	Uploader SnapshotUploader
}

func (c *Config) setDefaults() {
	if c.Dir == "" {
		c.Dir = "."
	}
	if c.Filename == "" {
		c.Filename = "accelindex.snap.gz"
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Manager
// ────────────────────────────────────────────────────────────────────────────

// Manager manages periodic snapshotting of a Snapshotable.
type Manager struct {
	cfg   Config
	index Snapshotable

	lastWritten time.Time
	lastSize    int64
}

// New creates a Manager. Call Start() to begin background snapshotting.
func New(index Snapshotable, cfg Config) *Manager {
	cfg.setDefaults()
	return &Manager{cfg: cfg, index: index}
}

// Start begins the background snapshot loop. It returns when ctx is cancelled.
// Call this in a separate goroutine:
//
//	go manager.Start(ctx)
func (m *Manager) Start(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final snapshot on shutdown
			_ = m.WriteSnapshot(context.Background())
			return
		case <-ticker.C:
			_ = m.WriteSnapshot(ctx)
		}
	}
}

// WriteSnapshot serialises the index and writes it atomically to disk.
// It is safe to call concurrently — the index's Snapshot() method handles
// its own locking.
func (m *Manager) WriteSnapshot(ctx context.Context) error {
	data, err := m.index.Snapshot()
	if err != nil {
		return fmt.Errorf("snapshot: serialising index: %w", err)
	}

	path := m.snapshotPath()
	if err := writeGzipAtomic(path, data); err != nil {
		return fmt.Errorf("snapshot: writing file %s: %w", path, err)
	}

	m.lastWritten = time.Now()
	m.lastSize = int64(len(data))

	// Optional remote upload
	if m.cfg.Uploader != nil {
		if err := m.cfg.Uploader.Upload(ctx, data); err != nil {
			// Non-fatal: disk snapshot succeeded
			return fmt.Errorf("snapshot: remote upload failed (disk snapshot ok): %w", err)
		}
	}
	return nil
}

// Restore loads the latest snapshot from disk (or remote if Uploader is set)
// and restores the index state. Call once at startup before serving requests.
func (m *Manager) Restore(ctx context.Context) error {
	var data []byte

	// Prefer remote snapshot if uploader supports it
	if m.cfg.Uploader != nil {
		remote, err := m.cfg.Uploader.Download(ctx)
		if err == nil && len(remote) > 0 {
			data = remote
		}
	}

	// Fall back to local disk snapshot
	if len(data) == 0 {
		path := m.snapshotPath()
		local, err := readGzip(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // No snapshot yet — fresh start
			}
			return fmt.Errorf("snapshot: reading %s: %w", path, err)
		}
		data = local
	}

	if err := m.index.Restore(ctx, data); err != nil {
		return fmt.Errorf("snapshot: restoring index: %w", err)
	}
	return nil
}

// LastWritten returns the time of the most recent successful snapshot write.
func (m *Manager) LastWritten() time.Time { return m.lastWritten }

// LastSize returns the uncompressed size of the last snapshot in bytes.
func (m *Manager) LastSize() int64 { return m.lastSize }

// ────────────────────────────────────────────────────────────────────────────
// File I/O helpers
// ────────────────────────────────────────────────────────────────────────────

// writeGzipAtomic writes data gzip-compressed to path atomically:
//  1. Write to <path>.tmp
//  2. os.Rename(<path>.tmp, <path>)  — atomic on POSIX filesystems
func writeGzipAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		// Clean up tmp if rename failed
		if _, statErr := os.Stat(tmp); statErr == nil {
			_ = os.Remove(tmp)
		}
	}()

	gz := gzip.NewWriter(f)
	if _, err := gz.Write(data); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil { // fdatasync for durability
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readGzip reads and decompresses a gzip file.
func readGzip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip header: %w", err)
	}
	defer gz.Close()

	return io.ReadAll(gz)
}

func (m *Manager) snapshotPath() string {
	return filepath.Join(m.cfg.Dir, m.cfg.Filename)
}

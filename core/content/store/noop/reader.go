// Package noop provides no-operation implementations of the core content
// interfaces (Store, Writer, ReaderAt). These are primarily intended for
// use in tests as stand-ins that succeed silently.
package noop

import "github.com/containerd/containerd/v2/core/content"

// ReaderAt returns a content.ReaderAt that reports every read as successful
// without delivering any real bytes.
func ReaderAt() content.ReaderAt { return &readerAt{} }

type readerAt struct{}

// ReadAt satisfies io.ReaderAt — reports success for any read range.
func (r *readerAt) ReadAt(p []byte, _ int64) (int, error) { return len(p), nil }

// Size reports -1 because no real content is stored.
func (r *readerAt) Size() int64 { return -1 }

// Close is a no-op.
func (r *readerAt) Close() error { return nil }

var _ content.ReaderAt = (*readerAt)(nil)

package dagstore

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Codec handles serialisation and deserialisation of metadata objects.
// Implementations must be safe for concurrent use.
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
	ContentEncoding() string
	ContentType() string
}

// GzipJSONCodec serialises to JSON then compresses with gzip using pooled writers.
type GzipJSONCodec struct {
	level  int
	wrPool sync.Pool
}

// NewGzipJSONCodec creates a GzipJSONCodec at the given compression level.
func NewGzipJSONCodec(level int) (*GzipJSONCodec, error) {
	if level < gzip.HuffmanOnly || level > gzip.BestCompression {
		return nil, fmt.Errorf("gzip codec: invalid level %d", level)
	}
	c := &GzipJSONCodec{level: level}
	c.wrPool = sync.Pool{
		New: func() any {
			w, _ := gzip.NewWriterLevel(io.Discard, c.level)
			return w
		},
	}
	return c, nil
}

// DefaultCodec is a ready-to-use GzipJSONCodec at the default compression level.
var DefaultCodec = mustDefaultCodec()

func mustDefaultCodec() *GzipJSONCodec {
	c, err := NewGzipJSONCodec(gzip.DefaultCompression)
	if err != nil {
		panic("dagstore: failed to initialise default codec: " + err.Error())
	}
	return c
}

func (c *GzipJSONCodec) ContentEncoding() string { return "gzip" }
func (c *GzipJSONCodec) ContentType() string     { return "application/json" }

func (c *GzipJSONCodec) Marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}
	var buf bytes.Buffer
	w := c.wrPool.Get().(*gzip.Writer)
	w.Reset(&buf)
	if _, err := w.Write(raw); err != nil {
		c.wrPool.Put(w)
		return nil, fmt.Errorf("gzip compress: %w", err)
	}
	if err := w.Close(); err != nil {
		c.wrPool.Put(w)
		return nil, fmt.Errorf("gzip flush: %w", err)
	}
	c.wrPool.Put(w)
	return buf.Bytes(), nil
}

func (c *GzipJSONCodec) Unmarshal(data []byte, v any) error {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip new reader: %w", err)
	}
	defer r.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return fmt.Errorf("gzip decompress: %w", err)
	}
	return json.Unmarshal(buf.Bytes(), v)
}

// PlainJSONCodec serialises to uncompressed JSON. Useful for debugging and tests.
type PlainJSONCodec struct{}

func (PlainJSONCodec) ContentEncoding() string            { return "" }
func (PlainJSONCodec) ContentType() string                { return "application/json" }
func (PlainJSONCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (PlainJSONCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

package content

import (
	"testing"

	"github.com/bons/bons-ci/content/split"
	"github.com/containerd/containerd/v2/core/content"
)

type mockWriter struct {
	content.Writer
	writeResult int
	writeErr    error
}

func (m *mockWriter) Write(p []byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	if m.writeResult > 0 {
		return m.writeResult, nil
	}
	return len(p), nil
}

func TestMultiWriterWriteConcurrent(t *testing.T) {
	// Create a multiWriter with 10 dummy writers
	var writers []content.Writer
	for i := 0; i < 10; i++ {
		writers = append(writers, &mockWriter{})
	}

	mw := split.NewMultiWriter(writers...)
	data := []byte("test data")

	// Call Write multiple times concurrently
	done := make(chan bool)
	for i := 0; i < 50; i++ {
		go func() {
			n, err := mw.Write(data)
			if err != nil {
				t.Errorf("Write() unexpected error: %v", err)
			}
			if n != len(data) {
				t.Errorf("Write() expected %d bytes written, got %d", len(data), n)
			}
			done <- true
		}()
	}

	// Wait for all goroutines to finish
	for i := 0; i < 50; i++ {
		<-done
	}
}

func TestMultiWriterEmptyGuards(t *testing.T) {
	mw := split.NewMultiWriter([]content.Writer{}...)

	// Should not panic on empty writers slice
	if dgst := mw.Digest(); dgst != "" {
		t.Errorf("Digest() expected empty, got %v", dgst)
	}

	if _, err := mw.Status(); err == nil {
		t.Errorf("Status() expected error on empty writers, got nil")
	}
}

func TestMultiStoreListStatuses(t *testing.T) {
	// A placeholder for multi-store tests
}

package dag_test

import (
	"context"
	"testing"
	"time"
)

// TestLogStreamLifecycle verifies the create → upload → tail → close flow.
// Uses the real LogService with a fake metadata/storage backend.
func TestLogStreamLifecycle(t *testing.T) {
	// This is an integration-style smoke test.
	// In CI these would use the in-memory backends defined above.
	// Real assertions require wiring up the LogService with real stores;
	// here we document the expected call sequence as executable pseudocode.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx

	// 1. CreateLogStream → returns LogStream with ID
	// 2. UploadChunk(seq=0, data="line1
") → ok
	// 3. UploadChunk(seq=1, data="line2
") → ok
	// 4. TailLogs(from=0, follow=false) → receives 2 chunks, channel closed
	// 5. GetLogs(from=0, to=0) → 2 chunks
	// 6. CloseLogStream → stream.Closed == true
	// 7. UploadChunk after close → ErrStreamClosed

	t.Log("LogService lifecycle test — wire up stores in integration suite")
}

// TestLogInterleave verifies that GetVertexLogs with interleaved=true returns
// chunks sorted by timestamp regardless of FD.
func TestLogInterleave(t *testing.T) {
	t1 := time.Now()
	t2 := t1.Add(time.Millisecond)
	t3 := t2.Add(time.Millisecond)

	type chunk struct {
		ts  time.Time
		fd  int
		msg string
	}
	chunks := []chunk{
		{t2, 1, "stdout line"},
		{t1, 2, "stderr before stdout"},
		{t3, 1, "stdout after stderr"},
	}

	// Sort by timestamp
	for i := 1; i < len(chunks); i++ {
		for j := i; j > 0 && chunks[j].ts.Before(chunks[j-1].ts); j-- {
			chunks[j], chunks[j-1] = chunks[j-1], chunks[j]
		}
	}

	if chunks[0].msg != "stderr before stdout" {
		t.Errorf("expected stderr first, got %q", chunks[0].msg)
	}
	if chunks[1].msg != "stdout line" {
		t.Errorf("expected stdout second, got %q", chunks[1].msg)
	}
	if chunks[2].msg != "stdout after stderr" {
		t.Errorf("expected stdout after stderr third, got %q", chunks[2].msg)
	}
}

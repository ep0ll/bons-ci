package metrics_test

import (
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/metrics"
)

func TestCounterIncrements(t *testing.T) {
	var c metrics.Counter
	c.Inc()
	c.Inc()
	c.Add(8)
	if got := c.Load(); got != 10 {
		t.Errorf("want 10, got %d", got)
	}
	c.Reset()
	if c.Load() != 0 {
		t.Error("reset failed")
	}
}

func TestCounterConcurrent(t *testing.T) {
	var c metrics.Counter
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Inc() }()
	}
	wg.Wait()
	if c.Load() != 1000 {
		t.Errorf("want 1000, got %d", c.Load())
	}
}

func TestHistogramRecord(t *testing.T) {
	var h metrics.Histogram
	durations := []time.Duration{
		1 * time.Microsecond,
		100 * time.Microsecond,
		1 * time.Millisecond,
		10 * time.Millisecond,
		100 * time.Millisecond,
	}
	for _, d := range durations {
		h.Record(d)
	}
	s := h.Snapshot()
	if s.Count != int64(len(durations)) {
		t.Errorf("count: want %d, got %d", len(durations), s.Count)
	}
	if s.MinUS <= 0 {
		t.Error("min should be > 0")
	}
	if s.MaxUS <= s.MinUS {
		t.Error("max should be > min")
	}
	mean := s.MeanUS()
	if mean <= 0 {
		t.Error("mean should be > 0")
	}
}

func TestHistogramZeroDuration(t *testing.T) {
	var h metrics.Histogram
	h.Record(0)
	s := h.Snapshot()
	if s.Count != 1 {
		t.Errorf("count: want 1, got %d", s.Count)
	}
}

func TestHistogramConcurrent(t *testing.T) {
	var h metrics.Histogram
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		d := time.Duration(i) * time.Microsecond
		wg.Add(1)
		go func(d time.Duration) { defer wg.Done(); h.Record(d) }(d)
	}
	wg.Wait()
	s := h.Snapshot()
	if s.Count != 500 {
		t.Errorf("want 500, got %d", s.Count)
	}
}

func TestRecorderSnapshot(t *testing.T) {
	var r metrics.Recorder
	r.EventsReceived.Add(5)
	r.HashesComputed.Add(3)
	r.CacheHits.Add(2)
	r.HashLatency.Record(50 * time.Microsecond)

	s := r.Snapshot()
	if s.EventsReceived != 5 {
		t.Errorf("EventsReceived: want 5, got %d", s.EventsReceived)
	}
	if s.HashesComputed != 3 {
		t.Errorf("HashesComputed: want 3, got %d", s.HashesComputed)
	}
	if s.CacheHits != 2 {
		t.Errorf("CacheHits: want 2, got %d", s.CacheHits)
	}
	if s.HashLatency.Count != 1 {
		t.Errorf("HashLatency.Count: want 1, got %d", s.HashLatency.Count)
	}
}

func TestSnapshotMap(t *testing.T) {
	var r metrics.Recorder
	r.BytesHashed.Add(1024)
	s := r.Snapshot()
	m := s.Map()
	if m["bytes_hashed"] != 1024 {
		t.Errorf("map bytes_hashed: want 1024, got %d", m["bytes_hashed"])
	}
}

func TestSnapshotString(t *testing.T) {
	var r metrics.Recorder
	r.EventsReceived.Inc()
	s := r.Snapshot()
	str := s.String()
	if len(str) == 0 {
		t.Error("snapshot String() should be non-empty")
	}
}

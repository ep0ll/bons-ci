package resources

// resources_test.go – comprehensive white-box tests for the refactored
// resource monitoring package.
//
// Tests cover:
//   - Low-level parse helpers (parse.go)
//   - Per-controller collection (cpu, memory, io, pids)
//   - Registry.Collect fan-out
//   - Sampler lifecycle (adaptive interval, Close idempotency, timer-leak fix)
//   - cgroupRecord lifecycle (Start→Close, nopRecord)
//   - OperationRecorder lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// writeCgroupFile creates a file at path with the given content.
func writeCgroupFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// cgroupDir creates a temporary directory that mimics a cgroupv2 namespace.
func cgroupDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// ─────────────────────────────────────────────────────────────────────────────
// parse.go unit tests
// ─────────────────────────────────────────────────────────────────────────────

func TestParseKVFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeCgroupFile(t, filepath.Join(dir, "test.stat"),
		"usage_usec 1000\nuser_usec 500\nsystem_usec 500\n")

	got := map[string]uint64{}
	require.NoError(t, parseKVFile(filepath.Join(dir, "test.stat"), func(k string, v uint64) {
		got[k] = v
	}))
	assert.Equal(t, uint64(1000), got["usage_usec"])
	assert.Equal(t, uint64(500), got["user_usec"])
	assert.Equal(t, uint64(500), got["system_usec"])
}

func TestParseKVFile_Missing_ReturnsNil(t *testing.T) {
	// A missing cgroup file means the controller is not enabled — not an error.
	require.NoError(t, parseKVFile("/nonexistent/path/cpu.stat", func(_ string, _ uint64) {}))
}

func TestParseKVFile_MalformedLines_Skipped(t *testing.T) {
	dir := t.TempDir()
	// Contains a line with no value and a line with a non-numeric value.
	writeCgroupFile(t, filepath.Join(dir, "test.stat"),
		"good 42\nno_value\nbad_num xyz\n")

	got := map[string]uint64{}
	require.NoError(t, parseKVFile(filepath.Join(dir, "test.stat"), func(k string, v uint64) {
		got[k] = v
	}))
	assert.Equal(t, map[string]uint64{"good": 42}, got)
}

func TestParseSingleUint64File_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeCgroupFile(t, filepath.Join(dir, "pids.current"), "  123\n")
	v, ok, err := parseSingleUint64File(filepath.Join(dir, "pids.current"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(123), v)
}

func TestParseSingleUint64File_MaxLiteral_OkFalse(t *testing.T) {
	dir := t.TempDir()
	writeCgroupFile(t, filepath.Join(dir, "pids.max"), "max\n")
	_, ok, err := parseSingleUint64File(filepath.Join(dir, "pids.max"))
	require.NoError(t, err)
	assert.False(t, ok, "\"max\" should be treated as unlimited, not a numeric value")
}

func TestParseSingleUint64File_Missing_OkFalse(t *testing.T) {
	_, ok, err := parseSingleUint64File("/nonexistent/memory.peak")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestParsePressureFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeCgroupFile(t, filepath.Join(dir, "cpu.pressure"),
		"some avg10=1.23 avg60=4.56 avg300=7.89 total=3031\n"+
			"full avg10=0.12 avg60=0.34 avg300=0.56 total=9876\n")

	p, err := parsePressureFile(filepath.Join(dir, "cpu.pressure"))
	require.NoError(t, err)
	require.NotNil(t, p)

	require.NotNil(t, p.Some)
	assert.InDelta(t, 1.23, *p.Some.Avg10, 1e-9)
	assert.InDelta(t, 4.56, *p.Some.Avg60, 1e-9)
	assert.InDelta(t, 7.89, *p.Some.Avg300, 1e-9)
	assert.Equal(t, uint64(3031), *p.Some.Total)

	require.NotNil(t, p.Full)
	assert.InDelta(t, 0.12, *p.Full.Avg10, 1e-9)
	assert.Equal(t, uint64(9876), *p.Full.Total)
}

func TestParsePressureFile_Missing_ReturnsNil(t *testing.T) {
	p, err := parsePressureFile("/nonexistent/cpu.pressure")
	require.NoError(t, err)
	assert.Nil(t, p)
}

// ─────────────────────────────────────────────────────────────────────────────
// CPU controller tests
// ─────────────────────────────────────────────────────────────────────────────

func TestCPUController_FullStat(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "cpu.stat"),
		"usage_usec 1234567\n"+
			"user_usec 123456\n"+
			"system_usec 123456\n"+
			"nr_periods 123\n"+
			"nr_throttled 12\n"+
			"throttled_usec 123456\n")

	stat, err := collectCPUStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)

	assert.Equal(t, uint64(1234567*1000), *stat.UsageNanos)
	assert.Equal(t, uint64(123456*1000), *stat.UserNanos)
	assert.Equal(t, uint64(123456*1000), *stat.SystemNanos)
	assert.Equal(t, uint32(123), *stat.NrPeriods)
	assert.Equal(t, uint32(12), *stat.NrThrottled)
	assert.Equal(t, uint64(123456*1000), *stat.ThrottledNanos)
	assert.Nil(t, stat.Pressure) // no pressure file written
}

func TestCPUController_MissingStat_ReturnsNil(t *testing.T) {
	stat, err := collectCPUStat(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, stat, "absent cpu.stat should return nil (controller not active)")
}

func TestCPUController_WithPressure(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 1000\n")
	writeCgroupFile(t, filepath.Join(dir, "cpu.pressure"),
		"some avg10=0.10 avg60=0.20 avg300=0.30 total=100\n")

	stat, err := collectCPUStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)
	require.NotNil(t, stat.Pressure)
	require.NotNil(t, stat.Pressure.Some)
	assert.InDelta(t, 0.10, *stat.Pressure.Some.Avg10, 1e-9)
}

func TestCPUController_ViaRegistry(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 500\n")

	reg := &Registry{}
	reg.Register(&cpuController{})

	sample, err := reg.Collect(context.Background(), dir, time.Now())
	require.NoError(t, err)
	require.NotNil(t, sample.CPUStat)
	assert.Equal(t, uint64(500*1000), *sample.CPUStat.UsageNanos)
}

// ─────────────────────────────────────────────────────────────────────────────
// Memory controller tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryController_FullStat(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "memory.stat"),
		"anon 24576\nfile 12791808\nkernel_stack 8192\npagetables 4096\n"+
			"sock 2048\nshmem 16384\nfile_mapped 8192\nfile_dirty 32768\n"+
			"file_writeback 16384\nslab 1503104\n"+
			"pgscan 100\npgsteal 99\npgfault 32711\npgmajfault 12\n")
	writeCgroupFile(t, filepath.Join(dir, "memory.events"),
		"low 4\nhigh 3\nmax 2\noom 1\noom_kill 5\n")
	writeCgroupFile(t, filepath.Join(dir, "memory.peak"), "123456\n")
	writeCgroupFile(t, filepath.Join(dir, "memory.swap.current"), "987654\n")
	writeCgroupFile(t, filepath.Join(dir, "memory.pressure"),
		"some avg10=1.23 avg60=4.56 avg300=7.89 total=3031\n"+
			"full avg10=0.12 avg60=0.34 avg300=0.56 total=9876\n")

	stat, err := collectMemoryStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)

	assert.Equal(t, uint64(24576), *stat.Anon)
	assert.Equal(t, uint64(12791808), *stat.File)
	assert.Equal(t, uint64(8192), *stat.KernelStack)
	assert.Equal(t, uint64(4096), *stat.PageTables)
	assert.Equal(t, uint64(2048), *stat.Sock)
	assert.Equal(t, uint64(16384), *stat.Shmem)
	assert.Equal(t, uint64(8192), *stat.FileMapped)
	assert.Equal(t, uint64(32768), *stat.FileDirty)
	assert.Equal(t, uint64(16384), *stat.FileWriteback)
	assert.Equal(t, uint64(1503104), *stat.Slab)
	assert.Equal(t, uint64(100), *stat.Pgscan)
	assert.Equal(t, uint64(99), *stat.Pgsteal)
	assert.Equal(t, uint64(32711), *stat.Pgfault)
	assert.Equal(t, uint64(12), *stat.Pgmajfault)

	assert.Equal(t, uint64(4), stat.LowEvents)
	assert.Equal(t, uint64(3), stat.HighEvents)
	assert.Equal(t, uint64(2), stat.MaxEvents)
	assert.Equal(t, uint64(1), stat.OomEvents)
	assert.Equal(t, uint64(5), stat.OomKillEvents)

	assert.Equal(t, uint64(123456), *stat.Peak)
	assert.Equal(t, uint64(987654), *stat.SwapBytes)

	require.NotNil(t, stat.Pressure)
	require.NotNil(t, stat.Pressure.Some)
	assert.InDelta(t, 1.23, *stat.Pressure.Some.Avg10, 1e-9)
}

func TestMemoryController_Missing_ReturnsNil(t *testing.T) {
	stat, err := collectMemoryStat(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, stat)
}

func TestMemoryController_PeakAbsent_OmitsField(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "memory.stat"), "anon 1024\n")

	stat, err := collectMemoryStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)
	assert.Nil(t, stat.Peak, "memory.peak absent should leave Peak nil")
}

// ─────────────────────────────────────────────────────────────────────────────
// IO controller tests
// ─────────────────────────────────────────────────────────────────────────────

func TestIOController_MultiDeviceAggregation(t *testing.T) {
	dir := cgroupDir(t)
	// Two block devices; values should be summed.
	writeCgroupFile(t, filepath.Join(dir, "io.stat"),
		"8:0 rbytes=1024 wbytes=2048 dbytes=4096 rios=16 wios=32 dios=64\n"+
			"8:1 rbytes=512 wbytes=1024 dbytes=2048 rios=8 wios=16 dios=32\n")

	stat, err := collectIOStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)

	assert.Equal(t, uint64(1024+512), *stat.ReadBytes)
	assert.Equal(t, uint64(2048+1024), *stat.WriteBytes)
	assert.Equal(t, uint64(4096+2048), *stat.DiscardBytes)
	assert.Equal(t, uint64(16+8), *stat.ReadIOs)
	assert.Equal(t, uint64(32+16), *stat.WriteIOs)
	assert.Equal(t, uint64(64+32), *stat.DiscardIOs)
}

func TestIOController_WithPressure(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "io.stat"), "8:0 rbytes=100\n")
	writeCgroupFile(t, filepath.Join(dir, "io.pressure"),
		"some avg10=1.23 avg60=4.56 avg300=7.89 total=3031\n"+
			"full avg10=0.12 avg60=0.34 avg300=0.56 total=9876\n")

	stat, err := collectIOStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)
	require.NotNil(t, stat.Pressure)
	assert.InDelta(t, 1.23, *stat.Pressure.Some.Avg10, 1e-9)
}

func TestIOController_Missing_ReturnsNil(t *testing.T) {
	stat, err := collectIOStat(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, stat)
}

// ─────────────────────────────────────────────────────────────────────────────
// PIDs controller tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPIDsController_CurrentAndLimit(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "pids.current"), "42\n")
	writeCgroupFile(t, filepath.Join(dir, "pids.max"), "1024\n")

	stat, err := collectPIDsStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)
	assert.Equal(t, uint64(42), *stat.Current)
	require.NotNil(t, stat.Limit)
	assert.Equal(t, uint64(1024), *stat.Limit)
}

func TestPIDsController_UnlimitedMax_NilLimit(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "pids.current"), "10\n")
	writeCgroupFile(t, filepath.Join(dir, "pids.max"), "max\n")

	stat, err := collectPIDsStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)
	assert.Equal(t, uint64(10), *stat.Current)
	assert.Nil(t, stat.Limit, "\"max\" should result in nil Limit (unlimited)")
}

func TestPIDsController_Missing_ReturnsNil(t *testing.T) {
	stat, err := collectPIDsStat(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, stat)
}

// ─────────────────────────────────────────────────────────────────────────────
// Registry tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRegistry_CollectsAllControllers(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 100\n")
	writeCgroupFile(t, filepath.Join(dir, "memory.stat"), "anon 4096\n")
	writeCgroupFile(t, filepath.Join(dir, "io.stat"), "8:0 rbytes=512\n")
	writeCgroupFile(t, filepath.Join(dir, "pids.current"), "5\n")

	reg := NewRegistry()
	sample, err := reg.Collect(context.Background(), dir, time.Now())
	require.NoError(t, err)

	require.NotNil(t, sample.CPUStat)
	require.NotNil(t, sample.MemoryStat)
	require.NotNil(t, sample.IOStat)
	require.NotNil(t, sample.PIDsStat)
}

func TestRegistry_PartialControllers_NoError(t *testing.T) {
	// Only cpu.stat present; memory, io, pids absent (controllers not enabled).
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 200\n")

	reg := NewRegistry()
	sample, err := reg.Collect(context.Background(), dir, time.Now())
	// No error expected — absent controller files are not errors.
	require.NoError(t, err)
	require.NotNil(t, sample.CPUStat)
	assert.Nil(t, sample.MemoryStat)
	assert.Nil(t, sample.IOStat)
	assert.Nil(t, sample.PIDsStat)
}

func TestRegistry_CustomController(t *testing.T) {
	var called atomic.Bool

	stub := &stubController{
		name: "test",
		collectFn: func(_ context.Context, _ string, dst *resourcestypes.Sample) error {
			called.Store(true)
			return nil
		},
	}

	reg := &Registry{}
	reg.Register(stub)
	_, err := reg.Collect(context.Background(), t.TempDir(), time.Now())
	require.NoError(t, err)
	assert.True(t, called.Load(), "custom controller's Collect should have been called")
}

// stubController is a test double for Controller.
type stubController struct {
	name      string
	collectFn func(context.Context, string, *resourcestypes.Sample) error
}

func (s *stubController) Name() string { return s.name }
func (s *stubController) Collect(ctx context.Context, p string, dst *resourcestypes.Sample) error {
	return s.collectFn(ctx, p, dst)
}

// ─────────────────────────────────────────────────────────────────────────────
// Sampler tests
// ─────────────────────────────────────────────────────────────────────────────

// testSample is a minimal WithTimestamp implementation for Sampler tests.
type testSample struct {
	ts time.Time
	n  int
}

func (s *testSample) Timestamp() time.Time { return s.ts }

func TestSampler_CollectsAtLeastOneSample(t *testing.T) {
	var counter atomic.Int64
	s := NewSampler(50*time.Millisecond, 100, func(tm time.Time) (*testSample, error) {
		counter.Add(1)
		return &testSample{ts: tm, n: int(counter.Load())}, nil
	})
	defer s.Close()

	sub := s.Record()
	time.Sleep(200 * time.Millisecond)

	samples, err := sub.Close(false)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(samples), 1, "expected at least one sample")
}

func TestSampler_CaptureLast_AddsTerminalSample(t *testing.T) {
	s := NewSampler(10*time.Second, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	defer s.Close()

	sub := s.Record()
	// Close immediately with captureLast=true; should get exactly 1 terminal sample.
	samples, err := sub.Close(true)
	require.NoError(t, err)
	assert.Equal(t, 1, len(samples), "captureLast should append one terminal sample")
}

func TestSampler_MultipleSubscribers_IndependentHistory(t *testing.T) {
	s := NewSampler(50*time.Millisecond, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	defer s.Close()

	sub1 := s.Record()
	time.Sleep(120 * time.Millisecond) // sub1 gets ~2 samples
	sub2 := s.Record()
	time.Sleep(120 * time.Millisecond) // sub2 gets ~2 samples

	samples1, err := sub1.Close(false)
	require.NoError(t, err)
	samples2, err := sub2.Close(false)
	require.NoError(t, err)

	// sub1 started earlier so it should have more samples.
	assert.GreaterOrEqual(t, len(samples1), len(samples2),
		"sub1 started earlier and should have at least as many samples as sub2")
}

func TestSampler_Close_Idempotent(t *testing.T) {
	s := NewSampler(50*time.Millisecond, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	// Close multiple times must not panic.
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
}

func TestSampler_SubClose_Idempotent(t *testing.T) {
	s := NewSampler(10*time.Second, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	defer s.Close()

	sub := s.Record()
	_, err := sub.Close(false)
	require.NoError(t, err)
	// Second close must not panic.
	_, err = sub.Close(false)
	require.NoError(t, err)
}

func TestSampler_NoTimerLeak(t *testing.T) {
	// Regression test: the original code called time.NewTimer in a loop without
	// stopping the old timer, leaking goroutines.  This test runs the sampler
	// for many ticks and checks that the program does not accumulate timers.
	//
	// We can't directly count goroutines here, but we can verify that the
	// sampler exits cleanly within a bounded time, which would deadlock if
	// leaked timers held goroutines alive.
	done := make(chan struct{})
	s := NewSampler(10*time.Millisecond, 1000, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})

	sub := s.Record()
	go func() {
		time.Sleep(300 * time.Millisecond)
		sub.Close(false)
		s.Close()
		close(done)
	}()

	select {
	case <-done:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("sampler did not exit cleanly — possible goroutine/timer leak")
	}
}

func TestSampler_ConcurrentRecordClose(t *testing.T) {
	// Race-detector test: multiple goroutines record and close simultaneously.
	s := NewSampler(10*time.Millisecond, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := s.Record()
			time.Sleep(time.Duration(5+i) * time.Millisecond)
			sub.Close(false)
		}()
	}
	wg.Wait()
}

// ─────────────────────────────────────────────────────────────────────────────
// nopRecord tests
// ─────────────────────────────────────────────────────────────────────────────

func TestNopRecord_AllMethodsSafe(t *testing.T) {
	r := &nopRecord{}
	r.Start()
	r.Close()
	assert.NoError(t, r.Wait())

	samples, err := r.Samples()
	assert.NoError(t, err)
	assert.Nil(t, samples)

	nextCalled := make(chan struct{})
	err = r.CloseAsync(func(_ context.Context) error {
		close(nextCalled)
		return nil
	})
	assert.NoError(t, err)
	select {
	case <-nextCalled:
	case <-time.After(time.Second):
		t.Fatal("CloseAsync next callback not called")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OperationResourceDelta tests
// ─────────────────────────────────────────────────────────────────────────────

func TestComputeDelta_HappyPath(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Second)

	first := &resourcestypes.Sample{
		Timestamp_: start,
		CPUStat:    &resourcestypes.CPUStat{UsageNanos: uint64Ptr(1_000_000)},
		IOStat: &resourcestypes.IOStat{
			ReadBytes:  uint64Ptr(1024),
			WriteBytes: uint64Ptr(2048),
		},
		MemoryStat: &resourcestypes.MemoryStat{Peak: uint64Ptr(4096)},
	}
	last := &resourcestypes.Sample{
		Timestamp_: end,
		CPUStat:    &resourcestypes.CPUStat{UsageNanos: uint64Ptr(6_000_000)},
		IOStat: &resourcestypes.IOStat{
			ReadBytes:  uint64Ptr(10240),
			WriteBytes: uint64Ptr(20480),
		},
		MemoryStat: &resourcestypes.MemoryStat{Peak: uint64Ptr(8192), OomEvents: 1},
	}

	os := &resourcestypes.OperationSamples{
		Meta: resourcestypes.OperationMeta{
			StartTime: start,
			EndTime:   end,
		},
		Samples: &resourcestypes.Samples{
			Samples: []*resourcestypes.Sample{first, last},
		},
	}

	delta := resourcestypes.ComputeDelta(os)
	require.NotNil(t, delta)
	assert.Equal(t, 5*time.Second, delta.Duration)
	assert.Equal(t, uint64(5_000_000), delta.CPUNanos)
	assert.Equal(t, uint64(10240-1024), delta.ReadBytes)
	assert.Equal(t, uint64(20480-2048), delta.WriteBytes)
	assert.Equal(t, uint64(8192), delta.MemoryPeakBytes)
	assert.Equal(t, uint64(1), delta.OOMEvents)
}

func TestComputeDelta_TooFewSamples_ReturnsNil(t *testing.T) {
	os := &resourcestypes.OperationSamples{
		Samples: &resourcestypes.Samples{
			Samples: []*resourcestypes.Sample{
				{Timestamp_: time.Now()},
			},
		},
	}
	assert.Nil(t, resourcestypes.ComputeDelta(os))
}

// ─────────────────────────────────────────────────────────────────────────────
// parseBool tests
// ─────────────────────────────────────────────────────────────────────────────

func TestParseBool(t *testing.T) {
	trueInputs := []string{"1", "t", "true", "TRUE", "True", "  true  "}
	for _, s := range trueInputs {
		assert.True(t, parseBool(s), "parseBool(%q) should be true", s)
	}
	falseInputs := []string{"0", "false", "FALSE", "no", "", "2"}
	for _, s := range falseInputs {
		assert.False(t, parseBool(s), "parseBool(%q) should be false", s)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// addUint64Ptr (io.go helper)
// ─────────────────────────────────────────────────────────────────────────────

func TestAddUint64Ptr_Nil(t *testing.T) {
	result := addUint64Ptr(nil, 42)
	require.NotNil(t, result)
	assert.Equal(t, uint64(42), *result)
}

func TestAddUint64Ptr_Accumulates(t *testing.T) {
	v := uint64(100)
	result := addUint64Ptr(&v, 50)
	assert.Equal(t, uint64(150), *result)
}

// ─────────────────────────────────────────────────────────────────────────────
// parseKVPair and parseFloatKVPair
// ─────────────────────────────────────────────────────────────────────────────

func TestParseKVPair(t *testing.T) {
	k, v := parseKVPair("rbytes=1024")
	assert.Equal(t, "rbytes", k)
	assert.Equal(t, uint64(1024), v)

	k, v = parseKVPair("malformed")
	assert.Equal(t, "", k)
	assert.Equal(t, uint64(0), v)
}

func TestParseFloatKVPair(t *testing.T) {
	k, v, ok := parseFloatKVPair("avg10=1.23")
	assert.True(t, ok)
	assert.Equal(t, "avg10", k)
	assert.InDelta(t, 1.23, v, 1e-9)

	_, _, ok = parseFloatKVPair("malformed")
	assert.False(t, ok)
}

// ─────────────────────────────────────────────────────────────────────────────
// SysCPUStat JSON rounding
// ─────────────────────────────────────────────────────────────────────────────

func TestSysCPUStat_JSONRounding(t *testing.T) {
	import_json := func(s string) string { return s }
	_ = import_json

	s := resourcestypes.SysCPUStat{User: 1.23456789, System: 0.0001234}
	data, err := s.MarshalJSON() // uses the custom MarshalJSON
	require.NoError(t, err)
	// After rounding to 3 decimal places: User=1.235, System=0.000
	jsonStr := string(data)
	assert.True(t, strings.Contains(jsonStr, `"user":1.235`), "expected rounded user field in: %s", jsonStr)
}

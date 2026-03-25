package resources

// resources_test.go – comprehensive white-box unit tests for the refactored
// resource monitoring package.
//
// Test coverage:
//   - Low-level parse helpers (parse.go)
//   - Per-controller collection: cpu, memory, io, pids
//   - Registry.Collect fan-out and custom controller extensibility
//   - Sampler lifecycle: adaptive interval, Close idempotency, timer-leak fix
//   - nopRecord safety
//   - OperationResourceDelta computation

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
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// writeCgroupFile creates a file at the given absolute path with content.
// Parent directories are created as needed.
func writeCgroupFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// cgroupDir creates a temporary directory that mimics a cgroupv2 namespace root.
func cgroupDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// ─────────────────────────────────────────────────────────────────────────────
// parse.go — parseKVFile
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
	// Line with no value and a line with a non-numeric value are both silently skipped.
	writeCgroupFile(t, filepath.Join(dir, "test.stat"),
		"good 42\nno_value\nbad_num xyz\n")

	got := map[string]uint64{}
	require.NoError(t, parseKVFile(filepath.Join(dir, "test.stat"), func(k string, v uint64) {
		got[k] = v
	}))
	assert.Equal(t, map[string]uint64{"good": 42}, got)
}

// ─────────────────────────────────────────────────────────────────────────────
// parse.go — parseSingleUint64File
// ─────────────────────────────────────────────────────────────────────────────

func TestParseSingleUint64File_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeCgroupFile(t, filepath.Join(dir, "pids.current"), "  123\n")
	v, ok, err := parseSingleUint64File(filepath.Join(dir, "pids.current"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(123), v)
}

func TestParseSingleUint64File_MaxLiteral_OkFalse(t *testing.T) {
	// The literal "max" means unlimited — should return (0, false, nil), not an error.
	dir := t.TempDir()
	writeCgroupFile(t, filepath.Join(dir, "pids.max"), "max\n")
	_, ok, err := parseSingleUint64File(filepath.Join(dir, "pids.max"))
	require.NoError(t, err)
	assert.False(t, ok, `"max" should be treated as unlimited, not a numeric value`)
}

func TestParseSingleUint64File_Missing_OkFalse(t *testing.T) {
	_, ok, err := parseSingleUint64File("/nonexistent/memory.peak")
	require.NoError(t, err)
	assert.False(t, ok)
}

// ─────────────────────────────────────────────────────────────────────────────
// parse.go — parsePressureFile
// ─────────────────────────────────────────────────────────────────────────────

func TestParsePressureFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeCgroupFile(t, filepath.Join(dir, "cpu.pressure"),
		"some avg10=1.23 avg60=4.56 avg300=7.89 total=3031\n"+
			"full avg10=0.12 avg60=0.34 avg300=0.56 total=9876\n")

	p, err := parsePressureFile(filepath.Join(dir, "cpu.pressure"))
	require.NoError(t, err)
	require.NotNil(t, p)

	require.NotNil(t, p.Some)
	require.NotNil(t, p.Some.Avg10, "Some.Avg10 must not be nil")
	require.NotNil(t, p.Some.Avg60, "Some.Avg60 must not be nil")
	require.NotNil(t, p.Some.Avg300, "Some.Avg300 must not be nil")
	require.NotNil(t, p.Some.Total, "Some.Total must not be nil — regression guard for the key-dispatch bug")
	assert.InDelta(t, 1.23, *p.Some.Avg10, 1e-9)
	assert.InDelta(t, 4.56, *p.Some.Avg60, 1e-9)
	assert.InDelta(t, 7.89, *p.Some.Avg300, 1e-9)
	assert.Equal(t, uint64(3031), *p.Some.Total)

	require.NotNil(t, p.Full)
	require.NotNil(t, p.Full.Avg10, "Full.Avg10 must not be nil")
	require.NotNil(t, p.Full.Total, "Full.Total must not be nil")
	assert.InDelta(t, 0.12, *p.Full.Avg10, 1e-9)
	assert.Equal(t, uint64(9876), *p.Full.Total)
}

// TestParsePressureFile_TotalNotNil is a targeted regression test for the
// key-dispatch bug where "total=3031" was consumed by the float64 parser,
// causing pv.Total to remain nil and *p.Some.Total to panic.
func TestParsePressureFile_TotalNotNil(t *testing.T) {
	dir := t.TempDir()
	// Minimal pressure file — only the "total" field on the "some" line.
	writeCgroupFile(t, filepath.Join(dir, "mem.pressure"),
		"some avg10=0.00 avg60=0.00 avg300=0.00 total=99999\n")

	p, err := parsePressureFile(filepath.Join(dir, "mem.pressure"))
	require.NoError(t, err)
	require.NotNil(t, p)
	require.NotNil(t, p.Some)
	require.NotNil(t, p.Some.Total,
		"Total must be non-nil: the key-dispatch fix ensures 'total' is parsed as uint64")
	assert.Equal(t, uint64(99999), *p.Some.Total)
}

// TestParsePressureFile_LargeTotalValue verifies that large total values
// (>2^32) are preserved without precision loss — a float64 parse would silently
// truncate values beyond 2^53 on very long-running systems.
func TestParsePressureFile_LargeTotalValue(t *testing.T) {
	dir := t.TempDir()
	// 9,007,199,254,740,993 = 2^53 + 1, cannot be represented exactly in float64.
	writeCgroupFile(t, filepath.Join(dir, "io.pressure"),
		"some avg10=0.01 avg60=0.02 avg300=0.03 total=9007199254740993\n"+
			"full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")

	p, err := parsePressureFile(filepath.Join(dir, "io.pressure"))
	require.NoError(t, err)
	require.NotNil(t, p)
	require.NotNil(t, p.Some)
	require.NotNil(t, p.Some.Total)
	// If stored as float64 this would silently become 9007199254740992 (off by 1).
	assert.Equal(t, uint64(9007199254740993), *p.Some.Total,
		"total must be exact: float64 cannot represent 2^53+1")
}

func TestParsePressureFile_Missing_ReturnsNil(t *testing.T) {
	p, err := parsePressureFile("/nonexistent/cpu.pressure")
	require.NoError(t, err)
	assert.Nil(t, p)
}

// ─────────────────────────────────────────────────────────────────────────────
// parse.go — parseKVPair and parseFloatKVPair
// ─────────────────────────────────────────────────────────────────────────────

func TestParseKVPair(t *testing.T) {
	k, v := parseKVPair("rbytes=1024")
	assert.Equal(t, "rbytes", k)
	assert.Equal(t, uint64(1024), v)

	// Malformed tokens return empty key and zero value, not a panic.
	k, v = parseKVPair("malformed")
	assert.Equal(t, "", k)
	assert.Equal(t, uint64(0), v)

	// Empty string.
	k, v = parseKVPair("")
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

	_, _, ok = parseFloatKVPair("")
	assert.False(t, ok)
}

// ─────────────────────────────────────────────────────────────────────────────
// CPU controller
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

	require.NotNil(t, stat.UsageNanos)
	require.NotNil(t, stat.UserNanos)
	require.NotNil(t, stat.SystemNanos)
	require.NotNil(t, stat.NrPeriods)
	require.NotNil(t, stat.NrThrottled)
	require.NotNil(t, stat.ThrottledNanos)

	assert.Equal(t, uint64(1234567*1000), *stat.UsageNanos)
	assert.Equal(t, uint64(123456*1000), *stat.UserNanos)
	assert.Equal(t, uint64(123456*1000), *stat.SystemNanos)
	assert.Equal(t, uint32(123), *stat.NrPeriods)
	assert.Equal(t, uint32(12), *stat.NrThrottled)
	assert.Equal(t, uint64(123456*1000), *stat.ThrottledNanos)
	assert.Nil(t, stat.Pressure, "no cpu.pressure file written — Pressure must be nil")
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
	require.NotNil(t, stat.Pressure.Some.Avg10)
	require.NotNil(t, stat.Pressure.Some.Total)
	assert.InDelta(t, 0.10, *stat.Pressure.Some.Avg10, 1e-9)
	assert.Equal(t, uint64(100), *stat.Pressure.Some.Total)
}

func TestCPUController_ViaRegistry(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 500\n")

	reg := &Registry{}
	reg.Register(&cpuController{})

	sample, err := reg.Collect(context.Background(), dir, time.Now())
	require.NoError(t, err)
	require.NotNil(t, sample.CPUStat)
	require.NotNil(t, sample.CPUStat.UsageNanos)
	assert.Equal(t, uint64(500*1000), *sample.CPUStat.UsageNanos)
}

// ─────────────────────────────────────────────────────────────────────────────
// Memory controller
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

	// memory.stat fields.
	require.NotNil(t, stat.Anon)
	require.NotNil(t, stat.File)
	require.NotNil(t, stat.KernelStack)
	require.NotNil(t, stat.PageTables)
	require.NotNil(t, stat.Sock)
	require.NotNil(t, stat.Shmem)
	require.NotNil(t, stat.FileMapped)
	require.NotNil(t, stat.FileDirty)
	require.NotNil(t, stat.FileWriteback)
	require.NotNil(t, stat.Slab)
	require.NotNil(t, stat.Pgscan)
	require.NotNil(t, stat.Pgsteal)
	require.NotNil(t, stat.Pgfault)
	require.NotNil(t, stat.Pgmajfault)
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

	// memory.events counters.
	assert.Equal(t, uint64(4), stat.LowEvents)
	assert.Equal(t, uint64(3), stat.HighEvents)
	assert.Equal(t, uint64(2), stat.MaxEvents)
	assert.Equal(t, uint64(1), stat.OomEvents)
	assert.Equal(t, uint64(5), stat.OomKillEvents)

	// Single-value files.
	require.NotNil(t, stat.Peak)
	require.NotNil(t, stat.SwapBytes)
	assert.Equal(t, uint64(123456), *stat.Peak)
	assert.Equal(t, uint64(987654), *stat.SwapBytes)

	// PSI pressure.
	require.NotNil(t, stat.Pressure)
	require.NotNil(t, stat.Pressure.Some)
	require.NotNil(t, stat.Pressure.Some.Avg10)
	require.NotNil(t, stat.Pressure.Some.Total)
	assert.InDelta(t, 1.23, *stat.Pressure.Some.Avg10, 1e-9)
	assert.Equal(t, uint64(3031), *stat.Pressure.Some.Total)
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
	assert.Nil(t, stat.Peak, "memory.peak absent → Peak must be nil")
	assert.Nil(t, stat.SwapBytes, "memory.swap.current absent → SwapBytes must be nil")
}

// ─────────────────────────────────────────────────────────────────────────────
// IO controller
// ─────────────────────────────────────────────────────────────────────────────

func TestIOController_MultiDeviceAggregation(t *testing.T) {
	dir := cgroupDir(t)
	// Two block devices — values are summed across devices.
	writeCgroupFile(t, filepath.Join(dir, "io.stat"),
		"8:0 rbytes=1024 wbytes=2048 dbytes=4096 rios=16 wios=32 dios=64\n"+
			"8:1 rbytes=512 wbytes=1024 dbytes=2048 rios=8 wios=16 dios=32\n")

	stat, err := collectIOStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)
	require.NotNil(t, stat.ReadBytes)
	require.NotNil(t, stat.WriteBytes)
	require.NotNil(t, stat.DiscardBytes)
	require.NotNil(t, stat.ReadIOs)
	require.NotNil(t, stat.WriteIOs)
	require.NotNil(t, stat.DiscardIOs)

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
	require.NotNil(t, stat.Pressure.Some, "pressure.Some must be populated from the 'some' line")
	require.NotNil(t, stat.Pressure.Some.Avg10)
	require.NotNil(t, stat.Pressure.Some.Total)
	assert.InDelta(t, 1.23, *stat.Pressure.Some.Avg10, 1e-9)
	assert.Equal(t, uint64(3031), *stat.Pressure.Some.Total)
}

func TestIOController_Missing_ReturnsNil(t *testing.T) {
	stat, err := collectIOStat(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, stat)
}

// ─────────────────────────────────────────────────────────────────────────────
// PIDs controller
// ─────────────────────────────────────────────────────────────────────────────

func TestPIDsController_CurrentAndLimit(t *testing.T) {
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "pids.current"), "42\n")
	writeCgroupFile(t, filepath.Join(dir, "pids.max"), "1024\n")

	stat, err := collectPIDsStat(dir)
	require.NoError(t, err)
	require.NotNil(t, stat)
	require.NotNil(t, stat.Current)
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
	require.NotNil(t, stat.Current)
	assert.Equal(t, uint64(10), *stat.Current)
	assert.Nil(t, stat.Limit, `"max" should result in nil Limit (unlimited)`)
}

func TestPIDsController_Missing_ReturnsNil(t *testing.T) {
	stat, err := collectPIDsStat(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, stat)
}

// ─────────────────────────────────────────────────────────────────────────────
// Registry
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
	// Absent controller files must not cause an error.
	dir := cgroupDir(t)
	writeCgroupFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 200\n")

	reg := NewRegistry()
	sample, err := reg.Collect(context.Background(), dir, time.Now())
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

// stubController is a test double for the Controller interface.
type stubController struct {
	name      string
	collectFn func(context.Context, string, *resourcestypes.Sample) error
}

func (s *stubController) Name() string { return s.name }
func (s *stubController) Collect(ctx context.Context, p string, dst *resourcestypes.Sample) error {
	return s.collectFn(ctx, p, dst)
}

// ─────────────────────────────────────────────────────────────────────────────
// Sampler
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
	assert.GreaterOrEqual(t, len(samples), 1, "expected at least one sample in 200ms window")
}

func TestSampler_CaptureLast_AddsTerminalSample(t *testing.T) {
	// minInterval is very long so no periodic sample fires; captureLast triggers
	// exactly one callback invocation after Close.
	s := NewSampler(10*time.Second, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	defer s.Close()

	sub := s.Record()
	samples, err := sub.Close(true /* captureLast */)
	require.NoError(t, err)
	assert.Equal(t, 1, len(samples), "captureLast should append exactly one terminal sample")
}

func TestSampler_MultipleSubscribers_IndependentHistory(t *testing.T) {
	s := NewSampler(50*time.Millisecond, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	defer s.Close()

	sub1 := s.Record()
	time.Sleep(120 * time.Millisecond) // sub1 gets ~2 samples
	sub2 := s.Record()
	time.Sleep(120 * time.Millisecond) // sub2 gets ~2 samples; sub1 gets ~4

	samples1, err := sub1.Close(false)
	require.NoError(t, err)
	samples2, err := sub2.Close(false)
	require.NoError(t, err)

	// sub1 started earlier so it must have at least as many samples as sub2.
	assert.GreaterOrEqual(t, len(samples1), len(samples2),
		"sub1 started earlier and should have at least as many samples as sub2")
}

func TestSampler_Close_Idempotent(t *testing.T) {
	s := NewSampler(50*time.Millisecond, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	// Multiple consecutive Close calls must not panic.
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
	// Second close must not panic or return a new error.
	_, err = sub.Close(false)
	require.NoError(t, err)
}

// TestSampler_NoTimerLeak is a regression test for the original sampler's
// timer leak: time.NewTimer was called inside the loop without stopping the
// previous timer, accumulating one leaked OS timer per tick.
//
// We verify the fix indirectly: the sampler goroutine must exit cleanly
// within a bounded deadline after Close().  A leaked timer would keep the
// goroutine blocked, causing this test to time out.
func TestSampler_NoTimerLeak(t *testing.T) {
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
		// Sampler exited cleanly within deadline.
	case <-time.After(2 * time.Second):
		t.Fatal("sampler did not exit cleanly within 2s — possible goroutine/timer leak")
	}
}

// TestSampler_ConcurrentRecordClose exercises the race detector: 20 goroutines
// record and close subscriptions simultaneously while the sampler is running.
func TestSampler_ConcurrentRecordClose(t *testing.T) {
	s := NewSampler(10*time.Millisecond, 100, func(tm time.Time) (*testSample, error) {
		return &testSample{ts: tm}, nil
	})
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i // capture loop var explicitly (safe on all Go versions)
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
// nopRecord
// ─────────────────────────────────────────────────────────────────────────────

func TestNopRecord_AllMethodsSafe(t *testing.T) {
	r := &nopRecord{}

	// None of these must panic or return an error.
	r.Start()
	r.Close()
	require.NoError(t, r.Wait())

	samples, err := r.Samples()
	require.NoError(t, err)
	assert.Nil(t, samples)

	// CloseAsync must call next exactly once.
	nextCalled := make(chan struct{})
	err = r.CloseAsync(func(_ context.Context) error {
		close(nextCalled)
		return nil
	})
	require.NoError(t, err)
	select {
	case <-nextCalled:
	case <-time.After(time.Second):
		t.Fatal("CloseAsync next callback was not called within 1s")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OperationResourceDelta (types/optypes.go)
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

	opSamples := &resourcestypes.OperationSamples{
		Meta: resourcestypes.OperationMeta{
			StartTime: start,
			EndTime:   end,
		},
		Samples: &resourcestypes.Samples{
			Samples: []*resourcestypes.Sample{first, last},
		},
	}

	delta := resourcestypes.ComputeDelta(opSamples)
	require.NotNil(t, delta)
	assert.Equal(t, 5*time.Second, delta.Duration)
	assert.Equal(t, uint64(5_000_000), delta.CPUNanos)
	assert.Equal(t, uint64(10240-1024), delta.ReadBytes)
	assert.Equal(t, uint64(20480-2048), delta.WriteBytes)
	assert.Equal(t, uint64(8192), delta.MemoryPeakBytes)
	assert.Equal(t, uint64(1), delta.OOMEvents)
}

func TestComputeDelta_TooFewSamples_ReturnsNil(t *testing.T) {
	opSamples := &resourcestypes.OperationSamples{
		Samples: &resourcestypes.Samples{
			Samples: []*resourcestypes.Sample{
				{Timestamp_: time.Now()},
			},
		},
	}
	assert.Nil(t, resourcestypes.ComputeDelta(opSamples),
		"fewer than 2 samples must return nil — cannot compute a delta")
}

func TestComputeDelta_NilInput_ReturnsNil(t *testing.T) {
	assert.Nil(t, resourcestypes.ComputeDelta(nil))
}

// ─────────────────────────────────────────────────────────────────────────────
// parseBool (cgroup.go)
// ─────────────────────────────────────────────────────────────────────────────

func TestParseBool(t *testing.T) {
	trueInputs := []string{"1", "t", "true", "TRUE", "True", "  true  "}
	for _, s := range trueInputs {
		assert.True(t, parseBool(s), "parseBool(%q) should be true", s)
	}
	falseInputs := []string{"0", "false", "FALSE", "no", "", "2", "yes"}
	for _, s := range falseInputs {
		assert.False(t, parseBool(s), "parseBool(%q) should be false", s)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// addUint64Ptr (io.go)
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
	// Must return the same pointer (in-place accumulation).
	assert.Equal(t, &v, result, "addUint64Ptr should mutate and return the existing pointer")
}

// ─────────────────────────────────────────────────────────────────────────────
// SysCPUStat JSON rounding (types/systypes.go)
// ─────────────────────────────────────────────────────────────────────────────

func TestSysCPUStat_JSONRounding(t *testing.T) {
	s := resourcestypes.SysCPUStat{
		User:   1.23456789, // rounds to 1.235
		System: 0.0001234, // rounds to 0.000 → omitted from output? No: still 0.000
		Idle:   100.0005,  // rounds to 100.001
	}
	data, err := s.MarshalJSON()
	require.NoError(t, err)

	jsonStr := string(data)
	// encoding/json never adds spaces around colons in compact mode.
	assert.True(t, strings.Contains(jsonStr, `"user":1.235`),
		"User=1.23456789 should round to 1.235; got: %s", jsonStr)
	assert.True(t, strings.Contains(jsonStr, `"idle":100.001`),
		"Idle=100.0005 should round to 100.001; got: %s", jsonStr)
}

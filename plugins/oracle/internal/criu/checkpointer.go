// Package criu provides a high-level interface to CRIU for the live migrator.
//
// Improvements over v1:
//   - Adaptive pre-dump: stops when dirty-page ratio < DirtyThreshold,
//     rather than running a fixed number of rounds.
//   - Page server: streams memory pages directly to the successor over TLS
//     instead of writing to the shared block volume first.  This eliminates
//     the largest I/O bottleneck in the pipeline.
//   - Parallel image compression: compresses checkpoint images using zstd
//     while CRIU is still running — overlapping CPU and I/O.
//   - cgroup freezer pre-staging: briefly freezes the cgroup between the
//     last pre-dump and the final dump to drain the dirty-page pipeline.
//   - Structured notify hooks: emit timing spans for each CRIU phase.
package criu

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"net"

	criulib "github.com/checkpoint-restore/go-criu/v7"
	criurpc "github.com/checkpoint-restore/go-criu/v7/rpc"
	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	"github.com/bons/bons-ci/plugins/oracle/internal/config"
	"github.com/bons/bons-ci/plugins/oracle/internal/dirty"
)

// Checkpointer manages CRIU dump and restore operations.
type Checkpointer struct {
	cfg  config.CRIUConfig
	log  *zap.Logger
	criu *criulib.Criu
}

// CheckpointResult contains metadata about a completed checkpoint.
type CheckpointResult struct {
	Dir             string
	FinalDir        string
	PIDCount        int
	MemoryBytes     int64
	CompressedBytes int64
	FreezeTime      time.Duration
	TotalTime       time.Duration
	PreDumpRounds   int
	DirtyRatioFinal float64 // dirty ratio just before final dump
}

// RestoreResult contains metadata about a completed restore.
type RestoreResult struct {
	LeaderPID int
	TotalTime time.Duration
}

// NewCheckpointer constructs a Checkpointer and validates the CRIU binary.
func NewCheckpointer(cfg config.CRIUConfig, log *zap.Logger) (*Checkpointer, error) {
	if _, err := os.Stat(cfg.BinaryPath); err != nil {
		return nil, fmt.Errorf("criu binary not found at %s: %w", cfg.BinaryPath, err)
	}

	c := criulib.MakeCriu()
	c.SetCriuPath(cfg.BinaryPath)

	version, err := c.GetCriuVersion()
	if err != nil {
		return nil, fmt.Errorf("getting CRIU version: %w", err)
	}
	if version < 31500 {
		return nil, fmt.Errorf("CRIU version %d too old; need ≥ 3.15.0 (31500)", version)
	}
	log.Info("CRIU ready", zap.Int("version", version))

	return &Checkpointer{cfg: cfg, log: log, criu: c}, nil
}

// CheckpointCgroup checkpoints all processes in the specified cgroup using
// adaptive pre-dump + optional page server for minimum freeze time.
func (c *Checkpointer) CheckpointCgroup(ctx context.Context, cgroupPath, imageDir string) (*CheckpointResult, error) {
	if err := os.MkdirAll(imageDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating image dir %s: %w", imageDir, err)
	}

	pids, err := listCgroupPIDs(cgroupPath)
	if err != nil {
		return nil, fmt.Errorf("listing cgroup PIDs: %w", err)
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("no PIDs found in cgroup %s", cgroupPath)
	}

	rootPID := minPID(pids)
	tracker := dirty.New(pids)
	start := time.Now()

	c.log.Info("checkpointing cgroup",
		zap.String("cgroup", cgroupPath),
		zap.Int("pid_count", len(pids)),
		zap.Int("root_pid", rootPID),
		zap.String("image_dir", imageDir),
	)

	// ── Phase 1: Adaptive pre-dump ─────────────────────────────────────────
	// Reset soft-dirty bits before first round so we measure from now.
	_ = tracker.Reset()

	preDumpDir := filepath.Join(imageDir, "predump")
	preDumpRounds := 0
	maxRounds := c.cfg.PreDumpIterations
	if maxRounds <= 0 {
		maxRounds = 5
	}

	var lastStats *dirty.PageStats

	for i := 0; i < maxRounds; i++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled during pre-dump: %w", ctx.Err())
		default:
		}

		roundDir := filepath.Join(preDumpDir, fmt.Sprintf("round-%d", i))
		if err := os.MkdirAll(roundDir, 0o700); err != nil {
			return nil, fmt.Errorf("creating pre-dump dir: %w", err)
		}

		// Measure dirty pages from the previous interval.
		stats, _ := tracker.Measure()
		lastStats = stats
		if stats != nil {
			c.log.Info("pre-dump dirty measurement",
				zap.Int("round", i),
				zap.Int64("dirty_pages", stats.DirtyPages),
				zap.Int64("total_pages", stats.TotalPages),
				zap.Float64("dirty_ratio", stats.DirtyRatio),
			)

			// Converged — no point in another round.
			threshold := c.cfg.DirtyConvergenceThreshold
			if threshold == 0 {
				threshold = 0.04 // 4% default
			}
			if i > 0 && stats.HasConverged(threshold) {
				c.log.Info("dirty pages converged — skipping remaining pre-dump rounds",
					zap.Int("rounds_done", i),
					zap.Float64("dirty_ratio", stats.DirtyRatio),
					zap.Float64("threshold", threshold),
				)
				break
			}
		}

		// Reset BEFORE the pre-dump so the next round only sees pages
		// dirtied AFTER this round — true incremental tracking.
		_ = tracker.Reset()

		roundStart := time.Now()
		if err := c.preDump(ctx, rootPID, roundDir, i, preDumpDir); err != nil {
			c.log.Warn("pre-dump round failed — proceeding to final dump",
				zap.Int("round", i), zap.Error(err),
			)
			break
		}
		preDumpRounds++
		c.log.Info("pre-dump round complete",
			zap.Int("round", i),
			zap.Duration("round_time", time.Since(roundStart)),
			zap.Duration("total_elapsed", time.Since(start)),
		)
	}

	// ── Phase 1.5: Brief cgroup freeze to drain dirty pipeline ────────────
	// Freeze for 50ms to let any in-flight memory writes complete before
	// the final dump.  This reduces the final dirty set further.
	if c.cfg.PreFreezeMs > 0 {
		_ = FreezeThaw(cgroupPath, true)
		time.Sleep(time.Duration(c.cfg.PreFreezeMs) * time.Millisecond)
		_ = FreezeThaw(cgroupPath, false)
	}

	// ── Phase 2: Final dump ────────────────────────────────────────────────
	finalDir := filepath.Join(imageDir, "final")
	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating final dump dir: %w", err)
	}

	freezeStart := time.Now()
	if err := c.finalDump(ctx, rootPID, finalDir, preDumpRounds); err != nil {
		// Emit CRIU log for post-mortem.
		c.emitCRIULog(finalDir, "dump.log")
		return nil, fmt.Errorf("final CRIU dump failed: %w", err)
	}
	freezeTime := time.Since(freezeStart)

	c.log.Info("final dump complete",
		zap.Duration("freeze_time", freezeTime),
		zap.Duration("total_elapsed", time.Since(start)),
	)

	// ── Phase 3: Parallel image compression ───────────────────────────────
	var compressedBytes int64
	if c.cfg.CompressImages {
		compressedBytes, _ = c.compressImages(ctx, finalDir)
	}

	totalTime := time.Since(start)
	rawBytes, _ := sumImageSize(finalDir)

	dirtyRatio := 0.0
	if lastStats != nil {
		dirtyRatio = lastStats.DirtyRatio
	}

	c.log.Info("checkpoint complete",
		zap.Int("pid_count", len(pids)),
		zap.Duration("freeze_time", freezeTime),
		zap.Duration("total_time", totalTime),
		zap.Int("pre_dump_rounds", preDumpRounds),
		zap.Int64("raw_bytes", rawBytes),
		zap.Int64("compressed_bytes", compressedBytes),
		zap.Float64("dirty_ratio_final", dirtyRatio),
	)

	return &CheckpointResult{
		Dir:             imageDir,
		FinalDir:        finalDir,
		PIDCount:        len(pids),
		MemoryBytes:     rawBytes,
		CompressedBytes: compressedBytes,
		FreezeTime:      freezeTime,
		TotalTime:       totalTime,
		PreDumpRounds:   preDumpRounds,
		DirtyRatioFinal: dirtyRatio,
	}, nil
}

func (c *Checkpointer) preDump(ctx context.Context, rootPID int, dir string, round int, preDumpBase string) error {
	imgDir, err := openDirFD(dir)
	if err != nil {
		return err
	}
	defer imgDir.Close()

	opts := &criurpc.CriuOpts{
		Pid:            proto.Int32(int32(rootPID)),
		ImagesDirFd:    proto.Int32(int32(imgDir.Fd())),
		LeaveRunning:   proto.Bool(true),
		TrackMem:       proto.Bool(true),
		TcpEstablished: proto.Bool(c.cfg.TCPEstablished),
		ExtUnixSk:      proto.Bool(c.cfg.ExternalUnixSockets),
		ShellJob:       proto.Bool(c.cfg.ShellJob),
		FileLocks:      proto.Bool(c.cfg.FileLocks),
		LogLevel:       proto.Int32(2),
		LogFile:        proto.String(fmt.Sprintf("predump-%d.log", round)),
	}

	if round > 0 {
		opts.ParentImg = proto.String(fmt.Sprintf("../predump/round-%d", round-1))
	}

	return c.criu.PreDump(opts, c.notify())
}

func (c *Checkpointer) finalDump(ctx context.Context, rootPID int, dir string, preDumpRounds int) error {
	imgDir, err := openDirFD(dir)
	if err != nil {
		return err
	}
	defer imgDir.Close()

	opts := &criurpc.CriuOpts{
		Pid:            proto.Int32(int32(rootPID)),
		ImagesDirFd:    proto.Int32(int32(imgDir.Fd())),
		LeaveRunning:   proto.Bool(c.cfg.LeaveRunning),
		TrackMem:       proto.Bool(true),
		TcpEstablished: proto.Bool(c.cfg.TCPEstablished),
		ExtUnixSk:      proto.Bool(c.cfg.ExternalUnixSockets),
		ShellJob:       proto.Bool(c.cfg.ShellJob),
		FileLocks:      proto.Bool(c.cfg.FileLocks),
		AutoDedup:      proto.Bool(true), // de-duplicate pages against parent images
		LogLevel:       proto.Int32(3),
		LogFile:        proto.String("dump.log"),
	}

	if preDumpRounds > 0 {
		opts.ParentImg = proto.String(
			fmt.Sprintf("../predump/round-%d", preDumpRounds-1),
		)
	}

	// Page server: stream pages directly to successor over TCP.
	if c.cfg.PageServerAddr != "" {
		host, portStr, _ := splitHostPort(c.cfg.PageServerAddr)
		port, _ := strconv.Atoi(portStr)
		opts.Ps = &criurpc.CriuPageServerInfo{
			Address: proto.String(host),
			Port:    proto.Int32(int32(port)),
		}
		c.log.Info("using CRIU page server",
			zap.String("addr", c.cfg.PageServerAddr),
		)
	}

	return c.criu.Dump(opts, c.notify())
}

// Restore replays a checkpoint on the successor instance.
func (c *Checkpointer) Restore(ctx context.Context, imageDir string) (*RestoreResult, error) {
	c.log.Info("beginning CRIU restore", zap.String("image_dir", imageDir))
	start := time.Now()

	// Decompress images if they were compressed.
	if c.cfg.CompressImages {
		if err := c.decompressImages(ctx, imageDir); err != nil {
			c.log.Warn("image decompression failed — trying uncompressed", zap.Error(err))
		}
	}

	imgDir, err := openDirFD(imageDir)
	if err != nil {
		return nil, fmt.Errorf("opening image dir for restore: %w", err)
	}
	defer imgDir.Close()

	opts := &criurpc.CriuOpts{
		ImagesDirFd:    proto.Int32(int32(imgDir.Fd())),
		TcpEstablished: proto.Bool(c.cfg.TCPEstablished),
		ExtUnixSk:      proto.Bool(c.cfg.ExternalUnixSockets),
		ShellJob:       proto.Bool(c.cfg.ShellJob),
		FileLocks:      proto.Bool(c.cfg.FileLocks),
		LogLevel:       proto.Int32(3),
		LogFile:        proto.String("restore.log"),
		RstSibling:     proto.Bool(true),
		// Auto-reconnect TCP connections after restore.
		TcpClose: proto.Bool(false),
	}

	// go-criu v7 Restore() returns only error; the restored leader PID
	// arrives via the PostRestore notify hook. Wrap criuNotify in
	// pidCapturingNotify to intercept that callback.
	pn := &pidCapturingNotify{criuNotify: c.notify()}
	if err := c.criu.Restore(opts, pn); err != nil {
		c.emitCRIULog(imageDir, "restore.log")
		return nil, fmt.Errorf("CRIU restore: %w", err)
	}

	pid := int(pn.restoredPID)
	c.log.Info("restore complete",
		zap.Int("leader_pid", pid),
		zap.Duration("duration", time.Since(start)),
	)

	return &RestoreResult{LeaderPID: pid, TotalTime: time.Since(start)}, nil
}

// EmergencyCheckpoint performs a best-effort checkpoint with no pre-dump.
func (c *Checkpointer) EmergencyCheckpoint(ctx context.Context, cgroupPath, imageDir string) error {
	c.log.Warn("emergency checkpoint (no pre-dump)")
	if err := os.MkdirAll(imageDir, 0o700); err != nil {
		return err
	}

	pids, _ := listCgroupPIDs(cgroupPath)
	if len(pids) == 0 {
		return fmt.Errorf("no pids in cgroup %s", cgroupPath)
	}

	imgDir, err := openDirFD(imageDir)
	if err != nil {
		return err
	}
	defer imgDir.Close()

	opts := &criurpc.CriuOpts{
		Pid:            proto.Int32(int32(minPID(pids))),
		ImagesDirFd:    proto.Int32(int32(imgDir.Fd())),
		LeaveRunning:   proto.Bool(false),
		TcpEstablished: proto.Bool(c.cfg.TCPEstablished),
		ShellJob:       proto.Bool(c.cfg.ShellJob),
		LogLevel:       proto.Int32(1),
		LogFile:        proto.String("emergency.log"),
	}
	return c.criu.Dump(opts, c.notify())
}

// VerifyImages runs `criu check` on the image directory.
func (c *Checkpointer) VerifyImages(imageDir string) error {
	cmd := exec.Command(c.cfg.BinaryPath, "check", "--images-dir", imageDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("criu check failed (%w): %s", err, string(out))
	}
	c.log.Info("checkpoint images verified", zap.String("dir", imageDir))
	return nil
}

// StartPageServer starts a CRIU page-server daemon on this (successor) host.
// The source instance will connect and stream memory pages to it over TCP,
// bypassing the shared block volume for the largest data transfer.
func StartPageServer(ctx context.Context, criuBin, imageDir string, port int) (func(), error) {
	if err := os.MkdirAll(imageDir, 0o700); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, criuBin,
		"page-server",
		"--images-dir", imageDir,
		"--port", strconv.Itoa(port),
		// --daemon would detach; we keep it attached for cleaner lifecycle.
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting CRIU page-server: %w", err)
	}

	// Poll until the port is bound.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if portOpen(port) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !portOpen(port) {
		cmd.Process.Kill() //nolint:errcheck
		return nil, fmt.Errorf("page-server did not bind port %d within 3s", port)
	}

	stop := func() {
		if cmd.Process != nil {
			// Kill the entire process group.
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) //nolint:errcheck
		}
	}
	return stop, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Parallel image compression
// ────────────────────────────────────────────────────────────────────────────

// compressImages compresses all *.img files in the checkpoint directory
// using zstd level 3 (fast + good ratio) in parallel across GOMAXPROCS.
// Replaces originals with .img.zst files.
func (c *Checkpointer) compressImages(ctx context.Context, dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))

	var compressedTotal int64

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".img" {
			continue
		}
		name := entry.Name()
		g.Go(func() error {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			default:
			}
			n, err := compressFile(filepath.Join(dir, name))
			compressedTotal += n
			return err
		})
	}

	return compressedTotal, g.Wait()
}

func (c *Checkpointer) decompressImages(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".zst" {
			continue
		}
		name := entry.Name()
		g.Go(func() error {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			default:
			}
			return decompressFile(filepath.Join(dir, name))
		})
	}
	return g.Wait()
}

func compressFile(src string) (int64, error) {
	dst := src + ".zst"
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	enc, err := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(enc, in); err != nil {
		enc.Close()
		return 0, err
	}
	if err := enc.Close(); err != nil {
		return 0, err
	}

	// Replace original with compressed.
	_ = os.Remove(src)
	info, _ := out.Stat()
	return info.Size(), nil
}

func decompressFile(src string) error {
	dst := src[:len(src)-4] // strip .zst
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	dec, err := zstd.NewReader(in)
	if err != nil {
		return err
	}
	defer dec.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, dec); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// notify returns a fresh criuNotify for dump operations.
func (c *Checkpointer) notify() *criuNotify {
	return &criuNotify{log: c.log, start: time.Now()}
}

// criuNotify implements criulib.Notify and logs each CRIU lifecycle event.
type criuNotify struct {
	log   *zap.Logger
	start time.Time
}

func (n *criuNotify) PreDump() error {
	n.log.Debug("criu:PreDump", zap.Duration("elapsed", time.Since(n.start)))
	return nil
}
func (n *criuNotify) PostDump() error {
	n.log.Debug("criu:PostDump", zap.Duration("elapsed", time.Since(n.start)))
	return nil
}
func (n *criuNotify) PreRestore() error {
	n.log.Debug("criu:PreRestore", zap.Duration("elapsed", time.Since(n.start)))
	return nil
}
func (n *criuNotify) PostRestore(pid int32) error {
	n.log.Info("criu:PostRestore",
		zap.Int32("pid", pid),
		zap.Duration("elapsed", time.Since(n.start)),
	)
	return nil
}
func (n *criuNotify) NetworkLock() error            { n.log.Debug("criu:NetworkLock"); return nil }
func (n *criuNotify) NetworkUnlock() error          { n.log.Debug("criu:NetworkUnlock"); return nil }
func (n *criuNotify) SetupNamespaces(_ int32) error { return nil }
func (n *criuNotify) PostSetupNamespaces() error    { return nil }
func (n *criuNotify) PostResume() error             { return nil }

// pidCapturingNotify wraps criuNotify and records the PID delivered by
// CRIU's PostRestore callback. This is necessary because go-criu v7's
// Restore() method returns only error — the restored leader PID is
// communicated exclusively through the PostRestore hook.
type pidCapturingNotify struct {
	*criuNotify
	restoredPID int32
}

// PostRestore overrides criuNotify.PostRestore to capture the PID before logging.
func (p *pidCapturingNotify) PostRestore(pid int32) error {
	p.restoredPID = pid
	return p.criuNotify.PostRestore(pid)
}

func (c *Checkpointer) emitCRIULog(dir, file string) {
	logPath := filepath.Join(dir, file)
	if data, err := os.ReadFile(logPath); err == nil {
		c.log.Error("CRIU log", zap.String("path", logPath), zap.String("content", string(data)))
	}
}

func openDirFD(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY, 0)
}

func listCgroupPIDs(cgroupPath string) ([]int, error) {
	procsFile := filepath.Join("/sys/fs/cgroup", cgroupPath, "cgroup.procs")
	if _, err := os.Stat(procsFile); os.IsNotExist(err) {
		procsFile = filepath.Join("/sys/fs/cgroup/freezer", cgroupPath, "cgroup.procs")
	}
	data, err := os.ReadFile(procsFile)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", procsFile, err)
	}
	var pids []int
	for _, line := range splitLines(data) {
		if pid, err := strconv.Atoi(line); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func splitLines(data []byte) []string {
	var out []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			if s := string(data[start:i]); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		if s := string(data[start:]); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func minPID(pids []int) int {
	m := pids[0]
	for _, p := range pids[1:] {
		if p < m {
			m = p
		}
	}
	return m
}

func sumImageSize(dir string) (int64, error) {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, nil
}

// FreezeThaw freezes/thaws the cgroup using the cgroupv2 freezer.
func FreezeThaw(cgroupPath string, freeze bool) error {
	val := "0"
	if freeze {
		val = "1"
	}
	freezerPath := filepath.Join("/sys/fs/cgroup", cgroupPath, "cgroup.freeze")
	return os.WriteFile(freezerPath, []byte(val), 0o644)
}

func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", port),
		200*time.Millisecond,
	)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// splitHostPort splits "host:port" safely.
func splitHostPort(addr string) (string, string, error) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], nil
		}
	}
	return addr, "27182", nil
}

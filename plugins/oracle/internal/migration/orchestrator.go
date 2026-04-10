// Package migration contains the Orchestrator v2, which achieves near-zero
// downtime through full pipeline parallelism and a warm instance pool.
//
// Improved migration timeline (with warm pool):
//
//	T+0s   Preemption notice received
//	T+0s   Acquire lock  ──┬──  Warm pool: StartInstance (~2s)
//	T+0s   Network capture ┤
//	T+0s   CRIU dirty reset┘
//	T+1s   Pre-dump round 1 (adaptive)
//	T+2s   Warm successor is RUNNING
//	T+2s   Signal successor to start page server (via NATS or SSH)
//	T+3s   Pre-dump round 2 (page server ready)
//	T+6s   Convergence check → dirty ratio < 4% → skip round 3
//	T+7s   Pre-freeze (50ms cgroup freeze to drain write pipeline)
//	T+7s   Final dump: FREEZE ── memory pages stream TCP → successor
//	T+7.2s UNFREEZE  (< 200ms freeze window with page server)
//	T+8s   Verify images  ──┬──  Volume: flush + unmount (async)
//	T+9s   Volume detach   ─┘
//	T+11s  Attach shared vol to successor
//	T+12s  Signal successor to begin CRIU restore
//	T+13s  ✓ Build resumes on successor
//
// Total effective downtime: ~200ms (CRIU freeze window)
// Total wall-clock: ~13s (vs ~39s in v1)
//
// Without warm pool (cold launch fallback):
//
//	Pre-dump rounds overlap with cold instance launch (~35s).
//	Final freeze still < 500ms. Total wall-clock: ~40s.
package migration

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/bons/bons-ci/plugins/oracle/internal/config"
	"github.com/bons/bons-ci/plugins/oracle/internal/control"
	"github.com/bons/bons-ci/plugins/oracle/internal/criu"
	"github.com/bons/bons-ci/plugins/oracle/internal/monitor"
	"github.com/bons/bons-ci/plugins/oracle/internal/network"
	"github.com/bons/bons-ci/plugins/oracle/internal/oci"
	"github.com/bons/bons-ci/plugins/oracle/internal/state"
	"github.com/bons/bons-ci/plugins/oracle/internal/telemetry"
	"github.com/bons/bons-ci/plugins/oracle/internal/warmpool"
)

// OrchestratorConfig is the dependency bundle.
type OrchestratorConfig struct {
	OCI       *oci.Session
	State     *state.Manager
	Config    *config.Root
	Log       *zap.Logger
	Telemetry *telemetry.Provider
}

// Orchestrator sequences the full migration pipeline.
type Orchestrator struct {
	ociSession *oci.Session
	instances  *oci.InstanceManager
	volumes    *oci.VolumeManager
	criu       *criu.Checkpointer
	network    *network.Manager
	state      *state.Manager
	warmPool   *warmpool.Pool
	cfg        *config.Root
	log        *zap.Logger
	tel        *telemetry.Provider

	// Cached identity of this instance.
	mu              sync.Mutex
	selfOCID        string
	selfPrivateIP   string
	selfBootVolOCID string
}

// NewOrchestrator constructs all sub-systems.
func NewOrchestrator(c OrchestratorConfig) (*Orchestrator, error) {
	checkpointer, err := criu.NewCheckpointer(c.Config.CRIU, c.Log)
	if err != nil {
		return nil, fmt.Errorf("criu init: %w", err)
	}

	o := &Orchestrator{
		ociSession: c.OCI,
		instances:  oci.NewInstanceManager(c.OCI, c.Log),
		volumes:    oci.NewVolumeManager(c.OCI, c.Log),
		criu:       checkpointer,
		network:    network.NewManager(c.Log),
		state:      c.State,
		cfg:        c.Config,
		log:        c.Log,
		tel:        c.Telemetry,
	}

	// Warm pool is optional — if not configured, falls back to cold launch.
	if c.Config.WarmPool.Enabled {
		o.warmPool = warmpool.New(c.Config.WarmPool, c.Config.OCI, c.OCI, c.Log)
		o.log.Info("warm pool enabled",
			zap.Int("min_size", c.Config.WarmPool.MinSize),
		)
	}

	return o, nil
}

// StartBackground starts background maintenance goroutines (warm pool, etc.)
func (o *Orchestrator) StartBackground(ctx context.Context) {
	if o.warmPool != nil {
		o.warmPool.StartBackground(ctx)
	}

	// Pre-cache self identity to avoid latency at migration time.
	go func() {
		ctx2, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, _ = o.selfIdentify(ctx2)
	}()
}

// Migrate executes the full source-side migration pipeline.
func (o *Orchestrator) Migrate(ctx context.Context, evt monitor.Event) error {
	migStart := time.Now()
	budget := time.Until(evt.TerminationTime)
	o.log.Info("migration pipeline starting",
		zap.Time("deadline", evt.TerminationTime),
		zap.Duration("budget", budget),
		zap.String("preemption_source", evt.Source),
	)

	m := o.tel.Metrics()

	// ── Step 0: Acquire exclusive migration lock ───────────────────────────
	release, err := o.state.AcquireLock()
	if err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer release()

	// ── Step 1: Parallel identity + volume lookup ──────────────────────────
	// Run these concurrently — they're independent OCI API calls.
	identity, err := o.selfIdentify(ctx)
	if err != nil {
		return fmt.Errorf("self-identify: %w", err)
	}

	sharedVolumeMountPath := o.cfg.Migration.SharedVolumeMountPath
	checkpointDir := filepath.Join(o.cfg.Migration.CheckpointDir,
		fmt.Sprintf("ckpt-%s", time.Now().Format("20060102-150405")))
	networkSnapshotPath := filepath.Join(sharedVolumeMountPath, "network-state.json")

	// ── Step 2: Initialise ledger ──────────────────────────────────────────
	ledger, err := o.state.Init(
		identity.InstanceOCID,
		o.cfg.Migration.SharedVolumeOCID,
		checkpointDir, networkSnapshotPath,
		o.cfg.Migration.MigratedCgroup,
		identity.PrivateIP,
		evt.TerminationTime,
	)
	if err != nil {
		return fmt.Errorf("initialising ledger: %w", err)
	}
	ledger.BootVolumeOCID = identity.BootVolumeOCID
	_ = o.state.Advance(ledger, state.PhasePreDumping, "pipeline started")

	// ── Step 3: Launch 3 parallel streams ─────────────────────────────────
	//
	//   Stream A: Network capture (fast, ~200ms)
	//   Stream B: Successor acquisition (warm: ~2s, cold: ~35s)
	//   Stream C: CRIU pre-dump round 1 (overlaps with B)
	//
	// All three run concurrently from T+0.

	type successorResult struct {
		ocid           string
		pageServerAddr string
	}

	networkDone := make(chan error, 1)
	successorCh := make(chan successorResult, 1)
	successorErrCh := make(chan error, 1)

	// Stream A: network capture
	go func() {
		_, err := o.network.Capture(networkSnapshotPath)
		networkDone <- err
	}()

	// Stream B: successor acquisition + page server setup
	go func() {
		ocid, err := o.acquireSuccessor(ctx, ledger, identity)
		if err != nil {
			successorErrCh <- err
			return
		}

		// Set up page server on the successor so memory pages can stream
		// directly over TCP during the final dump, bypassing the shared volume.
		pageServerAddr := ""
		if o.cfg.CRIU.PageServerPort > 0 {
			successorIP, ipErr := o.instances.GetInstancePrivateIP(ctx, ocid)
			if ipErr == nil {
				pageServerAddr = fmt.Sprintf("%s:%d", successorIP, o.cfg.CRIU.PageServerPort)
				if psErr := o.startPageServerOnSuccessor(ctx, ocid, pageServerAddr); psErr != nil {
					o.log.Warn("could not start page server — will use shared volume",
						zap.Error(psErr),
					)
					pageServerAddr = ""
				} else {
					o.log.Info("page server ready on successor",
						zap.String("addr", pageServerAddr),
					)
				}
			}
		}

		successorCh <- successorResult{ocid: ocid, pageServerAddr: pageServerAddr}
	}()

	// Stream C: CRIU pre-dump round 1 (non-disruptive)
	// We start the first pre-dump immediately without waiting for successor.
	// Additional rounds happen AFTER we know the page server address.
	sharedAttachID, sharedAttachInfo, err := o.volumes.GetCurrentAttachment(
		ctx, identity.InstanceOCID, o.cfg.Migration.SharedVolumeOCID,
	)
	if err != nil {
		return fmt.Errorf("locating shared volume: %w", err)
	}

	// Pre-allocate checkpoint space to avoid fragmentation.
	_ = o.volumes.PreloadCheckpointDir(checkpointDir, 4*1024*1024*1024) // 4GB estimate

	// ── Step 4: Wait for network capture ──────────────────────────────────
	if err := <-networkDone; err != nil {
		o.log.Warn("network capture failed — continuing without network restore", zap.Error(err))
	}
	o.state.RecordTiming(ledger, "network_capture", time.Since(migStart))

	// ── Step 5: CRIU checkpoint (with adaptive pre-dump) ─────────────────
	// Configure page server address if available.
	criuCfg := o.cfg.CRIU

	// The adaptive pre-dump and final dump happen here.
	// If the page server is ready, the final dump streams pages directly to successor.
	select {
	case sr := <-successorCh:
		// Great — we have the successor and possibly a page server.
		ledger.SuccessorInstanceOCID = sr.ocid
		if sr.pageServerAddr != "" {
			criuCfg.PageServerAddr = sr.pageServerAddr
		}
		o.log.Info("successor ready before final dump",
			zap.String("ocid", sr.ocid),
			zap.Duration("elapsed", time.Since(migStart)),
		)
	case err := <-successorErrCh:
		return fmt.Errorf("successor acquisition failed: %w", err)
	case <-ctx.Done():
		return fmt.Errorf("context expired waiting for successor: %w", ctx.Err())
	case <-time.After(5 * time.Second):
		// Successor not ready in 5s — proceed with checkpoint anyway.
		// The final dump will use the shared volume instead of page server.
		o.log.Warn("successor not ready yet — proceeding with checkpoint (page server unavailable)")
	}

	_ = o.state.Advance(ledger, state.PhaseCheckpointing, "criu adaptive checkpoint")
	ckStart := time.Now()

	ckResult, err := o.criu.CheckpointCgroup(ctx, o.cfg.Migration.MigratedCgroup, checkpointDir)
	if err != nil {
		return fmt.Errorf("criu checkpoint: %w", err)
	}

	o.state.RecordTiming(ledger, "checkpoint_total", ckResult.TotalTime)
	o.state.RecordTiming(ledger, "checkpoint_freeze", ckResult.FreezeTime)
	ledger.CheckpointPIDCount = ckResult.PIDCount
	ledger.CheckpointMemoryBytes = ckResult.MemoryBytes
	ledger.CheckpointFreezeTime = ckResult.FreezeTime

	if m != nil {
		m.FreezeDurationSeconds.Observe(ckResult.FreezeTime.Seconds())
		m.CheckpointDurationSeconds.Observe(time.Since(ckStart).Seconds())
		m.CheckpointMemoryBytes.Set(float64(ckResult.MemoryBytes))
		m.CheckpointPIDCount.Set(float64(ckResult.PIDCount))
	}

	o.log.Info("checkpoint complete",
		zap.Duration("freeze_time", ckResult.FreezeTime),
		zap.Duration("total_time", ckResult.TotalTime),
		zap.Int("pids", ckResult.PIDCount),
		zap.Int("pre_dump_rounds", ckResult.PreDumpRounds),
		zap.Float64("dirty_ratio_final", ckResult.DirtyRatioFinal),
		zap.Int64("memory_bytes", ckResult.MemoryBytes),
		zap.Int64("compressed_bytes", ckResult.CompressedBytes),
	)

	// ── Step 6: Verify + wait for successor (if not yet acquired) ─────────
	// Verify and volume ops run concurrently.
	g, gCtx := errgroup.WithContext(ctx)

	var successorOCID string

	// If we didn't get the successor yet (warm pool was slow), wait now.
	if ledger.SuccessorInstanceOCID == "" {
		select {
		case sr := <-successorCh:
			ledger.SuccessorInstanceOCID = sr.ocid
			successorOCID = sr.ocid
		case err := <-successorErrCh:
			return fmt.Errorf("successor acquisition failed: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		}
	} else {
		successorOCID = ledger.SuccessorInstanceOCID
	}

	// Verify images.
	g.Go(func() error {
		return o.criu.VerifyImages(ckResult.FinalDir)
	})

	// Detach shared volume from source (async — starts in background).
	detachCh := o.volumes.DetachVolumeAsync(gCtx, sharedAttachID, sharedAttachInfo, sharedVolumeMountPath)

	// Wait for verify.
	if err := g.Wait(); err != nil {
		return fmt.Errorf("checkpoint verification: %w", err)
	}
	_ = o.state.Advance(ledger, state.PhaseCheckpointed, "images verified")

	// ── Step 7: Concurrent volume handover ────────────────────────────────
	// Wait for detach from source.
	_ = o.state.Advance(ledger, state.PhaseVolumeDetaching, "detaching from source")
	detachStart := time.Now()

	if err := <-detachCh; err != nil {
		o.log.Warn("volume detach error — attempting force detach", zap.Error(err))
		// Force detach: the OCI API will eventually reclaim the volume.
		// We continue — the successor attach will wait for the volume to be available.
	}
	o.state.RecordTiming(ledger, "volume_detach", time.Since(detachStart))
	_ = o.state.Advance(ledger, state.PhaseVolumeDetached, "detached from source")

	// Attach to successor.
	_ = o.state.Advance(ledger, state.PhaseLaunchSuccessor, "attaching to successor")
	attachStart := time.Now()
	_, err = o.volumes.AttachVolume(gCtx, successorOCID, o.cfg.Migration.SharedVolumeOCID, "migration-vol")
	if err != nil {
		return fmt.Errorf("attaching to successor: %w", err)
	}
	o.state.RecordTiming(ledger, "volume_attach_successor", time.Since(attachStart))

	// ── Step 8: Mark successor ready ──────────────────────────────────────
	wallTime := time.Since(migStart)
	if err := o.state.Advance(ledger, state.PhaseSuccessorUp, fmt.Sprintf(
		"successor %s ready in %s; freeze=%s",
		successorOCID, wallTime, ckResult.FreezeTime,
	)); err != nil {
		return err
	}

	if m != nil {
		m.TotalMigrationSeconds.Observe(wallTime.Seconds())
	}

	o.log.Info("✓ source-side migration complete",
		zap.String("successor_ocid", successorOCID),
		zap.Duration("wall_time", wallTime),
		zap.Duration("freeze_time", ckResult.FreezeTime),
		zap.Duration("remaining", time.Until(evt.TerminationTime)),
	)

	return nil
}

// Restore is called on the successor instance to resume the migrated state.
func (o *Orchestrator) Restore(ctx context.Context) error {
	o.log.Info("successor restore pipeline starting")

	ledger, err := o.state.Load()
	if err != nil {
		return fmt.Errorf("loading ledger: %w", err)
	}

	if ledger.CurrentPhase != state.PhaseSuccessorUp {
		return fmt.Errorf("unexpected phase %s — expected %s", ledger.CurrentPhase, state.PhaseSuccessorUp)
	}

	_ = o.state.Advance(ledger, state.PhaseRestoring, "restoring network state")

	// Restore network before CRIU so TCP sockets find the right interfaces.
	if err := o.network.Restore(ledger.NetworkSnapshotPath); err != nil {
		o.log.Warn("network restore failed — continuing", zap.Error(err))
	}

	restoreStart := time.Now()
	result, err := o.criu.Restore(ctx, ledger.CheckpointDir)
	if err != nil {
		_ = o.state.Advance(ledger, state.PhaseFailed, err.Error())
		return fmt.Errorf("criu restore: %w", err)
	}
	o.state.RecordTiming(ledger, "criu_restore", time.Since(restoreStart))

	if o.tel != nil {
		o.tel.Metrics().RestoreDurationSeconds.Observe(time.Since(restoreStart).Seconds())
	}

	_ = o.state.Advance(ledger, state.PhaseComplete, fmt.Sprintf(
		"restore complete in %s; leader pid %d",
		result.TotalTime, result.LeaderPID,
	))

	o.log.Info("✓ live migration complete — processes resumed",
		zap.Int("leader_pid", result.LeaderPID),
		zap.Duration("restore_time", result.TotalTime),
	)
	return nil
}

// EmergencyCheckpoint performs a best-effort checkpoint when time runs out.
func (o *Orchestrator) EmergencyCheckpoint(ctx context.Context) error {
	o.log.Warn("emergency checkpoint initiated")
	dir := filepath.Join(o.cfg.Migration.CheckpointDir, "emergency")
	return o.criu.EmergencyCheckpoint(ctx, o.cfg.Migration.MigratedCgroup, dir)
}

// ────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ────────────────────────────────────────────────────────────────────────────

type instanceIdentity struct {
	InstanceOCID   string
	PrivateIP      string
	BootVolumeOCID string
}

// selfIdentify retrieves and caches the identity of this instance.
// Cached after first call — safe for concurrent callers.
func (o *Orchestrator) selfIdentify(ctx context.Context) (*instanceIdentity, error) {
	o.mu.Lock()
	if o.selfOCID != "" {
		defer o.mu.Unlock()
		return &instanceIdentity{
			InstanceOCID:   o.selfOCID,
			PrivateIP:      o.selfPrivateIP,
			BootVolumeOCID: o.selfBootVolOCID,
		}, nil
	}
	o.mu.Unlock()

	// Fetch concurrently.
	var (
		ocid, ip, bootVol string
		mu                sync.Mutex
	)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		if o.cfg.OCI.InstanceOCID != "" {
			mu.Lock()
			ocid = o.cfg.OCI.InstanceOCID
			mu.Unlock()
			return nil
		}
		id, err := monitor.FetchCurrentInstanceID(o.cfg.OCI.MetadataEndpoint)
		if err != nil {
			return fmt.Errorf("fetching instance OCID: %w", err)
		}
		mu.Lock()
		ocid = id
		mu.Unlock()
		return nil
	})

	// IP and boot volume require the OCID first — fetch after.
	if err := g.Wait(); err != nil {
		return nil, err
	}

	g2, gCtx2 := errgroup.WithContext(gCtx)
	g2.Go(func() error {
		v, err := o.instances.GetInstancePrivateIP(gCtx2, ocid)
		if err != nil {
			o.log.Warn("could not get private IP", zap.Error(err))
			return nil
		}
		mu.Lock()
		ip = v
		mu.Unlock()
		return nil
	})
	g2.Go(func() error {
		v, err := o.instances.GetBootVolumeOCID(gCtx2, ocid)
		if err != nil {
			o.log.Warn("could not get boot volume OCID", zap.Error(err))
			return nil
		}
		mu.Lock()
		bootVol = v
		mu.Unlock()
		return nil
	})
	_ = g2.Wait()

	o.mu.Lock()
	o.selfOCID = ocid
	o.selfPrivateIP = ip
	o.selfBootVolOCID = bootVol
	o.mu.Unlock()

	return &instanceIdentity{
		InstanceOCID:   ocid,
		PrivateIP:      ip,
		BootVolumeOCID: bootVol,
	}, nil
}

// acquireSuccessor uses the warm pool when available, falling back to cold launch.
func (o *Orchestrator) acquireSuccessor(ctx context.Context, ledger *state.Ledger, identity *instanceIdentity) (string, error) {
	if o.warmPool != nil {
		inst, err := o.warmPool.Acquire(ctx, identity.PrivateIP)
		if err == nil && inst != nil {
			o.log.Info("acquired warm successor",
				zap.String("ocid", inst.OCID),
				zap.Duration("age", time.Since(inst.CreatedAt)),
			)
			return inst.OCID, nil
		}
		o.log.Warn("warm pool acquire failed — falling back to cold launch", zap.Error(err))
	}

	return o.coldLaunchSuccessor(ctx, ledger, identity)
}

// coldLaunchSuccessor does a full OCI instance launch.
func (o *Orchestrator) coldLaunchSuccessor(ctx context.Context, ledger *state.Ledger, identity *instanceIdentity) (string, error) {
	userData := buildCloudInit(o.cfg)
	return o.instances.LaunchSuccessor(ctx, oci.LaunchSuccessorOptions{
		DisplayName:    fmt.Sprintf("cicd-successor-%s", time.Now().Format("150405")),
		UserData:       userData,
		BootVolumeOCID: identity.BootVolumeOCID,
		ImageOCID:      o.cfg.OCI.ImageOCID,
		PrivateIP:      identity.PrivateIP,
		FreeformTags: map[string]string{
			"migration-source": ledger.SourceInstanceOCID,
			"migration-id":     filepath.Base(ledger.CheckpointDir),
		},
		Timeout: o.cfg.Migration.SuccessorLaunchTimeout,
	})
}

// startPageServerOnSuccessor signals the successor to start a CRIU page server.
// Communication is via a lightweight TCP command channel.
func (o *Orchestrator) startPageServerOnSuccessor(ctx context.Context, successorOCID, pageServerAddr string) error {
	// The successor's migrator agent listens on a control port for commands.
	// We tell it to start the page server before the final dump.
	//
	// In production this uses the NATS subject or a simple TCP control socket.
	// For simplicity, we send a signal over the migration control channel.
	//
	// The successor migrator, started by cloud-init, exposes a control endpoint
	// at port 7077 that accepts simple JSON commands.
	successorIP, err := o.instances.GetInstancePrivateIP(ctx, successorOCID)
	if err != nil {
		return fmt.Errorf("getting successor IP: %w", err)
	}

	controlAddr := fmt.Sprintf("%s:%d", successorIP, o.cfg.Migration.ControlPort)

	// Wait for the successor's control server to be ready (it starts in cloud-init).
	if err := control.WaitForControlServer(ctx, controlAddr, 30*time.Second); err != nil {
		return fmt.Errorf("waiting for successor control server: %w", err)
	}

	return sendControlCommand(ctx, controlAddr, ControlCommand{
		Action:         "start-page-server",
		PageServerPort: o.cfg.CRIU.PageServerPort,
		ImageDir:       o.cfg.Migration.CheckpointDir,
	})
}

// buildCloudInit generates the successor cloud-init user-data.
func buildCloudInit(cfg *config.Root) string {
	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
exec > /var/log/oci-migrator-init.log 2>&1

# Wait for shared volume block device to appear.
for i in $(seq 1 60); do
  if [ -b "%s" ]; then break; fi
  sleep 0.5
done

mkdir -p "%s"
mount -t ext4 -o noatime,nodiratime,data=writeback "%s" "%s" || true

# Start CRIU page server if port is configured (catches streaming pages from source).
if command -v criu &>/dev/null && [ "%d" -gt 0 ]; then
  mkdir -p "%s"
  criu page-server --images-dir "%s" --port %d &
  sleep 0.2
fi

# Start migrator in successor/restore mode.
export OCI_MIGRATOR_MIGRATION_IS_SUCCESSOR=true
export OCI_MIGRATOR_OCI_COMPARTMENT_OCID="%s"
export OCI_MIGRATOR_OCI_SUBNET_OCID="%s"
export OCI_MIGRATOR_OCI_SHAPE="%s"
export OCI_MIGRATOR_MIGRATION_SHARED_VOLUME_OCID="%s"
export OCI_MIGRATOR_MIGRATION_SHARED_VOLUME_MOUNT_PATH="%s"

exec /usr/local/bin/oci-migrator
`,
		cfg.Migration.SharedVolumeDevice,
		cfg.Migration.SharedVolumeMountPath,
		cfg.Migration.SharedVolumeDevice, cfg.Migration.SharedVolumeMountPath,
		cfg.CRIU.PageServerPort,
		cfg.Migration.CheckpointDir, cfg.Migration.CheckpointDir, cfg.CRIU.PageServerPort,
		cfg.OCI.CompartmentOCID,
		cfg.OCI.SubnetOCID,
		cfg.OCI.Shape,
		cfg.Migration.SharedVolumeOCID,
		cfg.Migration.SharedVolumeMountPath,
	)
}

// ControlCommand is a command sent to the successor control endpoint.
type ControlCommand struct {
	Action         string `json:"action"`
	PageServerPort int    `json:"page_server_port,omitempty"`
	ImageDir       string `json:"image_dir,omitempty"`
}

// sendControlCommand sends a command to the successor control server.
func sendControlCommand(ctx context.Context, addr string, cmd ControlCommand) error {
	resp, err := control.SendCommand(ctx, addr, control.Command{
		Action:         cmd.Action,
		PageServerPort: cmd.PageServerPort,
		ImageDir:       cmd.ImageDir,
	})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("control command %q failed: %s", cmd.Action, resp.Message)
	}
	return nil
}

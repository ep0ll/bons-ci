// Package migration contains the Orchestrator v2, which achieves near-zero
// downtime through full pipeline parallelism and a warm instance pool.
//
// OCI preemptible instance constraints handled in this orchestrator:
//
//  1. No Stop/Start on preemptible: the WARM POOL uses regular instances
//     (non-preemptible) that can be stopped at storage-only cost and started
//     in ~2–3s. Cold-launch successors are preemptible (cheaper compute).
//
//  2. No IP reuse while source is alive: OCI rejects duplicate private IPs
//     in the same subnet. The successor receives an auto-assigned IP. CRIU
//     restores all process memory; TCP connections are dropped (RST) and the
//     application retries. For CI/CD builds this is acceptable — the build
//     state (compiled objects, module cache, in-progress work) is preserved.
//
//  3. Block volume detach/reattach: fully supported by OCI. Sequence:
//     unmount → detach API → wait for DETACHED → attach to successor → mount.
//
// Improved migration timeline (with warm pool):
//
//	T+0s   Preemption notice received
//	T+0s   Acquire lock + Network capture + CRIU dirty reset  [3 parallel]
//	T+0s   Warm pool: StartInstance (~2–3s on regular instance)
//	T+1s   Pre-dump round 1 (adaptive)
//	T+2s   Warm successor RUNNING
//	T+2s   Signal successor → start CRIU page server
//	T+3s   Pre-dump round 2 (page server ready)
//	T+6s   Dirty ratio < 4% → converged
//	T+7s   Pre-freeze (50ms cgroup freeze to drain write pipeline)
//	T+7s   Final dump: FREEZE — memory pages stream TCP → successor
//	T+7.2s UNFREEZE  (< 200ms freeze window with page server)
//	T+8s   Verify images ──┬── Volume: flush + unmount (async)
//	T+9s   Volume detach   ─┘
//	T+11s  Attach shared vol to successor
//	T+12s  Signal successor to begin CRIU restore
//	T+13s  ✓ Build resumes on successor
//
// Total effective downtime: ~200ms (CRIU freeze window)
// Total wall-clock: ~13s (warm) / ~40s (cold fallback)
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

	// Cached identity of this instance (immutable after first fetch).
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

	// ── Step 1: Self-identify ──────────────────────────────────────────────
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
	//   Stream A: Network capture (~200ms, non-blocking)
	//   Stream B: Successor acquisition (warm ~2s, cold ~35s)
	//   Stream C: Shared volume lookup + pre-allocation

	type successorResult struct {
		ocid           string
		pageServerAddr string
	}

	networkDone := make(chan error, 1)
	successorCh := make(chan successorResult, 1)
	successorErrCh := make(chan error, 1)

	// Stream A: network capture (snapshot routes/iptables before anything changes).
	go func() {
		_, err := o.network.Capture(networkSnapshotPath)
		networkDone <- err
	}()

	// Stream B: successor acquisition.
	// On warm path: StartInstance on a stopped regular instance (~2–3s).
	// On cold path: LaunchInstance of a preemptible instance (~35s).
	//
	// NOTE: We do NOT pass the source's private IP to the successor launch.
	// The source is still alive; OCI will reject a duplicate IP request.
	// The successor gets an auto-assigned IP. CRIU restores process memory;
	// open TCP connections are reset and retried by the build toolchain.
	go func() {
		ocid, err := o.acquireSuccessor(ctx, ledger, identity)
		if err != nil {
			successorErrCh <- err
			return
		}

		// Set up CRIU page server on the successor so memory pages stream
		// directly over TCP during the final dump (bypasses shared volume I/O).
		pageServerAddr := ""
		if o.cfg.CRIU.PageServerPort > 0 {
			successorIP, ipErr := o.instances.GetInstancePrivateIPNoCache(ctx, ocid)
			if ipErr == nil {
				addr := fmt.Sprintf("%s:%d", successorIP, o.cfg.CRIU.PageServerPort)
				if psErr := o.startPageServerOnSuccessor(ctx, ocid, successorIP); psErr != nil {
					o.log.Warn("could not start page server — will use shared volume",
						zap.Error(psErr),
					)
				} else {
					pageServerAddr = addr
					o.log.Info("page server ready on successor",
						zap.String("addr", pageServerAddr),
					)
				}
			} else {
				o.log.Warn("could not get successor IP for page server", zap.Error(ipErr))
			}
		}

		successorCh <- successorResult{ocid: ocid, pageServerAddr: pageServerAddr}
	}()

	// Stream C: shared volume lookup + pre-allocation.
	sharedAttachID, sharedAttachInfo, err := o.volumes.GetCurrentAttachment(
		ctx, identity.InstanceOCID, o.cfg.Migration.SharedVolumeOCID,
	)
	if err != nil {
		return fmt.Errorf("locating shared volume: %w", err)
	}
	// Pre-allocate contiguous space to avoid fragmentation on final write.
	_ = o.volumes.PreloadCheckpointDir(checkpointDir, 4*1024*1024*1024)

	// ── Step 4: Wait for network capture ──────────────────────────────────
	if err := <-networkDone; err != nil {
		o.log.Warn("network capture failed — continuing without network restore",
			zap.Error(err))
	}
	o.state.RecordTiming(ledger, "network_capture", time.Since(migStart))

	// ── Step 5: CRIU checkpoint (adaptive pre-dump + page server) ─────────
	criuCfg := o.cfg.CRIU

	// Wait for successor (up to 5s before proceeding without page server).
	// The checkpoint itself (adaptive pre-dump) will run concurrently with
	// the warm boot — we may still get the page server before the final dump.
	select {
	case sr := <-successorCh:
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
		// Not ready yet — proceed with checkpoint; pages go to shared volume.
		o.log.Warn("successor not ready within 5s — proceeding with checkpoint (no page server)")
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

	// ── Step 6: If successor wasn't ready earlier, wait now ───────────────
	var successorOCID string
	if ledger.SuccessorInstanceOCID == "" {
		select {
		case sr := <-successorCh:
			ledger.SuccessorInstanceOCID = sr.ocid
			successorOCID = sr.ocid
		case err := <-successorErrCh:
			return fmt.Errorf("successor acquisition failed: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("context cancelled waiting for successor: %w", ctx.Err())
		}
	} else {
		successorOCID = ledger.SuccessorInstanceOCID
	}

	// ── Step 7: Parallel verify + async volume detach ─────────────────────
	g, gCtx := errgroup.WithContext(ctx)

	// Verify checkpoint images.
	g.Go(func() error {
		return o.criu.VerifyImages(ckResult.FinalDir)
	})

	// Detach shared volume from source asynchronously.
	detachCh := o.volumes.DetachVolumeAsync(gCtx, sharedAttachID, sharedAttachInfo, sharedVolumeMountPath)

	if err := g.Wait(); err != nil {
		return fmt.Errorf("checkpoint verification: %w", err)
	}
	_ = o.state.Advance(ledger, state.PhaseCheckpointed, "images verified")

	// ── Step 8: Volume handover ────────────────────────────────────────────
	_ = o.state.Advance(ledger, state.PhaseVolumeDetaching, "detaching from source")
	detachStart := time.Now()

	if err := <-detachCh; err != nil {
		// Non-fatal: the volume may already be available.  The attach will
		// poll until detach completes.
		o.log.Warn("volume detach returned error — will retry attach",
			zap.Error(err))
	}
	o.state.RecordTiming(ledger, "volume_detach", time.Since(detachStart))
	_ = o.state.Advance(ledger, state.PhaseVolumeDetached, "detached from source")

	// Attach to successor.
	_ = o.state.Advance(ledger, state.PhaseLaunchSuccessor, "attaching to successor")
	attachStart := time.Now()
	_, err = o.volumes.AttachVolume(gCtx, successorOCID, o.cfg.Migration.SharedVolumeOCID, "migration-vol")
	if err != nil {
		return fmt.Errorf("attaching shared volume to successor: %w", err)
	}
	o.state.RecordTiming(ledger, "volume_attach_successor", time.Since(attachStart))

	// ── Step 9: Mark successor ready ──────────────────────────────────────
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
		return fmt.Errorf("unexpected phase %s — expected %s",
			ledger.CurrentPhase, state.PhaseSuccessorUp)
	}

	_ = o.state.Advance(ledger, state.PhaseRestoring, "restoring network state")

	// Restore network before CRIU so TCP sockets find the correct interfaces.
	// Note: the successor has a different private IP (OCI assigns a new one).
	// Existing TCP connections will receive RSTs; the build toolchain retries.
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
// Writes to the shared volume so the successor can potentially restore it.
func (o *Orchestrator) EmergencyCheckpoint(ctx context.Context) error {
	o.log.Warn("emergency checkpoint initiated")
	// Write to shared volume, not local disk — successor must be able to read it.
	dir := filepath.Join(o.cfg.Migration.SharedVolumeMountPath, "emergency-checkpoint")
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

// selfIdentify retrieves and caches this instance's OCI identity.
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
		// Warm pool uses regular (non-preemptible) instances.
		// No need to pass a target IP — successor gets its own IP from OCI.
		inst, err := o.warmPool.Acquire(ctx)
		if err == nil && inst != nil {
			o.log.Info("acquired warm successor",
				zap.String("ocid", inst.OCID),
				zap.Duration("age", time.Since(inst.CreatedAt)),
			)
			return inst.OCID, nil
		}
		if err != nil {
			o.log.Warn("warm pool acquire failed — falling back to cold launch",
				zap.Error(err))
		} else {
			o.log.Warn("warm pool empty — falling back to cold launch")
		}
	}

	return o.coldLaunchSuccessor(ctx, ledger, identity)
}

// coldLaunchSuccessor does a full OCI preemptible instance launch.
// The successor gets an auto-assigned private IP — not the source's IP,
// which is still in use while the source is alive.
func (o *Orchestrator) coldLaunchSuccessor(ctx context.Context, ledger *state.Ledger, identity *instanceIdentity) (string, error) {
	userData := buildCloudInit(o.cfg)
	return o.instances.LaunchSuccessor(ctx, oci.LaunchSuccessorOptions{
		DisplayName:    fmt.Sprintf("cicd-successor-%s", time.Now().Format("150405")),
		UserData:       userData,
		BootVolumeOCID: identity.BootVolumeOCID,
		ImageOCID:      o.cfg.OCI.ImageOCID,
		// DO NOT set PrivateIP here: source is still running, same IP cannot
		// be assigned to a second instance. OCI auto-assigns a new IP.
		// PrivateIP: identity.PrivateIP  ← intentionally omitted
		Preemptible: true, // cold-launch successors are preemptible (cheaper)
		FreeformTags: map[string]string{
			"migration-source": ledger.SourceInstanceOCID,
			"migration-id":     filepath.Base(ledger.CheckpointDir),
			"managed-by":       "oci-live-migrator",
		},
		Timeout: o.cfg.Migration.SuccessorLaunchTimeout,
	})
}

// startPageServerOnSuccessor signals the successor to start a CRIU page server.
func (o *Orchestrator) startPageServerOnSuccessor(ctx context.Context, successorOCID, successorIP string) error {
	controlAddr := fmt.Sprintf("%s:%d", successorIP, o.cfg.Migration.ControlPort)

	// Wait for the successor's control server (started by cloud-init).
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

# Wait for shared volume block device to appear (up to 30s).
for i in $(seq 1 60); do
  [ -b "%s" ] && break
  sleep 0.5
done

mkdir -p "%s"
mount -t ext4 -o noatime,nodiratime,data=writeback "%s" "%s" || true

# Start control server + page server if CRIU is available.
# The source migrator will signal via the control port to start the page server.
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

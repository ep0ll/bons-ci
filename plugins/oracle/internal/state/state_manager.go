// Package state manages the migration ledger — a durable JSON document
// written to the shared block volume that records:
//   - Which checkpoint is canonical
//   - Successor instance OCID
//   - Migration history and timings
//   - Health assertions for post-restore verification
//
// The ledger is protected by a POSIX flock so concurrent migrators
// (e.g. two instances both thinking they're the source) cannot corrupt it.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/oracle/internal/config"
)

// MigrationPhase enumerates the stages of a migration.
type MigrationPhase string

const (
	PhaseIdle            MigrationPhase = "idle"
	PhasePreemptNotified MigrationPhase = "preempt_notified"
	PhasePreDumping      MigrationPhase = "pre_dumping"
	PhaseCheckpointing   MigrationPhase = "checkpointing"
	PhaseCheckpointed    MigrationPhase = "checkpointed"
	PhaseVolumeDetaching MigrationPhase = "volume_detaching"
	PhaseVolumeDetached  MigrationPhase = "volume_detached"
	PhaseLaunchSuccessor MigrationPhase = "launch_successor"
	PhaseSuccessorUp     MigrationPhase = "successor_up"
	PhaseRestoring       MigrationPhase = "restoring"
	PhaseComplete        MigrationPhase = "complete"
	PhaseFailed          MigrationPhase = "failed"
)

// Ledger is the durable migration record.
type Ledger struct {
	// SchemaVersion allows future ledger format changes.
	SchemaVersion int `json:"schema_version"`

	// SourceInstanceOCID is the preempted instance.
	SourceInstanceOCID string `json:"source_instance_ocid"`
	// SuccessorInstanceOCID is the newly launched instance.
	SuccessorInstanceOCID string `json:"successor_instance_ocid,omitempty"`

	// CurrentPhase is the last confirmed phase.
	CurrentPhase MigrationPhase `json:"current_phase"`

	// CheckpointDir is the absolute path to CRIU images on the shared volume.
	CheckpointDir string `json:"checkpoint_dir"`

	// NetworkSnapshotPath is the path to the network state file.
	NetworkSnapshotPath string `json:"network_snapshot_path"`

	// BootVolumeOCID to pass to successor launch.
	BootVolumeOCID string `json:"boot_volume_ocid,omitempty"`

	// SharedVolumeOCID and its attachment on successor.
	SharedVolumeOCID       string `json:"shared_volume_ocid"`
	SharedVolumeAttachment string `json:"shared_volume_attachment_ocid,omitempty"`

	// CgroupPath that was checkpointed.
	CgroupPath string `json:"cgroup_path"`

	// TerminationTime is when OCI will forcibly terminate the source.
	TerminationTime time.Time `json:"termination_time"`

	// SourcePrivateIP is the private IP the successor should claim.
	SourcePrivateIP string `json:"source_private_ip"`

	// Timings records durations for each phase for observability.
	Timings map[string]string `json:"timings,omitempty"`

	// History is an append-only log of phase transitions.
	History []PhaseEvent `json:"history,omitempty"`

	// CheckpointStats from CRIU.
	CheckpointPIDCount    int           `json:"checkpoint_pid_count,omitempty"`
	CheckpointMemoryBytes int64         `json:"checkpoint_memory_bytes,omitempty"`
	CheckpointFreezeTime  time.Duration `json:"checkpoint_freeze_time_ns,omitempty"`

	// UpdatedAt tracks the last write time.
	UpdatedAt time.Time `json:"updated_at"`
}

// PhaseEvent records one phase transition.
type PhaseEvent struct {
	Phase     MigrationPhase `json:"phase"`
	Timestamp time.Time      `json:"timestamp"`
	Message   string         `json:"message,omitempty"`
}

// Manager reads and writes the ledger file under a file lock.
type Manager struct {
	cfg config.StateConfig
	log *zap.Logger
}

// NewManager constructs a Manager.
func NewManager(cfg config.StateConfig, log *zap.Logger) (*Manager, error) {
	return &Manager{cfg: cfg, log: log}, nil
}

// Init creates a fresh ledger for a new migration.
func (m *Manager) Init(sourceOCID, sharedVolumeOCID, checkpointDir, networkSnapshotPath, cgroupPath, sourceIP string, terminationTime time.Time) (*Ledger, error) {
	ledger := &Ledger{
		SchemaVersion:       1,
		SourceInstanceOCID:  sourceOCID,
		CurrentPhase:        PhasePreemptNotified,
		CheckpointDir:       checkpointDir,
		NetworkSnapshotPath: networkSnapshotPath,
		SharedVolumeOCID:    sharedVolumeOCID,
		CgroupPath:          cgroupPath,
		TerminationTime:     terminationTime,
		SourcePrivateIP:     sourceIP,
		Timings:             make(map[string]string),
		UpdatedAt:           time.Now(),
	}
	ledger.addHistory(PhasePreemptNotified, "migration initiated")
	return ledger, m.write(ledger)
}

// Load reads and returns the current ledger.
func (m *Manager) Load() (*Ledger, error) {
	data, err := os.ReadFile(m.cfg.LedgerPath)
	if err != nil {
		return nil, fmt.Errorf("reading ledger %s: %w", m.cfg.LedgerPath, err)
	}
	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parsing ledger: %w", err)
	}
	return &l, nil
}

// Advance updates the ledger to a new phase and writes it atomically.
func (m *Manager) Advance(l *Ledger, phase MigrationPhase, message string) error {
	prev := l.CurrentPhase
	l.CurrentPhase = phase
	l.UpdatedAt = time.Now()
	l.addHistory(phase, message)

	if err := m.write(l); err != nil {
		return err
	}
	m.log.Info("migration phase advance",
		zap.String("from", string(prev)),
		zap.String("to", string(phase)),
		zap.String("message", message),
	)
	return nil
}

// RecordTiming records a named duration in the ledger.
func (m *Manager) RecordTiming(l *Ledger, key string, d time.Duration) {
	l.Timings[key] = d.String()
}

// write serialises the ledger to disk atomically via a tmp file + rename.
// This ensures the ledger is never partially written.
func (m *Manager) write(l *Ledger) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling ledger: %w", err)
	}

	tmp := m.cfg.LedgerPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing tmp ledger: %w", err)
	}
	if err := os.Rename(tmp, m.cfg.LedgerPath); err != nil {
		return fmt.Errorf("atomically replacing ledger: %w", err)
	}
	return nil
}

// AcquireLock grabs the advisory flock on the lock file.
// Returns a release function.  Must be called before any migration.
func (m *Manager) AcquireLock() (func(), error) {
	f, err := os.OpenFile(m.cfg.LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", m.cfg.LockPath, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquiring exclusive flock on %s: %w (another migrator may be running)", m.cfg.LockPath, err)
	}

	m.log.Info("migration lock acquired", zap.String("lock_path", m.cfg.LockPath))
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()
		m.log.Info("migration lock released")
	}, nil
}

func (l *Ledger) addHistory(phase MigrationPhase, msg string) {
	l.History = append(l.History, PhaseEvent{
		Phase:     phase,
		Timestamp: time.Now(),
		Message:   msg,
	})
}

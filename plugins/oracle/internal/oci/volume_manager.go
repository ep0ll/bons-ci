// Package oci - volume manager with concurrent attach/detach operations.
//
// Portability: Linux-only syscalls (inotify, fallocate) live in
// volume_manager_linux.go behind a //go:build linux constraint, so this
// package compiles cleanly on macOS for local development.
package oci

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/plugins/oracle/internal/circuit"
)

// VolumeManager handles block volume attachment lifecycle.
type VolumeManager struct {
	session *Session
	cfg     volumeCfg
	log     *zap.Logger
	breaker *circuit.Breaker
}

type volumeCfg struct {
	CompartmentOCID    string
	AvailabilityDomain string
	DetachTimeout      time.Duration
	AttachTimeout      time.Duration
}

// NewVolumeManager constructs a VolumeManager.
func NewVolumeManager(session *Session, log *zap.Logger) *VolumeManager {
	return &VolumeManager{
		session: session,
		cfg: volumeCfg{
			CompartmentOCID:    session.cfg.CompartmentOCID,
			AvailabilityDomain: session.cfg.AvailabilityDomain,
			DetachTimeout:      30 * time.Second,
			AttachTimeout:      60 * time.Second,
		},
		log:     log,
		breaker: circuit.New("oci-blockstorage", circuit.DefaultSettings(), log),
	}
}

// AttachmentInfo holds details of a live volume attachment.
type AttachmentInfo struct {
	AttachmentOCID string
	VolumeOCID     string
	DevicePath     string
	IQN            string
	IPv4           string
	Port           int32
}

// AttachVolume attaches a block volume to an instance and performs the
// OS-level iSCSI login so the block device appears in /dev.
func (v *VolumeManager) AttachVolume(ctx context.Context, instanceOCID, volumeOCID, displayName string) (*AttachmentInfo, error) {
	v.log.Info("attaching block volume",
		zap.String("volume_ocid", volumeOCID),
		zap.String("instance_ocid", instanceOCID),
	)

	// Note: IsPvEncryptionInTransitEnabled is NOT a field on
	// core.AttachIScsiVolumeDetails in OCI SDK v65 — it is set at the instance
	// level via launchOptions.isPvEncryptionInTransitEnabled. In-transit
	// encryption is therefore controlled by the instance launch policy.
	// UseChap provides iSCSI-layer CHAP authentication for the target.
	resp, err := circuit.ExecuteTyped(ctx, v.breaker, func() (core.AttachVolumeResponse, error) {
		return v.session.Compute.AttachVolume(ctx, core.AttachVolumeRequest{
			AttachVolumeDetails: core.AttachIScsiVolumeDetails{
				InstanceId:  &instanceOCID,
				VolumeId:    &volumeOCID,
				DisplayName: &displayName,
				UseChap:     common.Bool(true),
				IsReadOnly:  common.Bool(false),
			},
		})
	})
	if err != nil {
		return nil, fmt.Errorf("AttachVolume API: %w", err)
	}

	attachID := *resp.VolumeAttachment.GetId()
	v.log.Info("volume attachment initiated", zap.String("attachment_ocid", attachID))

	info, err := v.waitForAttached(ctx, attachID, v.cfg.AttachTimeout)
	if err != nil {
		return nil, err
	}

	if err := v.iscsiLogin(ctx, info); err != nil {
		return nil, fmt.Errorf("iSCSI login: %w", err)
	}

	v.log.Info("volume attached and accessible",
		zap.String("device", info.DevicePath),
		zap.String("attachment_ocid", attachID),
	)
	return info, nil
}

// DetachVolumeAsync initiates detach in a goroutine and returns a channel
// that delivers the result. Allows the orchestrator to overlap detach from
// source with attach to successor.
func (v *VolumeManager) DetachVolumeAsync(
	ctx context.Context,
	attachmentOCID string,
	info *AttachmentInfo,
	mountPath string,
) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- v.DetachVolume(ctx, attachmentOCID, info, mountPath) }()
	return ch
}

// DetachVolume syncs, unmounts, iSCSI-logs-out, then calls the OCI detach API.
func (v *VolumeManager) DetachVolume(
	ctx context.Context,
	attachmentOCID string,
	info *AttachmentInfo,
	mountPath string,
) error {
	v.log.Info("detaching block volume", zap.String("attachment_ocid", attachmentOCID))

	_ = exec.Command("sync").Run()

	if err := v.unmount(mountPath); err != nil {
		v.log.Warn("unmount error — continuing", zap.Error(err))
	}

	if info != nil && info.IQN != "" {
		if err := v.iscsiLogout(ctx, info); err != nil {
			v.log.Warn("iSCSI logout error — continuing", zap.Error(err))
		}
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 2 * time.Second
	bo.MaxInterval = 10 * time.Second
	bo.MaxElapsedTime = v.cfg.DetachTimeout

	return backoff.Retry(func() error {
		return v.breaker.Execute(ctx, func() error {
			_, err := v.session.Compute.DetachVolume(ctx, core.DetachVolumeRequest{
				VolumeAttachmentId: &attachmentOCID,
			})
			return err
		})
	}, backoff.WithContext(bo, ctx))
}

// MountVolume mounts a block device with performance-tuned mount options.
func (v *VolumeManager) MountVolume(device, mountPath, fstype string) error {
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		return fmt.Errorf("creating mount point %s: %w", mountPath, err)
	}

	opts := "noatime,nodiratime"
	if fstype == "ext4" {
		// data=writeback: maximises write throughput for large sequential
		// checkpoint page files by relaxing metadata ordering guarantees.
		opts += ",data=writeback"
	}

	cmd := exec.Command("mount", "-t", fstype, "-o", opts, device, mountPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount %s → %s: %w: %s", device, mountPath, err, string(out))
	}
	v.log.Info("volume mounted",
		zap.String("device", device),
		zap.String("mount_path", mountPath),
		zap.String("opts", opts),
	)
	return nil
}

// FormatVolume formats the device as ext4 if no filesystem exists. Idempotent.
func (v *VolumeManager) FormatVolume(device string) error {
	out, _ := exec.Command("blkid", "-o", "value", "-s", "TYPE", device).Output()
	if existing := strings.TrimSpace(string(out)); existing != "" {
		v.log.Info("volume already formatted",
			zap.String("device", device),
			zap.String("fstype", existing),
		)
		return nil
	}

	v.log.Info("formatting volume as ext4", zap.String("device", device))
	cmd := exec.Command("mkfs.ext4", "-F",
		"-E", "lazy_itable_init=0,lazy_journal_init=0",
		"-O", "^has_journal",
		device,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 on %s: %w: %s", device, err, string(out))
	}
	return nil
}

// GetCurrentAttachment returns the attached volume attachment for a given
// instance and volume. Returns an error if the volume is not attached.
func (v *VolumeManager) GetCurrentAttachment(
	ctx context.Context,
	instanceOCID, volumeOCID string,
) (string, *AttachmentInfo, error) {
	resp, err := circuit.ExecuteTyped(ctx, v.breaker, func() (core.ListVolumeAttachmentsResponse, error) {
		return v.session.Compute.ListVolumeAttachments(ctx, core.ListVolumeAttachmentsRequest{
			CompartmentId: &v.cfg.CompartmentOCID,
			InstanceId:    &instanceOCID,
			VolumeId:      &volumeOCID,
		})
	})
	if err != nil {
		return "", nil, fmt.Errorf("listing volume attachments: %w", err)
	}

	for _, att := range resp.Items {
		if att.GetLifecycleState() != core.VolumeAttachmentLifecycleStateAttached {
			continue
		}
		iscsiAtt, ok := att.(core.IScsiVolumeAttachment)
		if !ok {
			return *att.GetId(), &AttachmentInfo{
				AttachmentOCID: *att.GetId(),
				VolumeOCID:     volumeOCID,
			}, nil
		}
		return *att.GetId(), &AttachmentInfo{
			AttachmentOCID: *att.GetId(),
			VolumeOCID:     volumeOCID,
			DevicePath:     strOrEmpty(iscsiAtt.Device),
			IQN:            strOrEmpty(iscsiAtt.Iqn),
			IPv4:           strOrEmpty(iscsiAtt.Ipv4),
			Port:           int32OrZero(iscsiAtt.Port),
		}, nil
	}
	return "", nil, fmt.Errorf("volume %s not attached to %s", volumeOCID, instanceOCID)
}

// ────────────────────────────────────────────────────────────────────────────
// iSCSI helpers
// ────────────────────────────────────────────────────────────────────────────

func (v *VolumeManager) iscsiLogin(ctx context.Context, info *AttachmentInfo) error {
	if info == nil || info.IQN == "" || info.IPv4 == "" {
		v.log.Warn("no iSCSI target info — skipping login (non-iSCSI attachment)")
		return nil
	}

	targetAddr := fmt.Sprintf("%s:%d", info.IPv4, info.Port)
	v.log.Info("iSCSI login",
		zap.String("iqn", info.IQN),
		zap.String("addr", targetAddr),
	)

	if out, err := exec.CommandContext(ctx, "iscsiadm", "-m", "node", "-o", "new",
		"-T", info.IQN, "-p", targetAddr).CombinedOutput(); err != nil {
		return fmt.Errorf("iscsiadm node new: %w: %s", err, string(out))
	}

	// Best-effort tuning — failures are non-fatal.
	tuning := [][]string{
		{"-n", "node.conn[0].timeo.login_timeout", "-v", "10"},
		{"-n", "node.session.queue_depth", "-v", "128"},
		{"-n", "node.session.nr_sessions", "-v", "1"},
	}
	for _, t := range tuning {
		args := append([]string{"-m", "node", "-T", info.IQN, "-p", targetAddr, "-o", "update"}, t...)
		_ = exec.CommandContext(ctx, "iscsiadm", args...).Run()
	}

	if out, err := exec.CommandContext(ctx, "iscsiadm", "-m", "node",
		"-T", info.IQN, "-p", targetAddr, "--login").CombinedOutput(); err != nil {
		return fmt.Errorf("iscsiadm login: %w: %s", err, string(out))
	}

	return v.waitForDevice(ctx, info.DevicePath, 15*time.Second)
}

func (v *VolumeManager) iscsiLogout(ctx context.Context, info *AttachmentInfo) error {
	if info == nil || info.IQN == "" {
		return nil
	}
	targetAddr := fmt.Sprintf("%s:%d", info.IPv4, info.Port)
	out, err := exec.CommandContext(ctx, "iscsiadm", "-m", "node",
		"-T", info.IQN, "-p", targetAddr, "--logout").CombinedOutput()
	if err != nil {
		return fmt.Errorf("iscsiadm logout: %w: %s", err, string(out))
	}
	return nil
}

func (v *VolumeManager) unmount(mountPath string) error {
	if mountPath == "" {
		return nil
	}
	out, err := exec.Command("umount", "-l", mountPath).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "not mounted") ||
			strings.Contains(string(out), "no mount point") {
			return nil // already unmounted — not an error
		}
		return fmt.Errorf("umount %s: %w: %s", mountPath, err, string(out))
	}
	return nil
}

// waitForDevice waits until devicePath appears in /dev.
// Delegates to the platform-specific fast path first (inotify on Linux),
// then falls back to the portable poll loop.
func (v *VolumeManager) waitForDevice(ctx context.Context, devicePath string, timeout time.Duration) error {
	if devicePath == "" {
		return fmt.Errorf("device path is empty")
	}
	if _, err := os.Stat(devicePath); err == nil {
		return nil // already present
	}
	return v.waitForDevicePlatform(ctx, devicePath, timeout)
}

// waitForDevicePoll is the portable poll-based fallback.
func (v *VolumeManager) waitForDevicePoll(ctx context.Context, devicePath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(devicePath); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("device %s did not appear within %s", devicePath, timeout)
			}
		}
	}
}

// waitForAttached polls the OCI API until the attachment reaches ATTACHED.
func (v *VolumeManager) waitForAttached(
	ctx context.Context,
	attachmentOCID string,
	timeout time.Duration,
) (*AttachmentInfo, error) {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 1 * time.Second
	bo.MaxInterval = 8 * time.Second
	bo.MaxElapsedTime = timeout

	var mu sync.Mutex
	var result *AttachmentInfo

	err := backoff.Retry(func() error {
		resp, err := v.session.Compute.GetVolumeAttachment(ctx, core.GetVolumeAttachmentRequest{
			VolumeAttachmentId: &attachmentOCID,
		})
		if err != nil {
			return err
		}

		st := resp.VolumeAttachment.GetLifecycleState()
		v.log.Debug("volume attachment state",
			zap.String("ocid", attachmentOCID),
			zap.String("state", string(st)),
		)

		switch st {
		case core.VolumeAttachmentLifecycleStateAttached:
			iscsiAtt, ok := resp.VolumeAttachment.(core.IScsiVolumeAttachment)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				result = &AttachmentInfo{
					AttachmentOCID: attachmentOCID,
					DevicePath:     strOrEmpty(iscsiAtt.Device),
					IQN:            strOrEmpty(iscsiAtt.Iqn),
					IPv4:           strOrEmpty(iscsiAtt.Ipv4),
					Port:           int32OrZero(iscsiAtt.Port),
				}
			} else {
				result = &AttachmentInfo{AttachmentOCID: attachmentOCID}
			}
			return nil

		case core.VolumeAttachmentLifecycleStateDetached,
			core.VolumeAttachmentLifecycleStateDetaching:
			return backoff.Permanent(fmt.Errorf(
				"attachment %s entered terminal state %s", attachmentOCID, st,
			))

		default:
			return fmt.Errorf("attachment state %s, waiting for ATTACHED", st)
		}
	}, backoff.WithContext(bo, ctx))

	return result, err
}

// ────────────────────────────────────────────────────────────────────────────
// Shared private helpers
// ────────────────────────────────────────────────────────────────────────────

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func int32OrZero(i *int) int32 {
	if i == nil {
		return 0
	}
	return int32(*i)
}

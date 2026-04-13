// Package oci provides typed wrappers around the OCI Go SDK.
//
// Key OCI preemptible-instance constraints:
//
//  1. Preemptible instances CANNOT be stopped and restarted via InstanceAction.
//     They can only be Terminated (with optional boot-volume preservation).
//     The warm pool therefore uses REGULAR (non-preemptible) instances that
//     support Stop → Start in ~2–3 seconds.
//
//  2. Two running instances cannot share the same private IP.  While the
//     source is still alive, the successor receives an auto-assigned IP.
//     CRIU restores process memory; any open TCP connections whose embedded
//     source IP no longer matches will be reset by the kernel and retried by
//     the application.  For CI/CD build workloads (Go build, npm, apt) this
//     is acceptable — only the build cache / compiler state matters.
//
//  3. HTTP/2 multiplexing for all OCI API calls (connection reuse).
//
//  4. Circuit breaker on every API call — fast-fail during OCI degradation.
//
//  5. Jittered exponential backoff on transient failures.
package oci

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/core"
	"go.uber.org/zap"
	"golang.org/x/net/http2"

	"github.com/bons/bons-ci/plugins/oracle/internal/circuit"
	"github.com/bons/bons-ci/plugins/oracle/internal/config"
)

// Session holds authenticated OCI SDK clients.
type Session struct {
	Compute      core.ComputeClient
	Network      core.VirtualNetworkClient
	BlockStorage core.BlockstorageClient
	cfg          config.OCIConfig
	log          *zap.Logger
}

// NewSession constructs OCI clients with HTTP/2 and instance-principal auth.
func NewSession(cfg config.OCIConfig, log *zap.Logger) (*Session, error) {
	var provider common.ConfigurationProvider
	var err error

	if cfg.ConfigFilePath != "" {
		provider, err = common.ConfigurationProviderFromFileWithProfile(
			cfg.ConfigFilePath, cfg.Profile, "",
		)
	} else {
		provider, err = auth.InstancePrincipalConfigurationProvider()
	}
	if err != nil {
		return nil, fmt.Errorf("OCI config provider: %w", err)
	}

	// HTTP/2 transport: connection multiplexing reduces per-call latency
	// by 30–50ms through elimination of TCP handshake overhead.
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	h2Transport := &http2.Transport{TLSClientConfig: tlsCfg}
	httpClient := &http.Client{
		Transport: h2Transport,
		Timeout:   30 * time.Second,
	}

	compute, err := core.NewComputeClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("compute client: %w", err)
	}
	compute.HTTPClient = httpClient

	network, err := core.NewVirtualNetworkClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("network client: %w", err)
	}
	network.HTTPClient = httpClient

	block, err := core.NewBlockstorageClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("block storage client: %w", err)
	}
	block.HTTPClient = httpClient

	log.Info("OCI session established (HTTP/2)",
		zap.String("region", cfg.Region),
		zap.String("compartment", cfg.CompartmentOCID),
	)

	return &Session{
		Compute:      compute,
		Network:      network,
		BlockStorage: block,
		cfg:          cfg,
		log:          log,
	}, nil
}

// InstanceManager provides high-level instance lifecycle operations.
type InstanceManager struct {
	session *Session
	cfg     config.OCIConfig
	log     *zap.Logger
	breaker *circuit.Breaker

	// Cache for self-identity lookups (immutable after first call).
	mu              sync.Mutex
	cachedBootVol   string
	cachedPrivateIP string
}

// NewInstanceManager constructs an InstanceManager.
func NewInstanceManager(session *Session, log *zap.Logger) *InstanceManager {
	return &InstanceManager{
		session: session,
		cfg:     session.cfg,
		log:     log,
		breaker: circuit.New("oci-compute", circuit.DefaultSettings(), log),
	}
}

// LaunchSuccessorOptions contains all parameters for creating a successor VM.
type LaunchSuccessorOptions struct {
	DisplayName    string
	UserData       string
	BootVolumeOCID string
	ImageOCID      string

	// PrivateIP, if set, requests a specific IP from the subnet.
	// IMPORTANT: Only set this AFTER the source instance has terminated and
	// released the IP. Attempting to assign an in-use IP will fail with an
	// OCI API error. Leave empty to let OCI auto-assign a new IP.
	PrivateIP string

	// Preemptible controls whether the instance is launched as a preemptible
	// (spot) instance.
	//
	//   true  — used for cold-launch successors (cheaper, same shape as source).
	//            Preemptible instances CANNOT be stopped/restarted.
	//
	//   false — used for warm-pool instances (regular, stoppable/startable).
	//           Stopped regular instances cost only storage (~$0.003/hour).
	Preemptible  bool
	FreeformTags map[string]string
	Timeout      time.Duration
}

// LaunchSuccessor creates a new instance for the migration successor or warm pool.
func (m *InstanceManager) LaunchSuccessor(ctx context.Context, opts LaunchSuccessorOptions) (string, error) {
	m.log.Info("launching instance",
		zap.String("display_name", opts.DisplayName),
		zap.String("shape", m.cfg.Shape),
		zap.Bool("preemptible", opts.Preemptible),
		zap.Bool("reuse_boot_volume", opts.BootVolumeOCID != ""),
		zap.String("private_ip", opts.PrivateIP),
	)

	var sourceDetails core.InstanceSourceDetails
	if opts.BootVolumeOCID != "" {
		sourceDetails = core.InstanceSourceViaBootVolumeDetails{
			BootVolumeId: &opts.BootVolumeOCID,
		}
	} else {
		sourceDetails = core.InstanceSourceViaImageDetails{
			ImageId: &opts.ImageOCID,
		}
	}

	tags := mergeTags(m.cfg.FreeformTags, opts.FreeformTags, map[string]string{
		"oci-migrator-role": "successor",
		"oci-migrator-ts":   time.Now().UTC().Format(time.RFC3339),
	})

	vnicDetails := core.CreateVnicDetails{
		SubnetId:       &m.cfg.SubnetOCID,
		NsgIds:         m.cfg.NsgOCIDs,
		AssignPublicIp: common.Bool(false),
	}
	// Only set PrivateIP when it's safe (source has already released it).
	// Setting it while the source is alive causes an OCI API conflict error.
	if opts.PrivateIP != "" {
		vnicDetails.PrivateIp = &opts.PrivateIP
	}

	launchDetails := core.LaunchInstanceDetails{
		CompartmentId:      &m.cfg.CompartmentOCID,
		AvailabilityDomain: &m.cfg.AvailabilityDomain,
		DisplayName:        &opts.DisplayName,
		Shape:              &m.cfg.Shape,
		SourceDetails:      sourceDetails,
		CreateVnicDetails:  &vnicDetails,
		FreeformTags:       tags,
	}

	if opts.UserData != "" {
		userDataB64 := base64.StdEncoding.EncodeToString([]byte(opts.UserData))
		launchDetails.Metadata = map[string]string{"user_data": userDataB64}
	}

	if m.cfg.ShapeConfig.OCPUs > 0 {
		launchDetails.ShapeConfig = &core.LaunchInstanceShapeConfigDetails{
			Ocpus:       &m.cfg.ShapeConfig.OCPUs,
			MemoryInGBs: &m.cfg.ShapeConfig.MemoryInGBs,
		}
	}

	// Preemptible config: only for actual migration successors, NOT warm-pool
	// instances. Warm-pool instances must be regular so they can be Stopped
	// and Started — preemptible instances only support Terminate.
	if opts.Preemptible {
		preserveBoot := true
		launchDetails.PreemptibleInstanceConfig = &core.PreemptibleInstanceConfigDetails{
			PreemptionAction: core.TerminatePreemptionAction{
				PreserveBootVolume: &preserveBoot,
			},
		}
	}

	var resp core.LaunchInstanceResponse
	if err := m.breaker.Execute(ctx, func() error {
		var e error
		resp, e = m.session.Compute.LaunchInstance(ctx, core.LaunchInstanceRequest{
			LaunchInstanceDetails: launchDetails,
		})
		return e
	}); err != nil {
		return "", fmt.Errorf("LaunchInstance: %w", err)
	}

	instanceOCID := *resp.Instance.Id
	m.log.Info("instance created, waiting for RUNNING",
		zap.String("ocid", instanceOCID),
		zap.Bool("preemptible", opts.Preemptible),
	)

	if err := m.WaitForState(ctx, instanceOCID, core.InstanceLifecycleStateRunning, opts.Timeout); err != nil {
		return instanceOCID, fmt.Errorf("waiting for instance RUNNING: %w", err)
	}

	m.log.Info("instance RUNNING", zap.String("ocid", instanceOCID))
	return instanceOCID, nil
}

// StopInstance stops a regular instance and waits for STOPPED.
// NOTE: This ONLY works for regular (non-preemptible) instances.
// Preemptible instances cannot be stopped — only terminated.
func (m *InstanceManager) StopInstance(ctx context.Context, instanceOCID string, timeout time.Duration) error {
	m.log.Info("stopping instance", zap.String("ocid", instanceOCID))

	action := core.InstanceActionActionStop
	if err := m.breaker.Execute(ctx, func() error {
		_, err := m.session.Compute.InstanceAction(ctx, core.InstanceActionRequest{
			InstanceId: &instanceOCID,
			Action:     action,
		})
		return err
	}); err != nil {
		return fmt.Errorf("stop InstanceAction: %w", err)
	}

	return m.WaitForState(ctx, instanceOCID, core.InstanceLifecycleStateStopped, timeout)
}

// StartInstance starts a stopped regular instance and waits for RUNNING.
// NOTE: This ONLY works for regular (non-preemptible) instances.
// Preemptible instances cannot be restarted after being stopped.
func (m *InstanceManager) StartInstance(ctx context.Context, instanceOCID string, timeout time.Duration) error {
	m.log.Info("starting warm instance", zap.String("ocid", instanceOCID))

	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = 30 * time.Second
	bo.InitialInterval = 500 * time.Millisecond

	action := core.InstanceActionActionStart
	if err := backoff.Retry(func() error {
		return m.breaker.Execute(ctx, func() error {
			_, err := m.session.Compute.InstanceAction(ctx, core.InstanceActionRequest{
				InstanceId: &instanceOCID,
				Action:     action,
			})
			return err
		})
	}, backoff.WithContext(bo, ctx)); err != nil {
		return fmt.Errorf("start InstanceAction: %w", err)
	}

	return m.WaitForState(ctx, instanceOCID, core.InstanceLifecycleStateRunning, timeout)
}

// TerminatePreempted terminates the source instance preserving its boot volume.
func (m *InstanceManager) TerminatePreempted(ctx context.Context, instanceOCID string) error {
	m.log.Info("terminating preempted instance", zap.String("ocid", instanceOCID))
	preserveBootVolume := true
	return m.breaker.Execute(ctx, func() error {
		_, err := m.session.Compute.TerminateInstance(ctx, core.TerminateInstanceRequest{
			InstanceId:         &instanceOCID,
			PreserveBootVolume: &preserveBootVolume,
		})
		return err
	})
}

// GetInstancePrivateIP returns the primary private IP, using a cache.
func (m *InstanceManager) GetInstancePrivateIP(ctx context.Context, instanceOCID string) (string, error) {
	m.mu.Lock()
	if m.cachedPrivateIP != "" {
		defer m.mu.Unlock()
		return m.cachedPrivateIP, nil
	}
	m.mu.Unlock()

	vnicResp, err := circuit.ExecuteTyped(ctx, m.breaker, func() (core.ListVnicAttachmentsResponse, error) {
		return m.session.Compute.ListVnicAttachments(ctx, core.ListVnicAttachmentsRequest{
			CompartmentId: &m.cfg.CompartmentOCID,
			InstanceId:    &instanceOCID,
		})
	})
	if err != nil {
		return "", fmt.Errorf("listing VNIC attachments: %w", err)
	}
	if len(vnicResp.Items) == 0 {
		return "", fmt.Errorf("no VNICs on instance %s", instanceOCID)
	}

	vnic, err := circuit.ExecuteTyped(ctx, m.breaker, func() (core.GetVnicResponse, error) {
		return m.session.Network.GetVnic(ctx, core.GetVnicRequest{VnicId: vnicResp.Items[0].VnicId})
	})
	if err != nil {
		return "", fmt.Errorf("getting VNIC: %w", err)
	}

	ip := *vnic.PrivateIp
	m.mu.Lock()
	m.cachedPrivateIP = ip
	m.mu.Unlock()

	return ip, nil
}

// GetInstancePrivateIPNoCache fetches the primary private IP without caching.
// Used for successor instances (which are different from self).
func (m *InstanceManager) GetInstancePrivateIPNoCache(ctx context.Context, instanceOCID string) (string, error) {
	vnicResp, err := circuit.ExecuteTyped(ctx, m.breaker, func() (core.ListVnicAttachmentsResponse, error) {
		return m.session.Compute.ListVnicAttachments(ctx, core.ListVnicAttachmentsRequest{
			CompartmentId: &m.cfg.CompartmentOCID,
			InstanceId:    &instanceOCID,
		})
	})
	if err != nil {
		return "", fmt.Errorf("listing VNIC attachments for %s: %w", instanceOCID, err)
	}
	if len(vnicResp.Items) == 0 {
		return "", fmt.Errorf("no VNICs on instance %s", instanceOCID)
	}

	vnic, err := circuit.ExecuteTyped(ctx, m.breaker, func() (core.GetVnicResponse, error) {
		return m.session.Network.GetVnic(ctx, core.GetVnicRequest{VnicId: vnicResp.Items[0].VnicId})
	})
	if err != nil {
		return "", fmt.Errorf("getting VNIC for %s: %w", instanceOCID, err)
	}

	return *vnic.PrivateIp, nil
}

// GetBootVolumeOCID returns the boot volume OCID, using a cache.
func (m *InstanceManager) GetBootVolumeOCID(ctx context.Context, instanceOCID string) (string, error) {
	m.mu.Lock()
	if m.cachedBootVol != "" {
		defer m.mu.Unlock()
		return m.cachedBootVol, nil
	}
	m.mu.Unlock()

	resp, err := circuit.ExecuteTyped(ctx, m.breaker, func() (core.ListBootVolumeAttachmentsResponse, error) {
		return m.session.Compute.ListBootVolumeAttachments(ctx, core.ListBootVolumeAttachmentsRequest{
			CompartmentId:      &m.cfg.CompartmentOCID,
			AvailabilityDomain: &m.cfg.AvailabilityDomain,
			InstanceId:         &instanceOCID,
		})
	})
	if err != nil {
		return "", fmt.Errorf("listing boot volume attachments: %w", err)
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("no boot volume on instance %s", instanceOCID)
	}

	ocid := *resp.Items[0].BootVolumeId
	m.mu.Lock()
	m.cachedBootVol = ocid
	m.mu.Unlock()

	return ocid, nil
}

// WaitForState polls the instance state with jittered exponential backoff.
func (m *InstanceManager) WaitForState(
	ctx context.Context,
	instanceOCID string,
	desired core.InstanceLifecycleStateEnum,
	timeout time.Duration,
) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 2 * time.Second
	bo.MaxInterval = 10 * time.Second
	bo.MaxElapsedTime = timeout
	// Add ±20% jitter to prevent thundering herd on concurrent instance launches.
	bo.RandomizationFactor = 0.2

	return backoff.Retry(func() error {
		resp, err := m.session.Compute.GetInstance(ctx, core.GetInstanceRequest{
			InstanceId: &instanceOCID,
		})
		if err != nil {
			return err
		}

		current := resp.Instance.LifecycleState
		m.log.Debug("instance state poll",
			zap.String("ocid", instanceOCID),
			zap.String("current", string(current)),
			zap.String("desired", string(desired)),
		)

		if current == desired {
			return nil
		}
		if current == core.InstanceLifecycleStateTerminated ||
			current == core.InstanceLifecycleStateTerminating {
			return backoff.Permanent(fmt.Errorf("instance reached terminal state %s", current))
		}
		return fmt.Errorf("instance state %s, want %s", current, desired)
	}, backoff.WithContext(bo, ctx))
}

// ────────────────────────────────────────────────────────────────────────────

func mergeTags(maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// jitter returns d ± 20%.
func jitter(d time.Duration) time.Duration { //nolint:unused
	factor := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * factor)
}

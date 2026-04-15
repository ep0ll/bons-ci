// Package compute provides the OCI Compute VM adapter for the provisioner port.
// It creates VM.Standard.E4.Flex (or any configured Flex shape) instances
// with a cloud-init script that installs and registers the GitHub Actions runner.
package compute

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/provisioner"
	"github.com/rs/zerolog"
)

// compile-time interface check
var _ provisioner.Provisioner = (*OCIComputeProvisioner)(nil)

// OCIComputeProvisioner creates and terminates OCI Compute instances as runner hosts.
type OCIComputeProvisioner struct {
	// ociComputeClient wraps the OCI Compute SDK client.
	// Typed as interface so the package compiles without network access to OCI SDK proxy.
	ociComputeClient ociComputeClient
	logger           zerolog.Logger
}

// ociComputeClient is the minimal OCI Compute SDK surface needed by this adapter.
type ociComputeClient interface {
	LaunchInstance(ctx context.Context, req LaunchInstanceRequest) (*LaunchInstanceResponse, error)
	GetInstance(ctx context.Context, instanceID string) (*InstanceDetails, error)
	TerminateInstance(ctx context.Context, instanceID string, preserveBootVolume bool) error
}

// LaunchInstanceRequest mirrors the OCI Compute SDK LaunchInstanceDetails.
type LaunchInstanceRequest struct {
	CompartmentID  string
	DisplayName    string
	Shape          string
	ShapeOCPUs     float32
	ShapeMemoryGB  float32
	SubnetID       string
	ImageID        string
	UserDataBase64 string
	FreeformTags   map[string]string
}

// LaunchInstanceResponse contains the created instance identifier.
type LaunchInstanceResponse struct {
	InstanceID string
}

// InstanceDetails contains OCI instance lifecycle state.
type InstanceDetails struct {
	InstanceID     string
	LifecycleState string // "PROVISIONING" | "RUNNING" | "TERMINATING" | "TERMINATED"
}

// Config holds configuration for the OCI Compute provisioner.
type Config struct {
	// DefaultImageID is the OCI custom image OCID used when RunnerSpec.ImageID is empty.
	DefaultImageID string
	// DefaultShape defaults to "VM.Standard.E4.Flex" when RunnerSpec.Shape is empty.
	DefaultShape string
}

// New constructs an OCIComputeProvisioner.
func New(ociClient ociComputeClient, cfg Config, logger zerolog.Logger) *OCIComputeProvisioner {
	return &OCIComputeProvisioner{
		ociComputeClient: ociClient,
		logger:           logger.With().Str("provisioner", "oci_compute").Logger(),
	}
}

// Type satisfies the provisioner.Provisioner interface.
func (p *OCIComputeProvisioner) Type() string { return "compute" }

// Provision launches an OCI Compute VM and returns its OCID.
// The cloud-init script configures and registers the GitHub Actions runner.
func (p *OCIComputeProvisioner) Provision(ctx context.Context, spec provisioner.RunnerSpec) (string, error) {
	if spec.Shape == "" {
		spec.Shape = "VM.Standard.E4.Flex"
	}

	userData, err := buildCloudInit(spec)
	if err != nil {
		return "", fmt.Errorf("build cloud-init: %w", err)
	}
	userDataB64 := base64.StdEncoding.EncodeToString([]byte(userData))

	tags := buildFreeformTags(spec)

	p.logger.Info().
		Str("tenant_id", spec.TenantID).
		Str("runner_id", spec.RunnerID).
		Str("shape", spec.Shape).
		Msg("launching OCI compute instance")

	resp, err := p.ociComputeClient.LaunchInstance(ctx, LaunchInstanceRequest{
		CompartmentID:  spec.CompartmentID,
		DisplayName:    fmt.Sprintf("runner-%s", spec.RunnerID),
		Shape:          spec.Shape,
		ShapeOCPUs:     spec.OCPUs,
		ShapeMemoryGB:  spec.MemoryGB,
		SubnetID:       spec.SubnetID,
		ImageID:        spec.ImageID,
		UserDataBase64: userDataB64,
		FreeformTags:   tags,
	})
	if err != nil {
		return "", fmt.Errorf("%w: launch instance: %v", provisioner.ErrProvisionFailed, err)
	}

	p.logger.Info().
		Str("tenant_id", spec.TenantID).
		Str("runner_id", spec.RunnerID).
		Str("oci_instance_id", resp.InstanceID).
		Msg("OCI compute instance launched")

	return resp.InstanceID, nil
}

// Terminate stops and deletes an OCI Compute instance. Idempotent.
func (p *OCIComputeProvisioner) Terminate(ctx context.Context, ociResourceID string) error {
	p.logger.Info().Str("oci_instance_id", ociResourceID).Msg("terminating OCI instance")

	if err := p.ociComputeClient.TerminateInstance(ctx, ociResourceID, false); err != nil {
		// 404 is acceptable — instance may already be gone.
		if isNotFound(err) {
			p.logger.Warn().Str("oci_instance_id", ociResourceID).Msg("instance already terminated")
			return nil
		}
		return fmt.Errorf("%w: terminate instance %s: %v", provisioner.ErrTerminateFailed, ociResourceID, err)
	}
	return nil
}

// Describe returns the current lifecycle state of an OCI Compute instance.
func (p *OCIComputeProvisioner) Describe(ctx context.Context, ociResourceID string) (*provisioner.InstanceState, error) {
	details, err := p.ociComputeClient.GetInstance(ctx, ociResourceID)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s", provisioner.ErrResourceNotFound, ociResourceID)
		}
		return nil, fmt.Errorf("describe instance %s: %w", ociResourceID, err)
	}
	return &provisioner.InstanceState{
		OCIResourceID: details.InstanceID,
		State:         details.LifecycleState,
		Running:       details.LifecycleState == "RUNNING",
	}, nil
}

// buildCloudInit renders the cloud-init script that installs and registers the runner.
// The registration token is only present in the user-data — it is never stored in DB.
var cloudInitTmpl = template.Must(template.New("cloud-init").Parse(`#!/bin/bash
set -euo pipefail

# --- GitHub Actions Runner Setup ---
RUNNER_VERSION="2.315.0"
RUNNER_DIR="/opt/actions-runner"
RUNNER_USER="runner"
RUNNER_NAME="byoc-{{.RunnerID}}"
GITHUB_ORG="{{.GitHubOrgName}}"
REG_TOKEN="{{.RegistrationToken}}"
LABELS="{{.LabelsCSV}}"
EPHEMERAL_FLAG="{{.EphemeralFlag}}"

useradd -m -s /bin/bash "$RUNNER_USER" 2>/dev/null || true
mkdir -p "$RUNNER_DIR"
chown "$RUNNER_USER:$RUNNER_USER" "$RUNNER_DIR"

cd "$RUNNER_DIR"
curl -sSLo runner.tar.gz "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
tar xzf runner.tar.gz
chown -R "$RUNNER_USER:$RUNNER_USER" "$RUNNER_DIR"

sudo -u "$RUNNER_USER" ./config.sh \
  --url "https://github.com/${GITHUB_ORG}" \
  --token "${REG_TOKEN}" \
  --name "${RUNNER_NAME}" \
  --labels "${LABELS}" \
  --unattended \
  ${EPHEMERAL_FLAG}

./svc.sh install "$RUNNER_USER"
./svc.sh start
`))

type cloudInitData struct {
	RunnerID          string
	GitHubOrgName     string
	RegistrationToken string
	LabelsCSV         string
	EphemeralFlag     string
}

func buildCloudInit(spec provisioner.RunnerSpec) (string, error) {
	ephemeralFlag := "--ephemeral"
	if !spec.Ephemeral {
		ephemeralFlag = ""
	}
	data := cloudInitData{
		RunnerID:          spec.RunnerID,
		GitHubOrgName:     spec.GitHubOrgName,
		RegistrationToken: spec.RegistrationToken,
		LabelsCSV:         strings.Join(spec.Labels, ","),
		EphemeralFlag:     ephemeralFlag,
	}
	var buf bytes.Buffer
	if err := cloudInitTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func buildFreeformTags(spec provisioner.RunnerSpec) map[string]string {
	tags := map[string]string{
		"byoc:tenant_id":  spec.TenantID,
		"byoc:runner_id":  spec.RunnerID,
		"byoc:managed_by": "byoc-oci-runners",
		"byoc:created_at": time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range spec.Tags {
		tags[k] = v
	}
	return tags
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found")
}

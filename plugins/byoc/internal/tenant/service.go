// Package tenant implements the tenant domain service — validation, defaults,
// and lifecycle transitions that sit between the HTTP layer and the store.
package tenant

import (
	"context"
	"fmt"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Service manages tenant lifecycle.
type Service struct {
	store  store.Store
	logger zerolog.Logger
}

// NewService constructs a tenant.Service.
func NewService(s store.Store, logger zerolog.Logger) *Service {
	return &Service{store: s, logger: logger.With().Str("component", "tenant_service").Logger()}
}

// CreateRequest carries all fields needed to onboard a new tenant.
type CreateRequest struct {
	Name             string   `json:"name"`
	GitHubAppID      int64    `json:"github_app_id"`
	GitHubInstallID  int64    `json:"github_install_id"`
	GitHubOrgName    string   `json:"github_org_name"`
	WebhookSecret    string   `json:"webhook_secret"`
	OCICompartmentID string   `json:"oci_compartment_id"`
	OCISubnetID      string   `json:"oci_subnet_id"`
	MaxRunners       int      `json:"max_runners"`
	MinWarmPool      int      `json:"min_warm_pool"`
	IdleTimeoutSec   int      `json:"idle_timeout_sec"`
	RunnerLabels     []string `json:"runner_labels"`
	RunnerShape      string   `json:"runner_shape"`
	RunnerOCPUs      float32  `json:"runner_ocpus"`
	RunnerMemoryGB   float32  `json:"runner_memory_gb"`
	ProvisionerType  string   `json:"provisioner_type"`
}

func (r *CreateRequest) applyDefaults() {
	if r.MaxRunners == 0 {
		r.MaxRunners = 20
	}
	if r.IdleTimeoutSec == 0 {
		r.IdleTimeoutSec = 300
	}
	if r.RunnerShape == "" {
		r.RunnerShape = "VM.Standard.E4.Flex"
	}
	if r.RunnerOCPUs == 0 {
		r.RunnerOCPUs = 2
	}
	if r.RunnerMemoryGB == 0 {
		r.RunnerMemoryGB = 8
	}
	if r.ProvisionerType == "" {
		r.ProvisionerType = "compute"
	}
}

func (r *CreateRequest) validate() error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if r.GitHubAppID == 0 {
		return fmt.Errorf("github_app_id is required")
	}
	if r.GitHubOrgName == "" {
		return fmt.Errorf("github_org_name is required")
	}
	if r.WebhookSecret == "" {
		return fmt.Errorf("webhook_secret is required")
	}
	if r.OCICompartmentID == "" {
		return fmt.Errorf("oci_compartment_id is required")
	}
	if r.OCISubnetID == "" {
		return fmt.Errorf("oci_subnet_id is required")
	}
	if r.MaxRunners < 1 || r.MaxRunners > 1000 {
		return fmt.Errorf("max_runners must be between 1 and 1000")
	}
	if r.ProvisionerType != "compute" && r.ProvisionerType != "container" {
		return fmt.Errorf("provisioner_type must be 'compute' or 'container'")
	}
	return nil
}

// Create validates the request, applies defaults, and persists the new tenant.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*store.Tenant, error) {
	req.applyDefaults()
	if err := req.validate(); err != nil {
		return nil, fmt.Errorf("invalid create tenant request: %w", err)
	}

	t := &store.Tenant{
		ID:               uuid.New().String(),
		Name:             req.Name,
		GitHubAppID:      req.GitHubAppID,
		GitHubInstallID:  req.GitHubInstallID,
		GitHubOrgName:    req.GitHubOrgName,
		WebhookSecret:    req.WebhookSecret,
		OCICompartmentID: req.OCICompartmentID,
		OCISubnetID:      req.OCISubnetID,
		MaxRunners:       req.MaxRunners,
		MinWarmPool:      req.MinWarmPool,
		IdleTimeoutSec:   req.IdleTimeoutSec,
		RunnerLabels:     req.RunnerLabels,
		RunnerShape:      req.RunnerShape,
		RunnerOCPUs:      req.RunnerOCPUs,
		RunnerMemoryGB:   req.RunnerMemoryGB,
		ProvisionerType:  req.ProvisionerType,
		Status:           store.TenantStatusActive,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := s.store.CreateTenant(ctx, t); err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}

	s.logger.Info().Str("tenant_id", t.ID).Str("name", t.Name).Msg("tenant created")
	return t, nil
}

// Get retrieves a tenant by ID.
func (s *Service) Get(ctx context.Context, id string) (*store.Tenant, error) {
	t, err := s.store.GetTenant(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get tenant %s: %w", id, err)
	}
	return t, nil
}

// Suspend marks a tenant as suspended; the orchestrator will stop dispatching for it.
func (s *Service) Suspend(ctx context.Context, id string) error {
	t, err := s.store.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	t.Status = store.TenantStatusSuspended
	t.UpdatedAt = time.Now()
	return s.store.UpdateTenant(ctx, t)
}

// Offboard marks a tenant for offboarding. The orchestrator drains runners before deletion.
func (s *Service) Offboard(ctx context.Context, id string) error {
	t, err := s.store.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	t.Status = store.TenantStatusOffboarding
	t.UpdatedAt = time.Now()
	return s.store.UpdateTenant(ctx, t)
}

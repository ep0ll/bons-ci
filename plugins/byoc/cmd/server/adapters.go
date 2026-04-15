package main

// This file contains no-op adapters for all external interfaces.
// In production replace each with the real adapter package.

import (
	"context"
	"fmt"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/github"
	"github.com/bons/bons-ci/plugins/byoc/internal/observability"
	"github.com/bons/bons-ci/plugins/byoc/internal/provisioner"
	"github.com/bons/bons-ci/plugins/byoc/internal/runner"
	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/rs/zerolog"
)

// --- noopStore ---

type noopStore struct{}

var _ store.Store = (*noopStore)(nil)

func (n *noopStore) CreateTenant(_ context.Context, _ *store.Tenant) error { return nil }
func (n *noopStore) GetTenant(_ context.Context, id string) (*store.Tenant, error) {
	return nil, fmt.Errorf("tenant %s: %w", id, store.ErrNotFound)
}
func (n *noopStore) UpdateTenant(_ context.Context, _ *store.Tenant) error { return nil }
func (n *noopStore) ListTenants(_ context.Context, _ store.TenantFilter) ([]*store.Tenant, error) {
	return nil, nil
}
func (n *noopStore) CreateRunner(_ context.Context, _ *store.Runner) error { return nil }
func (n *noopStore) GetRunner(_ context.Context, id string) (*store.Runner, error) {
	return nil, fmt.Errorf("runner %s: %w", id, store.ErrNotFound)
}
func (n *noopStore) UpdateRunnerStatus(_ context.Context, _ string, _ store.RunnerStatus, _ store.RunnerUpdateOpts) error {
	return nil
}
func (n *noopStore) ListRunners(_ context.Context, _ store.RunnerFilter) ([]*store.Runner, error) {
	return nil, nil
}
func (n *noopStore) CountRunners(_ context.Context, _ store.RunnerFilter) (int64, error) {
	return 0, nil
}
func (n *noopStore) DeleteRunner(_ context.Context, _ string) error { return nil }
func (n *noopStore) AcquireIdempotencyLock(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}
func (n *noopStore) Ping(_ context.Context) error { return nil }

// --- noopGitHubClient ---

type noopGitHubClient struct{}

var _ github.Client = (*noopGitHubClient)(nil)

func (n *noopGitHubClient) CreateRegistrationToken(_ context.Context, _ string) (*github.RegistrationToken, error) {
	return &github.RegistrationToken{Token: "noop-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (n *noopGitHubClient) RemoveRunner(_ context.Context, _ string, _ int64) error { return nil }
func (n *noopGitHubClient) ValidateWebhookSignature(_, _, _ string) error           { return nil }
func (n *noopGitHubClient) ParseWorkflowJobEvent(payload []byte) (*github.WorkflowJobEvent, error) {
	return &github.WorkflowJobEvent{}, nil
}

// ValidateWebhookSignature adapter with correct signature.
func (n *noopGitHubClient) validateSig(payload []byte, sigHeader, secret string) error { return nil }

// --- noopProvisioner ---

type noopProvisionerImpl struct{}

var _ provisioner.Provisioner = (*noopProvisionerImpl)(nil)

func (n *noopProvisionerImpl) Provision(_ context.Context, spec provisioner.RunnerSpec) (string, error) {
	return "ocid1.instance.noop." + spec.RunnerID, nil
}
func (n *noopProvisionerImpl) Terminate(_ context.Context, _ string) error { return nil }
func (n *noopProvisionerImpl) Describe(_ context.Context, id string) (*provisioner.InstanceState, error) {
	return &provisioner.InstanceState{OCIResourceID: id, State: "RUNNING", Running: true}, nil
}
func (n *noopProvisionerImpl) Type() string { return "noop" }

// --- noopRunnerService ---

type noopRunnerServiceImpl struct{}

// noopRunnerServiceImpl satisfies the duck-typed usage in orchestrator.New.
// In production replace with *runner.Service constructed via runner.NewService.
func (n *noopRunnerServiceImpl) CreateAndProvision(_ context.Context, _ *store.Tenant, _ int64) (*store.Runner, error) {
	return &store.Runner{ID: "noop"}, nil
}
func (n *noopRunnerServiceImpl) MarkIdle(_ context.Context, _ string, _ int64) error { return nil }
func (n *noopRunnerServiceImpl) MarkBusy(_ context.Context, _ string) error          { return nil }
func (n *noopRunnerServiceImpl) Terminate(_ context.Context, _, _ string) error      { return nil }

// Ensure runner.Service satisfies the same method set (compile-time duck check).
var _ interface {
	CreateAndProvision(context.Context, *store.Tenant, int64) (*store.Runner, error)
	MarkIdle(context.Context, string, int64) error
	MarkBusy(context.Context, string) error
	Terminate(context.Context, string, string) error
} = (*runner.Service)(nil)

// Suppress unused import warnings.
var _ = observability.TenantIDKey
var _ = zerolog.GlobalLevel()

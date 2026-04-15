// Package github defines the GitHub integration port and types for the BYOC platform.
// The domain depends only on the Client interface; concrete implementations
// live in sub-packages and are injected at startup.
package github

import (
	"context"
	"time"
)

// WorkflowJobAction represents the GitHub workflow_job event action field.
type WorkflowJobAction string

const (
	ActionQueued     WorkflowJobAction = "queued"
	ActionInProgress WorkflowJobAction = "in_progress"
	ActionCompleted  WorkflowJobAction = "completed"
	ActionWaiting    WorkflowJobAction = "waiting"
)

// WorkflowJobEvent is the parsed payload of a GitHub workflow_job webhook event.
type WorkflowJobEvent struct {
	Action      WorkflowJobAction `json:"action"`
	WorkflowJob WorkflowJob       `json:"workflow_job"`
	Repository  Repository        `json:"repository"`
	Org         *Organization     `json:"organization,omitempty"`
}

// WorkflowJob contains details about the queued/running/completed job.
type WorkflowJob struct {
	ID          int64     `json:"id"`
	RunID       int64     `json:"run_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Labels      []string  `json:"labels"`
	RunnerID    int64     `json:"runner_id"`
	RunnerName  string    `json:"runner_name"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// Repository identifies the repository that owns the workflow.
type Repository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// Organization identifies the GitHub organization, if applicable.
type Organization struct {
	Login string `json:"login"`
}

// RegistrationToken is a short-lived token used to register a runner with GitHub.
type RegistrationToken struct {
	Token     string
	ExpiresAt time.Time
}

// Client is the GitHub integration port. All orchestration logic depends on
// this interface; the concrete HTTP client is injected via constructor injection.
type Client interface {
	// CreateRegistrationToken fetches a fresh installation access token and uses it
	// to create a runner registration token for the tenant's GitHub org.
	// The returned token is valid for ~1 hour; callers must not cache it longer than 55 min.
	CreateRegistrationToken(ctx context.Context, tenantID string) (*RegistrationToken, error)

	// RemoveRunner deregisters a runner from the GitHub org.
	// This is best-effort — the caller should log errors but not fail the workflow.
	RemoveRunner(ctx context.Context, tenantID string, runnerID int64) error

	// ValidateWebhookSignature verifies the HMAC-SHA256 signature in the
	// X-Hub-Signature-256 header against the raw payload and tenant webhook secret.
	ValidateWebhookSignature(payload []byte, sigHeader, secret string) error

	// ParseWorkflowJobEvent decodes the raw webhook body into a WorkflowJobEvent.
	ParseWorkflowJobEvent(payload []byte) (*WorkflowJobEvent, error)
}

// Sentinel errors for the github package.
var (
	ErrInvalidSignature = &githubError{code: "INVALID_SIGNATURE", msg: "webhook signature validation failed"}
	ErrAPIFailure       = &githubError{code: "API_FAILURE", msg: "GitHub API call failed"}
	ErrTokenExpired     = &githubError{code: "TOKEN_EXPIRED", msg: "registration token has expired"}
)

type githubError struct {
	code string
	msg  string
}

func (e *githubError) Error() string { return e.msg }

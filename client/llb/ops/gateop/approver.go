package gateop

import (
	"context"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// Approver interface
// ─────────────────────────────────────────────────────────────────────────────

// Approver is the strategy interface for gate approval. Implementations define
// how to wait for and obtain approval signals.
type Approver interface {
	// WaitForApproval blocks until approval or denial is received, or the
	// context is cancelled.
	WaitForApproval(ctx context.Context, info GateInfo) (Approval, error)

	// Name returns a human-readable name for the approver type.
	Name() string
}

// ─────────────────────────────────────────────────────────────────────────────
// AutoApprover
// ─────────────────────────────────────────────────────────────────────────────

// AutoApprover immediately approves all gates. Useful for testing and CI
// environments where manual approval is not desired.
type AutoApprover struct{}

var _ Approver = (*AutoApprover)(nil)

// WaitForApproval returns immediate approval.
func (a *AutoApprover) WaitForApproval(_ context.Context, _ GateInfo) (Approval, error) {
	return Approval{
		Approved:  true,
		Approver:  "auto",
		Reason:    "auto-approved",
		Timestamp: time.Now(),
	}, nil
}

// Name returns the approver name.
func (a *AutoApprover) Name() string { return "auto" }

// ─────────────────────────────────────────────────────────────────────────────
// ChannelApprover
// ─────────────────────────────────────────────────────────────────────────────

// ChannelApprover waits for approval via a Go channel. Useful for
// programmatic control in tests and custom orchestrators.
type ChannelApprover struct {
	ch chan Approval
}

var _ Approver = (*ChannelApprover)(nil)

// NewChannelApprover creates a ChannelApprover with a buffered channel.
func NewChannelApprover() *ChannelApprover {
	return &ChannelApprover{ch: make(chan Approval, 1)}
}

// Approve sends an approval signal.
func (c *ChannelApprover) Approve(approver, reason string) {
	c.ch <- Approval{
		Approved:  true,
		Approver:  approver,
		Reason:    reason,
		Timestamp: time.Now(),
	}
}

// Deny sends a denial signal.
func (c *ChannelApprover) Deny(approver, reason string) {
	c.ch <- Approval{
		Approved:  false,
		Approver:  approver,
		Reason:    reason,
		Timestamp: time.Now(),
	}
}

// WaitForApproval blocks until a signal is received or context cancelled.
func (c *ChannelApprover) WaitForApproval(ctx context.Context, _ GateInfo) (Approval, error) {
	select {
	case <-ctx.Done():
		return Approval{}, ctx.Err()
	case a := <-c.ch:
		return a, nil
	}
}

// Name returns the approver name.
func (c *ChannelApprover) Name() string { return "channel" }

// ─────────────────────────────────────────────────────────────────────────────
// TimeoutApprover
// ─────────────────────────────────────────────────────────────────────────────

// TimeoutApprover wraps another approver and auto-approves or auto-denies
// after a timeout.
type TimeoutApprover struct {
	inner       Approver
	timeout     time.Duration
	autoApprove bool // if true, auto-approve on timeout; else auto-deny
}

var _ Approver = (*TimeoutApprover)(nil)

// NewTimeoutApprover creates a TimeoutApprover that auto-approves on timeout.
func NewTimeoutApprover(inner Approver, timeout time.Duration, autoApproveOnTimeout bool) *TimeoutApprover {
	return &TimeoutApprover{
		inner:       inner,
		timeout:     timeout,
		autoApprove: autoApproveOnTimeout,
	}
}

// WaitForApproval delegates to the inner approver with a timeout.
func (t *TimeoutApprover) WaitForApproval(ctx context.Context, info GateInfo) (Approval, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	result, err := t.inner.WaitForApproval(ctx, info)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Approval{
				Approved:  t.autoApprove,
				Approver:  "timeout",
				Reason:    "auto-decision after timeout: " + t.timeout.String(),
				Timestamp: time.Now(),
			}, nil
		}
		return Approval{}, err
	}
	return result, nil
}

// Name returns the approver name.
func (t *TimeoutApprover) Name() string { return "timeout(" + t.inner.Name() + ")" }

// ─────────────────────────────────────────────────────────────────────────────
// WebhookApprover
// ─────────────────────────────────────────────────────────────────────────────

// WebhookApprover sends approval requests to an HTTP endpoint and polls for
// the result.
type WebhookApprover struct {
	url      string
	client   *http.Client
	interval time.Duration
}

var _ Approver = (*WebhookApprover)(nil)

// NewWebhookApprover creates a WebhookApprover.
func NewWebhookApprover(url string, opts ...WebhookOption) *WebhookApprover {
	w := &WebhookApprover{
		url:      url,
		client:   &http.Client{Timeout: 30 * time.Second},
		interval: 5 * time.Second,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// WebhookOption configures a WebhookApprover.
type WebhookOption func(*WebhookApprover)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(c *http.Client) WebhookOption {
	return func(w *WebhookApprover) { w.client = c }
}

// WithPollInterval sets the polling interval.
func WithPollInterval(d time.Duration) WebhookOption {
	return func(w *WebhookApprover) { w.interval = d }
}

// WaitForApproval sends a POST to the webhook URL and polls for a result.
func (w *WebhookApprover) WaitForApproval(ctx context.Context, info GateInfo) (Approval, error) {
	// Send initial request.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, nil)
	if err != nil {
		return Approval{}, errors.Wrap(err, "creating webhook request")
	}
	req.Header.Set("X-Gate-ID", info.ID)
	req.Header.Set("X-Gate-Description", info.Description)

	resp, err := w.client.Do(req)
	if err != nil {
		return Approval{}, errors.Wrap(err, "sending webhook request")
	}
	resp.Body.Close()

	// Poll for approval.
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return Approval{}, ctx.Err()
		case <-ticker.C:
			pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url, nil)
			if err != nil {
				continue
			}
			pollReq.Header.Set("X-Gate-ID", info.ID)

			pollResp, err := w.client.Do(pollReq)
			if err != nil {
				continue
			}
			pollResp.Body.Close()

			switch pollResp.StatusCode {
			case http.StatusOK:
				return Approval{
					Approved:  true,
					Approver:  "webhook:" + w.url,
					Reason:    "approved via webhook",
					Timestamp: time.Now(),
				}, nil
			case http.StatusForbidden:
				return Approval{
					Approved:  false,
					Approver:  "webhook:" + w.url,
					Reason:    "denied via webhook",
					Timestamp: time.Now(),
				}, nil
			// 202 = still pending, continue polling
			case http.StatusAccepted:
				continue
			default:
				continue
			}
		}
	}
}

// Name returns the approver name.
func (w *WebhookApprover) Name() string { return "webhook" }

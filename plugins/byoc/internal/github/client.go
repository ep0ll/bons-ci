// Package github provides the concrete GitHub API client implementation.
package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/byoc/internal/vault"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
)

// compile-time interface check
var _ Client = (*HTTPClient)(nil)

// HTTPClient is the production GitHub API client.
// It uses a GitHub App private key (fetched from OCI Vault) to generate
// short-lived installation access tokens for runner registration.
type HTTPClient struct {
	appID      int64
	vault      vault.Client
	httpClient *http.Client
	logger     zerolog.Logger
	// tokenCache stores the last installation token per tenant to avoid
	// fetching a new one for every runner registration within the TTL window.
	tokenCache map[string]*cachedToken
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// HTTPClientConfig holds configuration for the GitHub HTTP client.
type HTTPClientConfig struct {
	// AppID is the GitHub App ID — the same for all tenants.
	AppID int64
	// VaultClient provides access to the GitHub App private key PEM.
	VaultClient vault.Client
}

// NewHTTPClient returns a production-ready GitHub client.
func NewHTTPClient(cfg HTTPClientConfig, logger zerolog.Logger) *HTTPClient {
	return &HTTPClient{
		appID:      cfg.AppID,
		vault:      cfg.VaultClient,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		logger:     logger.With().Str("component", "github_client").Logger(),
		tokenCache: make(map[string]*cachedToken),
	}
}

// CreateRegistrationToken generates a short-lived GitHub runner registration token
// for the given tenant. It:
//  1. Fetches the GitHub App private key PEM from OCI Vault.
//  2. Mints a short-lived JWT (10-min expiry) signed with the App private key.
//  3. Exchanges the JWT for an installation access token (IAT).
//  4. Uses the IAT to create a runner registration token via the GitHub REST API.
func (c *HTTPClient) CreateRegistrationToken(ctx context.Context, tenantID string) (*RegistrationToken, error) {
	// Re-use cached installation token if still valid (>5 min remaining).
	if cached, ok := c.tokenCache[tenantID]; ok && time.Until(cached.expiresAt) > 5*time.Minute {
		return c.createRunnerToken(ctx, tenantID, cached.token)
	}

	// Step 1: fetch private key PEM from Vault.
	secretID := fmt.Sprintf("github-app-key-%s", tenantID)
	pemBytes, err := c.vault.GetSecret(ctx, secretID)
	if err != nil {
		return nil, fmt.Errorf("fetch github app private key for tenant %s: %w", tenantID, err)
	}

	// Step 2: mint a GitHub App JWT.
	appJWT, err := c.mintAppJWT(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("mint github app JWT: %w", err)
	}

	// Step 3: exchange JWT for installation access token.
	installID, err := c.vault.GetSecret(ctx, fmt.Sprintf("github-install-id-%s", tenantID))
	if err != nil {
		return nil, fmt.Errorf("fetch github install id for tenant %s: %w", tenantID, err)
	}

	iat, iatExpiry, err := c.fetchInstallationToken(ctx, appJWT, string(installID))
	if err != nil {
		return nil, fmt.Errorf("fetch installation access token: %w", err)
	}
	c.tokenCache[tenantID] = &cachedToken{token: iat, expiresAt: iatExpiry}

	// Step 4: create runner registration token using the IAT.
	return c.createRunnerToken(ctx, tenantID, iat)
}

func (c *HTTPClient) mintAppJWT(pemBytes []byte) (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	if err != nil {
		return "", fmt.Errorf("parse RSA private key: %w", err)
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-30 * time.Second)), // clock skew tolerance
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		Issuer:    fmt.Sprintf("%d", c.appID),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(key)
}

func (c *HTTPClient) fetchInstallationToken(ctx context.Context, appJWT, installationID string) (string, time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", time.Time{}, fmt.Errorf("github installation token: HTTP %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("decode installation token response: %w", err)
	}
	return result.Token, result.ExpiresAt, nil
}

func (c *HTTPClient) createRunnerToken(ctx context.Context, tenantID, installationToken string) (*RegistrationToken, error) {
	orgName, err := c.vault.GetSecret(ctx, fmt.Sprintf("github-org-%s", tenantID))
	if err != nil {
		return nil, fmt.Errorf("fetch org name for tenant %s: %w", tenantID, err)
	}

	url := fmt.Sprintf("https://api.github.com/orgs/%s/actions/runners/registration-token", string(orgName))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+installationToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST registration-token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("github registration token: HTTP %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode registration token: %w", err)
	}
	return &RegistrationToken{Token: result.Token, ExpiresAt: result.ExpiresAt}, nil
}

// RemoveRunner deregisters a self-hosted runner from the GitHub org.
func (c *HTTPClient) RemoveRunner(ctx context.Context, tenantID string, runnerID int64) error {
	orgName, err := c.vault.GetSecret(ctx, fmt.Sprintf("github-org-%s", tenantID))
	if err != nil {
		return fmt.Errorf("fetch org name for tenant %s: %w", tenantID, err)
	}

	cached := c.tokenCache[tenantID]
	if cached == nil || time.Until(cached.expiresAt) < 5*time.Minute {
		// Re-fetch — we need a valid IAT to remove a runner.
		if _, err := c.CreateRegistrationToken(ctx, tenantID); err != nil {
			return fmt.Errorf("refresh token before remove runner: %w", err)
		}
		cached = c.tokenCache[tenantID]
	}

	url := fmt.Sprintf("https://api.github.com/orgs/%s/actions/runners/%d", string(orgName), runnerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cached.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE runner %d: %w", runnerID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("remove runner %d: HTTP %d: %s", runnerID, resp.StatusCode, body)
	}
	return nil
}

// ValidateWebhookSignature checks the HMAC-SHA256 signature in X-Hub-Signature-256.
func (c *HTTPClient) ValidateWebhookSignature(payload []byte, sigHeader, secret string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return ErrInvalidSignature
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(sigHeader, prefix))
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return ErrInvalidSignature
	}
	return nil
}

// ParseWorkflowJobEvent decodes a raw webhook payload into a WorkflowJobEvent.
func (c *HTTPClient) ParseWorkflowJobEvent(payload []byte) (*WorkflowJobEvent, error) {
	var event WorkflowJobEvent
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&event); err != nil {
		// Unknown fields from GitHub's evolving API should not be fatal.
		// Re-decode permissively.
		if err2 := json.Unmarshal(payload, &event); err2 != nil {
			return nil, fmt.Errorf("parse workflow_job event: %w", err2)
		}
	}
	return &event, nil
}

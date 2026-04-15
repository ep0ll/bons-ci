package orchestrator_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validateWebhookSignature mirrors the implementation in github/client.go.
// Extracted here so the test has no external dependencies.
func validateWebhookSignature(payload []byte, sigHeader, secret string) error {
	const prefix = "sha256="
	if len(sigHeader) <= len(prefix) || sigHeader[:len(prefix)] != prefix {
		return fmt.Errorf("invalid signature format")
	}
	sig, err := hex.DecodeString(sigHeader[len(prefix):])
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

func TestWebhookSignature_ValidSignature(t *testing.T) {
	secret := "super-secret-webhook-key"
	payload := []byte(`{"action":"queued","workflow_job":{"id":12345}}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	err := validateWebhookSignature(payload, sig, secret)
	require.NoError(t, err)
}

func TestWebhookSignature_WrongSecret_ShouldFail(t *testing.T) {
	payload := []byte(`{"action":"queued"}`)

	mac := hmac.New(sha256.New, []byte("correct-secret"))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	err := validateWebhookSignature(payload, sig, "wrong-secret")
	assert.Error(t, err)
}

func TestWebhookSignature_TamperedPayload_ShouldFail(t *testing.T) {
	secret := "my-secret"
	originalPayload := []byte(`{"action":"queued"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(originalPayload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Tampered payload
	tamperedPayload := []byte(`{"action":"completed"}`)
	err := validateWebhookSignature(tamperedPayload, sig, secret)
	assert.Error(t, err)
}

func TestWebhookSignature_MissingPrefix_ShouldFail(t *testing.T) {
	payload := []byte(`{}`)
	// Signature without "sha256=" prefix
	err := validateWebhookSignature(payload, "deadbeef", "secret")
	assert.Error(t, err)
}

func TestWebhookSignature_EmptySignature_ShouldFail(t *testing.T) {
	err := validateWebhookSignature([]byte(`{}`), "", "secret")
	assert.Error(t, err)
}

// parseRunnerID mirrors the implementation in api/handler/webhook.go.
func parseRunnerID(runnerName string) string {
	const prefix = "byoc-"
	if len(runnerName) > len(prefix) && runnerName[:len(prefix)] == prefix {
		return runnerName[len(prefix):]
	}
	return ""
}

func TestParseRunnerID_ValidName(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"byoc-550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
		{"byoc-abc123", "abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.expected, parseRunnerID(tc.input))
		})
	}
}

func TestParseRunnerID_InvalidName(t *testing.T) {
	cases := []string{"github-hosted-runner", "byoc-", "", "other-prefix-uuid"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			result := parseRunnerID(name)
			// "byoc-" alone returns "" because len > len(prefix) is false.
			if name == "byoc-uuid" {
				assert.NotEmpty(t, result)
			} else {
				assert.Empty(t, result)
			}
		})
	}
}

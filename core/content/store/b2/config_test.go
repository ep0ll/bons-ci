package b2

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ParseConfig
// ---------------------------------------------------------------------------

func TestParseConfig_Defaults(t *testing.T) {
	t.Setenv("AWS_TENANT", "test-tenant")

	cfg, err := ParseConfig(map[string]string{})
	require.NoError(t, err)

	assert.Equal(t, DefaultBucket, cfg.Bucket)
	assert.Equal(t, DefaultRegion, cfg.Region)
	assert.Equal(t, "test-tenant", cfg.Tenant)
	assert.Equal(t, DefaultManifestsPrefix, cfg.ManifestsPrefix)
	assert.Equal(t, DefaultBlobsPrefix, cfg.BlobsPrefix)
	assert.Equal(t, []string{"bons"}, cfg.Names)
	assert.Equal(t, 24*time.Hour, cfg.TouchRefresh)
	assert.Equal(t, 4, cfg.UploadParallelism)
	assert.False(t, cfg.UsePathStyle)
}

func TestParseConfig_AllAttrs(t *testing.T) {
	attrs := map[string]string{
		"bucket":             "my-bucket",
		"region":             "eu-central-1",
		"tenant":             "acme",
		"prefix":             "ci/",
		"manifests_prefix":   "m/",
		"blobs_prefix":       "b/",
		"name":               "foo;bar;baz",
		"touch_refresh":      "1h",
		"endpoint_url":       "s3.example.com",
		"access_key_id":      "AK",
		"secret_access_key":  "SK",
		"session_token":      "ST",
		"use_path_style":     "true",
		"upload_parallelism": "8",
	}

	cfg, err := ParseConfig(attrs)
	require.NoError(t, err)

	assert.Equal(t, "my-bucket", cfg.Bucket)
	assert.Equal(t, "eu-central-1", cfg.Region)
	assert.Equal(t, "acme", cfg.Tenant)
	assert.Equal(t, "ci/", cfg.Prefix)
	assert.Equal(t, "m/", cfg.ManifestsPrefix)
	assert.Equal(t, "b/", cfg.BlobsPrefix)
	assert.Equal(t, []string{"foo", "bar", "baz"}, cfg.Names)
	assert.Equal(t, time.Hour, cfg.TouchRefresh)
	assert.Equal(t, "s3.example.com", cfg.EndpointURL)
	assert.Equal(t, "AK", cfg.AccessKeyID)
	assert.Equal(t, "SK", cfg.SecretAccessKey)
	assert.Equal(t, "ST", cfg.SessionToken)
	assert.True(t, cfg.UsePathStyle)
	assert.Equal(t, 8, cfg.UploadParallelism)
}

func TestParseConfig_EnvFallback(t *testing.T) {
	t.Setenv("AWS_BUCKET", "env-bucket")
	t.Setenv("AWS_REGION", "env-region")
	t.Setenv("AWS_TENANT", "env-tenant")

	cfg, err := ParseConfig(map[string]string{})
	require.NoError(t, err)

	assert.Equal(t, "env-bucket", cfg.Bucket)
	assert.Equal(t, "env-region", cfg.Region)
	assert.Equal(t, "env-tenant", cfg.Tenant)
}

func TestParseConfig_AttrOverridesEnv(t *testing.T) {
	t.Setenv("AWS_BUCKET", "env-bucket")
	t.Setenv("AWS_TENANT", "env-tenant")

	cfg, err := ParseConfig(map[string]string{
		"bucket": "attr-bucket",
		"tenant": "attr-tenant",
	})
	require.NoError(t, err)

	assert.Equal(t, "attr-bucket", cfg.Bucket)
	assert.Equal(t, "attr-tenant", cfg.Tenant)
}

func TestParseConfig_MissingTenant(t *testing.T) {
	os.Unsetenv("AWS_TENANT")
	_, err := ParseConfig(map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant is required")
}

func TestParseConfig_TenantWithSlash(t *testing.T) {
	_, err := ParseConfig(map[string]string{"tenant": "bad/tenant"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain '/'")
}

func TestParseConfig_EmptyTenant(t *testing.T) {
	_, err := ParseConfig(map[string]string{"tenant": ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestParseConfig_InvalidUploadParallelism(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"non-integer", "abc"},
		{"zero", "0"},
		{"negative", "-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig(map[string]string{
				"tenant":             "t",
				"upload_parallelism": tc.value,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "upload_parallelism")
		})
	}
}

func TestParseConfig_InvalidTouchRefreshFallsBack(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"tenant":        "t",
		"touch_refresh": "not-a-duration",
	})
	require.NoError(t, err)
	// Falls back to default.
	assert.Equal(t, 24*time.Hour, cfg.TouchRefresh)
}

func TestParseConfig_InvalidUsePathStyleFallsBack(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"tenant":         "t",
		"use_path_style": "not-a-bool",
	})
	require.NoError(t, err)
	assert.False(t, cfg.UsePathStyle)
}

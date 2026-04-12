package b2

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all tunables for a B2 content store.
type Config struct {
	Bucket            string
	Region            string
	Prefix            string
	Tenant            string
	ManifestsPrefix   string
	BlobsPrefix       string
	Names             []string
	TouchRefresh      time.Duration
	EndpointURL       string
	AccessKeyID       string
	SecretAccessKey   string
	SessionToken      string
	UsePathStyle      bool
	UploadParallelism int
}

// ---------------------------------------------------------------------------
// Attribute keys
// ---------------------------------------------------------------------------

const (
	attrBucket            = "bucket"
	attrRegion            = "region"
	attrPrefix            = "prefix"
	attrTenant            = "tenant"
	attrManifestsPrefix   = "manifests_prefix"
	attrBlobsPrefix       = "blobs_prefix"
	attrName              = "name"
	attrTouchRefresh      = "touch_refresh"
	attrEndpointURL       = "endpoint_url"
	attrAccessKeyID       = "access_key_id"
	attrSecretAccessKey   = "secret_access_key"
	attrSessionToken      = "session_token"
	attrUsePathStyle      = "use_path_style"
	attrUploadParallelism = "upload_parallelism"
)

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

const (
	DefaultBucket          = "bons"
	DefaultRegion          = "us-east-005"
	DefaultManifestsPrefix = "manifests/"
	DefaultBlobsPrefix     = "blobs/"
	DefaultVerticesPrefix  = "vertices/"
)

// ParseConfig constructs a Config from the given attribute map, falling
// back to environment variables and built-in defaults.
func ParseConfig(attrs map[string]string) (Config, error) {
	bucket := attrOrEnv(attrs, attrBucket, "AWS_BUCKET", DefaultBucket)
	region := attrOrEnv(attrs, attrRegion, "AWS_REGION", DefaultRegion)

	tenant, ok := attrs[attrTenant]
	if !ok {
		tenant, ok = os.LookupEnv("AWS_TENANT")
		if !ok {
			return Config{}, fmt.Errorf("b2: tenant is required (set %q attr or AWS_TENANT env)", attrTenant)
		}
	}
	if err := validateTenant(tenant); err != nil {
		return Config{}, err
	}

	prefix := attrs[attrPrefix]

	manifestsPrefix := attrOr(attrs, attrManifestsPrefix, DefaultManifestsPrefix)
	blobsPrefix := attrOr(attrs, attrBlobsPrefix, DefaultBlobsPrefix)

	names := []string{"bons"}
	if name, ok := attrs[attrName]; ok {
		if parts := strings.Split(name, ";"); len(parts) > 0 {
			names = parts
		}
	}

	touchRefresh := 24 * time.Hour
	if s, ok := attrs[attrTouchRefresh]; ok {
		if d, err := time.ParseDuration(s); err == nil {
			touchRefresh = d
		}
	}

	usePathStyle := false
	if s, ok := attrs[attrUsePathStyle]; ok {
		if v, err := strconv.ParseBool(s); err == nil {
			usePathStyle = v
		}
	}

	uploadParallelism := 4
	if s, ok := attrs[attrUploadParallelism]; ok {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			return Config{}, fmt.Errorf("b2: %s must be a positive integer, got %q", attrUploadParallelism, s)
		}
		uploadParallelism = v
	}

	return Config{
		Bucket:            bucket,
		Region:            region,
		Prefix:            prefix,
		Tenant:            tenant,
		ManifestsPrefix:   manifestsPrefix,
		BlobsPrefix:       blobsPrefix,
		Names:             names,
		TouchRefresh:      touchRefresh,
		EndpointURL:       attrs[attrEndpointURL],
		AccessKeyID:       attrs[attrAccessKeyID],
		SecretAccessKey:   attrs[attrSecretAccessKey],
		SessionToken:      attrs[attrSessionToken],
		UsePathStyle:      usePathStyle,
		UploadParallelism: uploadParallelism,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func attrOrEnv(attrs map[string]string, key, envKey, fallback string) string {
	if v, ok := attrs[key]; ok {
		return v
	}
	if v, ok := os.LookupEnv(envKey); ok {
		return v
	}
	return fallback
}

func attrOr(attrs map[string]string, key, fallback string) string {
	if v, ok := attrs[key]; ok {
		return v
	}
	return fallback
}

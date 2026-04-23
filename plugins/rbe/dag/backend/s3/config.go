package s3

import (
	"fmt"
	"time"
)

const (
	defaultWorkers        = 32
	defaultPartSize       = 64 << 20 // 64 MiB
	minPartSize           = 5 << 20  // 5 MiB — S3 minimum part size
	defaultRetryMax       = 3
	defaultRetryBase      = 100 * time.Millisecond
	defaultRetryMaxDelay  = 5 * time.Second
	defaultListPageSize   = 1000
	defaultConnectTimeout = 10 * time.Second
	defaultOpTimeout      = 60 * time.Second
)

// Config holds all configuration for the S3/MinIO store backend.
type Config struct {
	// ——— connection ————————————————————————————————————————————————————————
	// Endpoint is the S3-compatible endpoint, e.g. "s3.amazonaws.com" or
	// "minio.internal:9000".  Do NOT include the scheme.
	Endpoint string

	// AccessKey and SecretKey are the S3 credentials.
	AccessKey string
	SecretKey string

	// SessionToken is used for temporary credentials (e.g. AWS STS).
	SessionToken string

	// UseSSL controls TLS.  Set false only for local dev MinIO.
	UseSSL bool

	// Region is optional but recommended for AWS S3.
	Region string

	// ——— storage ———————————————————————————————————————————————————————————
	// Bucket is the S3 bucket name.  Required.
	Bucket string

	// KeyPrefix is an optional namespace prepended to every object key,
	// allowing multiple logical stores to share one bucket.
	// Example: "dagstore/prod/"
	KeyPrefix string

	// ——— performance ———————————————————————————————————————————————————————
	// Workers is the maximum number of parallel object operations.
	// Default: 32.
	Workers int

	// PartSize is the multipart-upload chunk size for streams larger than this
	// value.  Must be >= 5 MiB (S3 minimum).  Default: 64 MiB.
	PartSize int64

	// ——— retry ———————————————————————————————————————————————————————————
	// RetryMax is the maximum number of retry attempts (0 = no retries).
	RetryMax int
	// RetryBaseDelay is the initial backoff interval.
	RetryBaseDelay time.Duration
	// RetryMaxDelay caps the exponential backoff.
	RetryMaxDelay time.Duration

	// ——— timeouts ————————————————————————————————————————————————————————
	// ConnectTimeout is the dial timeout for the S3 client.
	ConnectTimeout time.Duration
	// OpTimeout is a per-operation context timeout added when the caller's
	// context has no deadline.  Set 0 to rely solely on caller context.
	OpTimeout time.Duration

	// ——— pagination ——————————————————————————————————————————————————————
	// ListPageSize is the maximum number of objects per list page.
	ListPageSize int
}

// Validate returns an error if required fields are missing or values are out of range.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("s3 config: endpoint is required")
	}
	if c.Bucket == "" {
		return fmt.Errorf("s3 config: bucket is required")
	}
	if c.PartSize > 0 && c.PartSize < minPartSize {
		return fmt.Errorf("s3 config: part_size %d is below the S3 minimum of %d bytes",
			c.PartSize, minPartSize)
	}
	return nil
}

// withDefaults returns a copy of c with zero fields replaced by sensible defaults.
func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = defaultWorkers
	}
	if c.PartSize <= 0 {
		c.PartSize = defaultPartSize
	}
	if c.PartSize < minPartSize {
		c.PartSize = minPartSize
	}
	if c.RetryMax < 0 {
		c.RetryMax = 0
	}
	if c.RetryMax == 0 {
		c.RetryMax = defaultRetryMax
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = defaultRetryBase
	}
	if c.RetryMaxDelay <= 0 {
		c.RetryMaxDelay = defaultRetryMaxDelay
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = defaultConnectTimeout
	}
	if c.OpTimeout <= 0 {
		c.OpTimeout = defaultOpTimeout
	}
	if c.ListPageSize <= 0 {
		c.ListPageSize = defaultListPageSize
	}
	return c
}

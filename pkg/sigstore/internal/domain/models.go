package domain

import "time"

// SigningResult is the value object returned by a Signer implementation.
// It is immutable after construction.
type SigningResult struct {
	ImageRef      string
	SignatureRef  string
	CertChain     string // PEM-encoded; empty for static-key flows
	RekorLogIndex int64
	SignedAt      time.Time
}

// KeySpec identifies which key (or key-flow) to use for signing.
// An empty Name implies keyless (Fulcio) signing.
type KeySpec struct {
	Name     string // logical name, resolved by KeyProvider
	KMSPath  string // e.g. gcpkms://...; empty for static keys
	IsKeyless bool
}

// RetryPolicy carries per-request retry configuration.
// Zero value is safe: 0 max attempts means the ResiliencePolicy defaults apply.
type RetryPolicy struct {
	MaxAttempts int
	InitialWait time.Duration
	MaxWait     time.Duration
}

package models

import "time"

type CacheSharingMode string

const (
	CacheSharingShared  CacheSharingMode = "shared"
	CacheSharingPrivate CacheSharingMode = "private"
	CacheSharingLocked  CacheSharingMode = "locked"
)

// MountCache is a BuildKit-style persistent mount cache.
type MountCache struct {
	ID               string            `json:"id"`
	UserKey          string            `json:"user_key"`
	Scope            string            `json:"scope"`
	PlatformSpecific bool              `json:"platform_specific"`
	Platform         *Platform         `json:"platform,omitempty"`
	Sharing          CacheSharingMode  `json:"sharing"`
	BlobDigest       string            `json:"blob_digest,omitempty"`
	SizeBytes        int64             `json:"size_bytes"`
	Locked           bool              `json:"locked"`
	LockOwner        string            `json:"lock_owner,omitempty"`
	LockExpiresAt    *time.Time        `json:"lock_expires_at,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	AccessedAt       time.Time         `json:"accessed_at"`
}

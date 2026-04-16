// Package referral implements the OCI 1.1 Referrers API store.
//
// The referrers API allows arbitrary OCI artefacts (signatures, SBOMs,
// SOCI indexes, attestations) to reference an existing manifest via the
// `subject` field. Clients can then discover all artefacts attached to a
// manifest by querying GET /v2/{name}/referrers/{digest}.
//
// Storage model: subject_digest → []ocispec.Descriptor (filtered by
// artifactType). The index is kept sharded on the first byte of the
// subject digest (same scheme as the accel index) to allow concurrent
// writes from many goroutines pushing signatures simultaneously.
package referral

import (
	"context"
	"sync"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const numShards = 256

// ────────────────────────────────────────────────────────────────────────────
// shard
// ────────────────────────────────────────────────────────────────────────────

type shard struct {
	mu    sync.RWMutex
	index map[repoSubjectKey][]ocispec.Descriptor
}

type repoSubjectKey struct {
	repo    string
	subject digest.Digest
}

// ────────────────────────────────────────────────────────────────────────────
// Store
// ────────────────────────────────────────────────────────────────────────────

// Store implements types.ReferrersStore using 256-shard in-memory storage.
type Store struct {
	shards [numShards]*shard
}

// New returns a ready-to-use referrers Store.
func New() *Store {
	s := &Store{}
	for i := range s.shards {
		s.shards[i] = &shard{index: make(map[repoSubjectKey][]ocispec.Descriptor)}
	}
	return s
}

func (s *Store) shardFor(subjectDigest digest.Digest) *shard {
	hex := subjectDigest.Encoded()
	if len(hex) >= 2 {
		hi := hexNibble(hex[0]) << 4
		lo := hexNibble(hex[1])
		return s.shards[hi|lo]
	}
	return s.shards[0]
}

// AddReferrer records that the descriptor desc references subjectDigest.
// The operation is idempotent on desc.Digest.
func (s *Store) AddReferrer(_ context.Context, repo string, subjectDigest digest.Digest, desc ocispec.Descriptor) error {
	sh := s.shardFor(subjectDigest)
	key := repoSubjectKey{repo: repo, subject: subjectDigest}

	sh.mu.Lock()
	defer sh.mu.Unlock()

	existing := sh.index[key]
	for _, d := range existing {
		if d.Digest == desc.Digest {
			return nil // idempotent
		}
	}
	sh.index[key] = append(existing, desc)
	return nil
}

// GetReferrers returns all referrer descriptors for subjectDigest.
// If artifactType is non-empty, only descriptors with that ArtifactType are
// returned (OCI 1.1 filter parameter).
func (s *Store) GetReferrers(_ context.Context, repo string, subjectDigest digest.Digest, artifactType string) ([]ocispec.Descriptor, error) {
	sh := s.shardFor(subjectDigest)
	key := repoSubjectKey{repo: repo, subject: subjectDigest}

	sh.mu.RLock()
	raw := sh.index[key]
	sh.mu.RUnlock()

	if len(raw) == 0 {
		return nil, nil
	}

	if artifactType == "" {
		cp := make([]ocispec.Descriptor, len(raw))
		copy(cp, raw)
		return cp, nil
	}

	// Filter by artifactType
	var filtered []ocispec.Descriptor
	for _, d := range raw {
		if d.ArtifactType == artifactType {
			filtered = append(filtered, d)
		}
	}
	return filtered, nil
}

// RemoveReferrer removes a specific referrer by its manifest digest.
func (s *Store) RemoveReferrer(_ context.Context, repo string, subjectDigest, manifestDigest digest.Digest) error {
	sh := s.shardFor(subjectDigest)
	key := repoSubjectKey{repo: repo, subject: subjectDigest}

	sh.mu.Lock()
	defer sh.mu.Unlock()

	existing := sh.index[key]
	for i, d := range existing {
		if d.Digest == manifestDigest {
			existing[i] = existing[len(existing)-1]
			sh.index[key] = existing[:len(existing)-1]
			return nil
		}
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Helper
// ────────────────────────────────────────────────────────────────────────────

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

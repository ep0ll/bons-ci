package memory

import (
	digest "github.com/opencontainers/go-digest"
)

// ── manifestEntry accessor methods ────────────────────────────────────────

func (e manifestEntry) Digest() digest.Digest { return e.digest }
func (e manifestEntry) MediaType() string      { return e.mediaType }
func (e manifestEntry) Size() int64            { return e.size }

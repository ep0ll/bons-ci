// Package ops implements all concrete vertex types (operations) for the DAG.
//
// Each op type implements vertex.Vertex and produces a stable, content-derived ID
// via the idOf helper. IDs are computed by hashing the op's canonical JSON
// representation, ensuring the same logical operation always maps to the same ID.
//
// Ops are immutable after construction. All exported fields on Info structs are
// set at construction time and never mutated.
package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ─── Constraints ──────────────────────────────────────────────────────────────

// Platform specifies a target OS/architecture combination.
type Platform struct {
	OS           string   `json:"os"`
	Architecture string   `json:"arch"`
	Variant      string   `json:"variant,omitempty"`
	OSVersion    string   `json:"os_version,omitempty"`
	OSFeatures   []string `json:"os_features,omitempty"`
}

// Constraints carries build-time configuration that applies to an operation.
// It is separate from the op's semantic content so that the same operation
// can be represented with different constraints (e.g. different target platforms)
// as distinct vertices.
type Constraints struct {
	// Platform specifies the target platform for this operation.
	// Nil means "use the default platform".
	Platform *Platform `json:"platform,omitempty"`

	// WorkerConstraints are opaque filter strings forwarded to the solver
	// (e.g. "type=qemu") to select capable worker nodes.
	WorkerConstraints []string `json:"worker_constraints,omitempty"`

	// Metadata carries operation-level annotations.
	Metadata Metadata `json:"metadata,omitempty"`

	// CacheIgnore forces this vertex to be re-evaluated even if a cache hit exists.
	CacheIgnore bool `json:"cache_ignore,omitempty"`

	// Description is a free-form map of key-value pairs for human display.
	Description map[string]string `json:"description,omitempty"`
}

// WithPlatform returns a copy of c with the platform set.
func (c Constraints) WithPlatform(p Platform) Constraints {
	c.Platform = &p
	return c
}

// WithDescription returns a copy of c with a description key set.
func (c Constraints) WithDescription(key, value string) Constraints {
	if c.Description == nil {
		c.Description = make(map[string]string)
	}
	c.Description[key] = value
	return c
}

// ─── Metadata ─────────────────────────────────────────────────────────────────

// Metadata carries per-vertex annotations that travel alongside the wire format
// but do not affect the content digest.
type Metadata struct {
	// IgnoreCache forces cache bypass for this vertex.
	IgnoreCache bool `json:"ignore_cache,omitempty"`

	// Description is a free-form map (e.g. {"llb.customname": "Build stage"}).
	Description map[string]string `json:"description,omitempty"`

	// ExportCache controls whether this vertex's result is exported.
	ExportCache *bool `json:"export_cache,omitempty"`

	// ProgressGroup tags this vertex to a named progress group for UI grouping.
	ProgressGroup string `json:"progress_group,omitempty"`
}

// ─── ID computation ──────────────────────────────────────────────────────────

// idOf computes a deterministic, content-derived ID from any JSON-serializable
// payload. The payload must be a stable representation of the operation —
// same operation = same bytes = same ID.
//
// This is NOT a cryptographic security mechanism; it is a content-addressable
// identity scheme. The sha256 hash provides good collision resistance and is
// extremely fast for small op payloads.
func idOf(payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		// This should never happen in practice because all payloads are
		// constructed from plain Go structs. Panic is appropriate here to
		// surface programming errors early rather than silently producing
		// wrong IDs.
		panic(fmt.Sprintf("ops: failed to marshal ID payload: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sortedKeys returns the sorted keys of a string map, used to ensure
// deterministic JSON serialization of maps (Go map iteration is unordered).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// attrsID returns a sorted, deterministic representation of a string→string map.
// Used when map attributes are part of an op's ID payload.
func attrsSlice(m map[string]string) [][2]string {
	if len(m) == 0 {
		return nil
	}
	keys := sortedKeys(m)
	out := make([][2]string, len(keys))
	for i, k := range keys {
		out[i] = [2]string{k, m[k]}
	}
	return out
}

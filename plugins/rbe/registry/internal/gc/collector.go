// Package gc provides reference-counted garbage collection for the AccelRegistry
// content store.
//
// The GC works in two phases:
//
//  1. MARK — Walk all known manifests (via the ManifestLister) and collect
//     every blob digest reachable from them (config + layers). Build a
//     "live set" in a sync.Map for O(1) concurrent membership tests.
//
//  2. SWEEP — Walk the ContentStore and delete any blob whose digest is NOT
//     in the live set (i.e. no manifest references it).
//
// Safety:
//   - The mark and sweep phases run sequentially.
//   - No new blobs are written between mark start and sweep end (the caller
//     should hold a write-lock or pause ingest, or accept that recently
//     pushed blobs may be swept if their manifest has not yet been written —
//     mitigated by the GracePeriod).
//   - GracePeriod skips blobs younger than N minutes even if unreferenced,
//     protecting in-progress pushes.
//
// Typical usage:
//
//	collector := gc.New(store, lister, gc.Config{GracePeriod: 10 * time.Minute})
//	report, err := collector.Collect(ctx)
package gc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Interfaces
// ────────────────────────────────────────────────────────────────────────────

// ManifestLister returns all manifest digests known to the registry.
type ManifestLister interface {
	ListManifestDigests(ctx context.Context) ([]digest.Digest, error)
}

// ────────────────────────────────────────────────────────────────────────────
// Config
// ────────────────────────────────────────────────────────────────────────────

// Config configures the Collector.
type Config struct {
	// GracePeriod skips blobs created within this duration (default: 10 min).
	// This prevents a concurrent push from having its blobs swept before its
	// manifest is written.
	GracePeriod time.Duration

	// DryRun disables actual deletion and only reports what would be swept.
	DryRun bool

	// Concurrency is the number of parallel blob walkers during sweep (default: 8).
	Concurrency int
}

// DefaultGracePeriod is used when Config.GracePeriod is negative (unset).
// Set GracePeriod to 0 for no grace period, or to -1 to use the default.
const DefaultGracePeriod = 10 * time.Minute

func (c *Config) setDefaults() {
	if c.GracePeriod < 0 {
		c.GracePeriod = DefaultGracePeriod
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Report
// ────────────────────────────────────────────────────────────────────────────

// Report describes the outcome of a GC collection run.
type Report struct {
	// StartedAt is when the collection began.
	StartedAt time.Time `json:"startedAt"`
	// FinishedAt is when it completed.
	FinishedAt time.Time `json:"finishedAt"`
	// Duration is FinishedAt - StartedAt.
	Duration time.Duration `json:"duration"`
	// LiveBlobs is the count of reachable blobs after mark.
	LiveBlobs int64 `json:"liveBlobs"`
	// TotalBlobs is the total blob count before sweep.
	TotalBlobs int64 `json:"totalBlobs"`
	// SweptBlobs is the count of blobs deleted (or that would be deleted in DryRun).
	SweptBlobs int64 `json:"sweptBlobs"`
	// SweptBytes is the total bytes freed.
	SweptBytes int64 `json:"sweptBytes"`
	// Errors is a list of non-fatal errors encountered.
	Errors []string `json:"errors,omitempty"`
	// DryRun is true when no deletions were performed.
	DryRun bool `json:"dryRun"`
}

// ────────────────────────────────────────────────────────────────────────────
// Collector
// ────────────────────────────────────────────────────────────────────────────

// Collector implements reference-counted blob GC.
type Collector struct {
	store  types.ContentStore
	lister ManifestLister
	cfg    Config
}

// New creates a Collector.
func New(store types.ContentStore, lister ManifestLister, cfg Config) *Collector {
	cfg.setDefaults()
	return &Collector{store: store, lister: lister, cfg: cfg}
}

// Collect runs mark + sweep and returns a Report.
func (c *Collector) Collect(ctx context.Context) (*Report, error) {
	report := &Report{StartedAt: time.Now(), DryRun: c.cfg.DryRun}

	// ── Phase 1: MARK ────────────────────────────────────────────────────
	liveSet, totalBlobs, err := c.mark(ctx, report)
	if err != nil {
		return nil, fmt.Errorf("gc mark: %w", err)
	}
	report.TotalBlobs = totalBlobs

	// Count live blobs
	liveSet.Range(func(_, _ any) bool { report.LiveBlobs++; return true })

	// ── Phase 2: SWEEP ───────────────────────────────────────────────────
	if err := c.sweep(ctx, liveSet, report); err != nil {
		return nil, fmt.Errorf("gc sweep: %w", err)
	}

	report.FinishedAt = time.Now()
	report.Duration = report.FinishedAt.Sub(report.StartedAt)
	return report, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Mark phase
// ────────────────────────────────────────────────────────────────────────────

// mark walks all manifests and collects every referenced blob into liveSet.
func (c *Collector) mark(ctx context.Context, report *Report) (*sync.Map, int64, error) {
	manifests, err := c.lister.ListManifestDigests(ctx)
	if err != nil {
		return nil, 0, err
	}

	var liveSet sync.Map

	// Count total blobs for the report
	var totalBlobs int64
	_ = c.store.Walk(ctx, func(info types.ContentInfo) error {
		atomic.AddInt64(&totalBlobs, 1)
		return nil
	})

	// Mark all manifest blobs themselves as live
	for _, dgst := range manifests {
		liveSet.Store(dgst, struct{}{})

		// Parse manifest and mark its children
		rc, err := c.store.Get(ctx, dgst)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("mark: get manifest %s: %v", dgst, err))
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("mark: read manifest %s: %v", dgst, err))
			continue
		}

		c.markManifestChildren(data, &liveSet, report)
	}

	return &liveSet, totalBlobs, nil
}

// markManifestChildren parses raw manifest/index bytes and marks all
// referenced blob digests as live.
func (c *Collector) markManifestChildren(data []byte, liveSet *sync.Map, report *Report) {
	// Try as image index first
	var idx ocispec.Index
	if err := json.Unmarshal(data, &idx); err == nil && len(idx.Manifests) > 0 {
		for _, m := range idx.Manifests {
			liveSet.Store(m.Digest, struct{}{})
		}
		return
	}

	// Try as image manifest
	var mf ocispec.Manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("mark: parse manifest: %v", err))
		return
	}
	if mf.Config.Digest != "" {
		liveSet.Store(mf.Config.Digest, struct{}{})
	}
	for _, layer := range mf.Layers {
		if layer.Digest != "" {
			liveSet.Store(layer.Digest, struct{}{})
		}
	}
	if mf.Subject != nil && mf.Subject.Digest != "" {
		liveSet.Store(mf.Subject.Digest, struct{}{})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Sweep phase
// ────────────────────────────────────────────────────────────────────────────

// sweep deletes all blobs not in liveSet and not within GracePeriod.
func (c *Collector) sweep(ctx context.Context, liveSet *sync.Map, report *Report) error {
	cutoff := time.Now().Add(-c.cfg.GracePeriod)
	sem := make(chan struct{}, c.cfg.Concurrency)

	var (
		sweptBlobs int64
		sweptBytes int64
		errMu      sync.Mutex
	)

	err := c.store.Walk(ctx, func(info types.ContentInfo) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Check live set
		if _, live := liveSet.Load(info.Digest); live {
			return nil
		}

		// Skip recently created blobs (grace period)
		if info.CreatedAt.After(cutoff) {
			return nil
		}

		sem <- struct{}{}
		go func(di types.ContentInfo) {
			defer func() { <-sem }()

			if !c.cfg.DryRun {
				if err := c.store.Delete(ctx, di.Digest); err != nil {
					errMu.Lock()
					report.Errors = append(report.Errors,
						fmt.Sprintf("sweep: delete %s: %v", di.Digest, err))
					errMu.Unlock()
					return
				}
			}
			atomic.AddInt64(&sweptBlobs, 1)
			atomic.AddInt64(&sweptBytes, di.Size)
		}(info)
		return nil
	})

	// Drain semaphore — wait for all goroutines
	for i := 0; i < c.cfg.Concurrency; i++ {
		sem <- struct{}{}
	}

	report.SweptBlobs = atomic.LoadInt64(&sweptBlobs)
	report.SweptBytes = atomic.LoadInt64(&sweptBytes)
	return err
}

// ────────────────────────────────────────────────────────────────────────────
// SimpleManifestLister — wraps ManifestIndex for GC usage
// ────────────────────────────────────────────────────────────────────────────

// SimpleManifestLister walks a ContentStore to find all manifest blobs
// by probing for valid JSON with a schemaVersion field.
// For production, replace with a direct ManifestIndex.ListAll() call.
type SimpleManifestLister struct {
	store types.ContentStore
}

// NewSimpleManifestLister creates a SimpleManifestLister backed by store.
func NewSimpleManifestLister(store types.ContentStore) *SimpleManifestLister {
	return &SimpleManifestLister{store: store}
}

// ListManifestDigests returns the digests of all blobs that parse as OCI manifests.
func (s *SimpleManifestLister) ListManifestDigests(ctx context.Context) ([]digest.Digest, error) {
	var manifests []digest.Digest
	err := s.store.Walk(ctx, func(info types.ContentInfo) error {
		rc, err := s.store.Get(ctx, info.Digest)
		if err != nil {
			return nil
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, 4*1024*1024))
		if err != nil {
			return nil
		}
		if isManifestJSON(data) {
			manifests = append(manifests, info.Digest)
		}
		return nil
	})
	return manifests, err
}

// isManifestJSON returns true if data parses as an OCI manifest or index.
func isManifestJSON(data []byte) bool {
	// Fast path: must start with '{'
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		ArtifactType  string `json:"artifactType"`
	}
	return json.Unmarshal(data, &probe) == nil &&
		(probe.SchemaVersion >= 2 || probe.MediaType != "" || probe.ArtifactType != "")
}

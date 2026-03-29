package httpapplier

// applier.go – Implements HTTPApplier: orchestrates fetch → verify → unpack.
//
// Concurrency model
// ─────────────────
// A single Apply call is deliberately single-goroutine for its happy path:
//   1. Fetch → write to a temp file (disk I/O; CPU idle most of the time).
//   2. Verify signature if requested (CPU; fast).
//   3. Re-open temp file → pipe to Unpack (disk I/O + decompression CPU).
//
// The fetcher itself may use HTTP/2 multiplexing internally.
// The unpackers are free to parallelise internally (e.g. zstd decoder).
// Multiple Apply calls on different descriptors can run concurrently because
// the struct has no mutable shared state; all state lives on the stack.
//
// Error handling
// ──────────────
// All errors are wrapped with pkg/errors to preserve stack traces.
// Typed sentinel errors (ErrDigestMismatch, etc.) are preserved through
// wrapping and can be detected with errors.As.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/pkg/errors"
)

// Options configures the default HTTPApplier.
type Options struct {
	// Fetcher downloads remote content.  Defaults to NewDefaultFetcher with
	// default FetcherOptions.
	Fetcher HTTPFetcher

	// Unpacker extracts layer content onto mounts.  Defaults to TarUnpacker.
	Unpacker Unpacker

	// SignatureVerifier is used when the fetch request carries a Signature.
	// Defaults to pgpVerifier (OpenPGP).
	SignatureVerifier SignatureVerifier

	// WorkDir is where temporary files are written during Apply.
	// Defaults to os.TempDir().
	WorkDir string

	// MaxConcurrentFetches caps parallel Apply calls (0 = unlimited).
	// When set, excess calls block until a slot is free.
	MaxConcurrentFetches int
}

// httpApplier is the concrete implementation of HTTPApplier.
type httpApplier struct {
	fetcher   HTTPFetcher
	unpacker  Unpacker
	verifier  SignatureVerifier
	workDir   string
	semaphore chan struct{} // nil when MaxConcurrentFetches == 0
}

// New constructs an HTTPApplier from Options.
// It is the primary constructor; prefer it over zero-value initialisation.
func New(opts Options) (HTTPApplier, error) {
	fetcher := opts.Fetcher
	if fetcher == nil {
		var err error
		fetcher, err = NewDefaultFetcher(FetcherOptions{})
		if err != nil {
			return nil, errors.Wrap(err, "create default fetcher")
		}
	}

	unpacker := opts.Unpacker
	if unpacker == nil {
		unpacker = &TarUnpacker{}
	}

	verifier := opts.SignatureVerifier
	if verifier == nil {
		verifier = &pgpVerifier{}
	}

	workDir := opts.WorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}

	var sem chan struct{}
	if opts.MaxConcurrentFetches > 0 {
		sem = make(chan struct{}, opts.MaxConcurrentFetches)
	}

	return &httpApplier{
		fetcher:   fetcher,
		unpacker:  unpacker,
		verifier:  verifier,
		workDir:   workDir,
		semaphore: sem,
	}, nil
}

// Apply implements HTTPApplier.
//
// Steps:
//  1. Validate descriptor (must have at least one URL).
//  2. Acquire concurrency semaphore if configured.
//  3. Create a secure temp file in workDir.
//  4. Fetch remote content → temp file (verified in-stream).
//  5. Verify PGP signature if requested.
//  6. Open temp file → Unpack onto mounts.
//  7. Build and return the new descriptor.
//  8. Remove temp file (deferred).
func (a *httpApplier) Apply(ctx context.Context, desc ocispec.Descriptor, mounts []mount.Mount, opts ...ApplyOpt) (ocispec.Descriptor, error) {
	cfg := resolveApplyConfig(opts)

	if err := validateDescriptor(desc); err != nil {
		return ocispec.Descriptor{}, err
	}

	// ── Concurrency gate ──────────────────────────────────────────────────────
	if a.semaphore != nil {
		select {
		case a.semaphore <- struct{}{}:
			defer func() { <-a.semaphore }()
		case <-ctx.Done():
			return ocispec.Descriptor{}, ctx.Err()
		}
	}

	// ── Temp file ─────────────────────────────────────────────────────────────
	// mktemp with 0600 (owner read/write only) so other processes on the same
	// host cannot read partially-written layer data.
	tmp, err := os.CreateTemp(a.workDir, "httpapplier-*.tmp")
	if err != nil {
		return ocispec.Descriptor{}, errors.Wrap(err, "create temp file")
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	// ── Fetch ─────────────────────────────────────────────────────────────────
	req, err := buildFetchRequest(desc)
	if err != nil {
		tmp.Close()
		return ocispec.Descriptor{}, err
	}

	result, err := a.fetcher.Fetch(ctx, req, tmp)
	if err != nil {
		tmp.Close()
		return ocispec.Descriptor{}, errors.Wrap(err, "fetch")
	}

	// Ensure buffered writes reach disk before we verify or unpack.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return ocispec.Descriptor{}, errors.Wrap(err, "fsync temp file after fetch")
	}
	tmp.Close()

	// ── Signature verification ─────────────────────────────────────────────
	if req.Signature != nil {
		vopts := VerifyOptions{
			PubKey:    req.Signature.ArmoredPubKey,
			Signature: req.Signature.ArmoredSignature,
		}
		if err := a.verifier.Verify(ctx, tmpPath, req.Signature.ArmoredSignature, vopts); err != nil {
			return ocispec.Descriptor{}, errors.Wrap(err, "signature verification")
		}
	}

	// ── Optional processor (e.g. decryption) ─────────────────────────────────
	var src io.Reader
	rawFile, err := os.Open(tmpPath)
	if err != nil {
		return ocispec.Descriptor{}, errors.Wrap(err, "re-open temp file for unpack")
	}
	defer rawFile.Close()

	src = rawFile
	if cfg.ProcessorFunc != nil {
		processed, err := cfg.ProcessorFunc(rawFile)
		if err != nil {
			return ocispec.Descriptor{}, errors.Wrap(err, "processor func")
		}
		src = processed
	}

	// ── Unpack ────────────────────────────────────────────────────────────────
	if len(mounts) > 0 {
		uOpts := unpackOptionsFromDesc(desc, result)
		if err := a.unpacker.Unpack(ctx, src, desc.MediaType, mounts, uOpts); err != nil {
			return ocispec.Descriptor{}, errors.Wrap(err, "unpack")
		}
	}

	// ── Build output descriptor ───────────────────────────────────────────────
	out := buildOutputDescriptor(desc, result, cfg)

	return out, nil
}

// ─── Functional options ───────────────────────────────────────────────────────

// WithParentDigests sets the parent digest chain for the output descriptor.
func WithParentDigests(digests ...interface{}) ApplyOpt {
	return func(cfg *ApplyConfig) {
		// no-op placeholder; extend as needed
	}
}

// WithLabels attaches key/value labels to the output descriptor annotations.
func WithLabels(labels map[string]string) ApplyOpt {
	return func(cfg *ApplyConfig) {
		cfg.Labels = labels
	}
}

// WithProcessorFunc sets a processor that wraps the raw reader before unpack.
func WithProcessorFunc(fn func(io.Reader) (io.Reader, error)) ApplyOpt {
	return func(cfg *ApplyConfig) {
		cfg.ProcessorFunc = fn
	}
}

func resolveApplyConfig(opts []ApplyOpt) ApplyConfig {
	var cfg ApplyConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func validateDescriptor(desc ocispec.Descriptor) error {
	if len(desc.URLs) == 0 {
		return errors.New("descriptor has no URLs")
	}
	return nil
}

// buildFetchRequest translates an ocispec.Descriptor into a FetchRequest.
// It uses the first URL in desc.URLs and the digest as the pinned content check.
func buildFetchRequest(desc ocispec.Descriptor) (FetchRequest, error) {
	req := FetchRequest{
		URL:          desc.URLs[0],
		PinnedDigest: desc.Digest,
	}

	// Extract filename from annotations if present (OCI spec: org.opencontainers.image.title).
	if ann := desc.Annotations; ann != nil {
		if fn, ok := ann["org.opencontainers.image.title"]; ok {
			req.Filename = fn
		}
	}

	return req, nil
}

func unpackOptionsFromDesc(desc ocispec.Descriptor, result FetchResult) UnpackOptions {
	opts := UnpackOptions{
		Filename: result.Filename,
		MTime:    result.LastModified,
	}
	if ann := desc.Annotations; ann != nil {
		if uidStr, ok := ann["buildkit/source.http.uid"]; ok {
			if uid, err := strconv.Atoi(uidStr); err == nil {
				opts.UID = &uid
			}
		}
		if gidStr, ok := ann["buildkit/source.http.gid"]; ok {
			if gid, err := strconv.Atoi(gidStr); err == nil {
				opts.GID = &gid
			}
		}
		if permStr, ok := ann["buildkit/source.http.perm"]; ok {
			if perm, err := strconv.ParseInt(permStr, 0, 32); err == nil {
				p := int(perm)
				opts.Perm = &p
			}
		}
		if fn, ok := ann["buildkit/source.http.filename"]; ok {
			opts.Filename = fn
		}
	}
	if opts.Filename == "" {
		opts.Filename = "download"
	}
	return opts
}

// buildOutputDescriptor constructs the ocispec.Descriptor that Apply returns.
// The returned digest is the content digest of the fetched bytes (pre-unpack),
// matching containerd's convention for the "diff-id" chain.
func buildOutputDescriptor(base ocispec.Descriptor, result FetchResult, cfg ApplyConfig) ocispec.Descriptor {
	out := ocispec.Descriptor{
		MediaType: base.MediaType,
		Digest:    result.Digest,
		Size:      base.Size,
	}
	if len(cfg.Labels) > 0 {
		if out.Annotations == nil {
			out.Annotations = make(map[string]string, len(cfg.Labels))
		}
		for k, v := range cfg.Labels {
			out.Annotations[k] = v
		}
	}
	return out
}

// ─── Metrics / observability stub ────────────────────────────────────────────

// ApplyMetrics carries counters updated by the applier.
// Callers can read these atomically for Prometheus / OpenTelemetry export.
type ApplyMetrics struct {
	TotalApplied  atomic.Int64
	TotalBytes    atomic.Int64
	TotalDuration atomic.Int64 // nanoseconds
	Errors        atomic.Int64
}

// instrumentedApplier wraps an HTTPApplier with metrics collection.
type instrumentedApplier struct {
	inner   HTTPApplier
	metrics *ApplyMetrics
}

// NewInstrumented wraps inner with metrics.
func NewInstrumented(inner HTTPApplier, m *ApplyMetrics) HTTPApplier {
	return &instrumentedApplier{inner: inner, metrics: m}
}

func (ia *instrumentedApplier) Apply(ctx context.Context, desc ocispec.Descriptor, mounts []mount.Mount, opts ...ApplyOpt) (ocispec.Descriptor, error) {
	start := time.Now()
	out, err := ia.inner.Apply(ctx, desc, mounts, opts...)
	elapsed := time.Since(start)

	ia.metrics.TotalApplied.Add(1)
	ia.metrics.TotalDuration.Add(elapsed.Nanoseconds())
	if err != nil {
		ia.metrics.Errors.Add(1)
	} else {
		ia.metrics.TotalBytes.Add(out.Size)
	}
	return out, err
}

// ─── Convenience: in-memory apply (testing / small payloads) ─────────────────

// ApplyBytes applies the given bytes as if they were downloaded from a URL.
// Useful in tests to skip actual HTTP network calls.
func ApplyBytes(ctx context.Context, applier HTTPApplier, mediaType string, data []byte, mounts []mount.Mount) (ocispec.Descriptor, error) {
	// Build a synthetic descriptor whose "URL" is a data URI.
	// The default fetcher will reject data: URIs as insecure, so this method
	// bypasses the fetcher entirely and calls Unpack directly.
	unpacker := &TarUnpacker{}
	opts := UnpackOptions{Filename: "download"}
	if err := unpacker.Unpack(ctx, bytes.NewReader(data), mediaType, mounts, opts); err != nil {
		return ocispec.Descriptor{}, err
	}
	return ocispec.Descriptor{MediaType: mediaType}, nil
}

// ─── Temporary file helpers ───────────────────────────────────────────────────

// secureTempDir creates a temporary directory with 0700 permissions.
// Used when the caller needs to stage files before unpack.
func secureTempDir(base string) (string, error) {
	dir, err := os.MkdirTemp(base, "httpapplier-*")
	if err != nil {
		return "", err
	}
	// Tighten permissions: only owner can enter the directory.
	if err := os.Chmod(dir, 0700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// atomicWriteFile writes data to a temp file in the same directory as dst,
// then renames it into place.  This ensures readers never see a partial file.
func atomicWriteFile(dst string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()
	return os.Rename(tmpPath, dst)
}

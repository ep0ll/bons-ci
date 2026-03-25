// Package gitapply implements a secure git content fetcher and a containerd
// diff.Applier adapter that populates filesystem mounts from git repository refs.
//
// # Architecture
//
// The package is structured around five narrow interfaces:
//
//   - [GitFetcher]        – fetches a ref into a plain directory (no containerd dependency)
//   - [DescriptorParser]  – decodes an OCI descriptor into a [FetchSpec]
//   - [MountActivator]    – activates containerd mounts and exposes a root directory
//   - [AuthProvider]      – resolves per-remote authentication credentials
//   - [SignatureVerifier] – verifies commit / tag cryptographic signatures
//
// [ContainerdApplierAdapter] composes DescriptorParser, MountActivator, and
// GitFetcher to implement containerd's diff.Applier interface.
//
// # Security posture
//
// Every layer of the stack applies defence-in-depth:
//
//   - Credentials are never written to disk; they are injected via git -c
//     http.<remote>.extraheader= in memory and scrubbed from error output.
//   - SSH uses BatchMode=yes and StrictHostKeyChecking=yes always.
//   - git processes run in their own process group (Setpgid) with Pdeathsig
//     on Linux/FreeBSD so they die when the parent exits.
//   - On Linux the umask is isolated via CLONE_FS + runtime.LockOSThread so
//     the 0022 setting does not bleed into other goroutines.
//   - Local git clones use the file:// protocol to disable optimisations that
//     could be abused to copy unintended host files into the build context.
//   - FETCH_HEAD is removed and the reflog is expired after checkout to
//     prevent later processes from reading the upstream URL or history.
//   - The child environment is built from a minimal whitelist; the parent's
//     GIT_CONFIG, HOME-level config, and system config are all suppressed.
//   - All remote URLs in error messages are redacted via [redactURL].
package gitapply

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/mount"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// MediaTypeGitCommit is the OCI media type used in descriptors returned by
// [ContainerdApplierAdapter.Apply] to indicate that the content is a git checkout.
const MediaTypeGitCommit = "application/vnd.git.commit.v1"

// GitFetcher populates a local directory with the content of a git ref.
//
// Implementations are responsible for:
//   - Authenticating with the remote (via [AuthProvider]).
//   - Shallow-cloning to minimise network and disk usage.
//   - Verifying commit/tag signatures when requested.
//   - Initialising submodules unless explicitly suppressed.
//   - Cleaning up transient state (FETCH_HEAD, reflog, temp dirs).
//
// Fetch must be safe to call concurrently for distinct remotes; callers that
// share a single instance across goroutines should not assume serialisation.
type GitFetcher interface {
	// Fetch resolves spec and populates dstDir with the checked-out content.
	//
	// dstDir must exist and should be empty; behaviour with non-empty
	// directories is implementation-defined (typically a merge / overwrite).
	// On success dstDir contains only the requested tree (no .git directory
	// unless spec.KeepGitDir is true) and the returned [FetchResult] carries
	// the fully-resolved commit SHA.
	Fetch(ctx context.Context, spec FetchSpec, dstDir string) (FetchResult, error)
}

// DescriptorParser converts an OCI content descriptor (typically carrying git
// fetch parameters in its Annotations map) into a [FetchSpec].
//
// Decoupling parsing from fetching lets callers define their own annotation
// conventions or bypass parsing entirely by constructing FetchSpec directly.
type DescriptorParser interface {
	ParseFetchSpec(desc ocispec.Descriptor) (FetchSpec, error)
}

// MountActivator mounts a slice of containerd mounts onto a temporary directory
// and calls fn with the resulting host-visible root path.
//
// The mount is guaranteed to be active for the duration of fn.  Implementations
// must unmount cleanly whether fn succeeds or returns an error.
type MountActivator interface {
	Activate(ctx context.Context, mounts []mount.Mount, fn func(rootDir string) error) error
}

// ContainerdApplierAdapter bridges [GitFetcher] to containerd's diff.Applier
// interface so that git sources can be used anywhere a containerd layer applier
// is expected (e.g. as a snapshotter content provider).
//
// The returned descriptor from Apply carries:
//   - MediaType: [MediaTypeGitCommit]
//   - Digest:    the commit SHA encoded as a go-digest (sha1:<40hex> or sha256:<64hex>)
//   - Annotations: resolved remote (redacted), ref, and commit SHA
type ContainerdApplierAdapter struct {
	fetcher   GitFetcher
	parser    DescriptorParser
	activator MountActivator
}

// NewContainerdApplierAdapter constructs a [ContainerdApplierAdapter].
// All three arguments are required; passing nil panics at construction time
// to surface misconfiguration early.
func NewContainerdApplierAdapter(
	fetcher GitFetcher,
	parser DescriptorParser,
	activator MountActivator,
) *ContainerdApplierAdapter {
	if fetcher == nil {
		panic("gitapply: NewContainerdApplierAdapter: fetcher must not be nil")
	}
	if parser == nil {
		panic("gitapply: NewContainerdApplierAdapter: parser must not be nil")
	}
	if activator == nil {
		panic("gitapply: NewContainerdApplierAdapter: activator must not be nil")
	}
	return &ContainerdApplierAdapter{
		fetcher:   fetcher,
		parser:    parser,
		activator: activator,
	}
}

// Apply implements diff.Applier.
//
// The flow is:
//  1. Parse the OCI descriptor's annotations into a [FetchSpec].
//  2. Activate the provided mounts to obtain a host-visible directory.
//  3. Delegate to the underlying [GitFetcher].
//  4. Return a descriptor annotated with the resolved commit metadata.
func (a *ContainerdApplierAdapter) Apply(
	ctx context.Context,
	desc ocispec.Descriptor,
	mounts []mount.Mount,
	opts ...diff.ApplyOpt,
) (ocispec.Descriptor, error) {
	spec, err := a.parser.ParseFetchSpec(desc)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("gitapply: parse descriptor: %w", err)
	}

	var result FetchResult
	applyErr := a.activator.Activate(ctx, mounts, func(rootDir string) error {
		result, err = a.fetcher.Fetch(ctx, spec, rootDir)
		return err
	})
	if applyErr != nil {
		return ocispec.Descriptor{}, fmt.Errorf("gitapply: apply git content to mount: %w", applyErr)
	}

	applied := ocispec.Descriptor{
		MediaType: MediaTypeGitCommit,
		Digest:    commitDigest(result.CommitSHA),
		Annotations: map[string]string{
			AnnotationRemote:   redactURL(spec.Remote),
			AnnotationRef:      result.Ref,
			AnnotationChecksum: result.CommitSHA,
		},
	}
	return applied, nil
}

// commitDigest encodes a git commit SHA as a go-digest.Digest.
// SHA-1 commit hashes (40 hex chars) use the "sha1" algorithm prefix;
// SHA-256 hashes (64 hex chars) use "sha256".
func commitDigest(sha string) digest.Digest {
	switch len(sha) {
	case 64:
		return digest.Digest("sha256:" + sha)
	default: // 40 chars — SHA-1
		return digest.Digest("sha1:" + sha)
	}
}

// Compile-time assertion: ContainerdApplierAdapter must satisfy diff.Applier.
var _ diff.Applier = (*ContainerdApplierAdapter)(nil)

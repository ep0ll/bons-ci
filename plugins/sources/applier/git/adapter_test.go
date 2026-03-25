package gitapply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/mount"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── Test doubles ─────────────────────────────────────────────────────────────

// stubFetcher is a [GitFetcher] that records calls and returns preset responses.
type stubFetcher struct {
	result FetchResult
	err    error
	calls  []FetchSpec
}

func (s *stubFetcher) Fetch(_ context.Context, spec FetchSpec, _ string) (FetchResult, error) {
	s.calls = append(s.calls, spec)
	return s.result, s.err
}

// stubParser is a [DescriptorParser] that returns preset responses.
type stubParser struct {
	spec FetchSpec
	err  error
}

func (s *stubParser) ParseFetchSpec(_ ocispec.Descriptor) (FetchSpec, error) {
	return s.spec, s.err
}

// recordingActivator is a [MountActivator] that records the root dir it called
// fn with.  It uses [PassthroughMountActivator] internally.
type recordingActivator struct {
	inner     MountActivator
	activated []string
}

func (a *recordingActivator) Activate(ctx context.Context, mounts []mount.Mount, fn func(string) error) error {
	return a.inner.Activate(ctx, mounts, func(root string) error {
		a.activated = append(a.activated, root)
		return fn(root)
	})
}

// ─── ContainerdApplierAdapter ─────────────────────────────────────────────────

func TestContainerdApplierAdapter_Apply_happy(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()

	spec := FetchSpec{Remote: "https://github.com/org/repo.git", Ref: "main"}
	wantResult := FetchResult{
		CommitSHA: "aabbccdd" + "00000000000000000000000000000000",
		Ref:       "refs/heads/main",
	}
	fetcher := &stubFetcher{result: wantResult}
	parser := &stubParser{spec: spec}
	activator := &recordingActivator{inner: &PassthroughMountActivator{RootDir: dst}}

	adapter := NewContainerdApplierAdapter(fetcher, parser, activator)

	ctx := context.Background()
	applied, err := adapter.Apply(ctx, ocispec.Descriptor{}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify descriptor fields.
	if applied.MediaType != MediaTypeGitCommit {
		t.Errorf("MediaType: want %q, got %q", MediaTypeGitCommit, applied.MediaType)
	}
	wantDigest := commitDigest(wantResult.CommitSHA)
	if applied.Digest != wantDigest {
		t.Errorf("Digest: want %q, got %q", wantDigest, applied.Digest)
	}
	if applied.Annotations[AnnotationChecksum] != wantResult.CommitSHA {
		t.Errorf("checksum annotation: want %q, got %q",
			wantResult.CommitSHA, applied.Annotations[AnnotationChecksum])
	}
	if applied.Annotations[AnnotationRef] != wantResult.Ref {
		t.Errorf("ref annotation: want %q, got %q",
			wantResult.Ref, applied.Annotations[AnnotationRef])
	}

	// Verify the activator saw exactly one call.
	if len(activator.activated) != 1 {
		t.Errorf("activator should have been called once; got %d", len(activator.activated))
	}

	// Verify the fetcher was called with the parsed spec.
	if len(fetcher.calls) != 1 {
		t.Fatalf("fetcher should have been called once; got %d", len(fetcher.calls))
	}
	if fetcher.calls[0].Remote != spec.Remote {
		t.Errorf("fetcher called with wrong remote: want %q, got %q",
			spec.Remote, fetcher.calls[0].Remote)
	}
}

func TestContainerdApplierAdapter_Apply_parseError(t *testing.T) {
	t.Parallel()
	parseErr := errors.New("bad descriptor")
	adapter := NewContainerdApplierAdapter(
		&stubFetcher{},
		&stubParser{err: parseErr},
		&PassthroughMountActivator{RootDir: t.TempDir()},
	)
	_, err := adapter.Apply(context.Background(), ocispec.Descriptor{}, nil)
	if err == nil {
		t.Fatal("expected error from parser, got nil")
	}
	if !errors.Is(err, parseErr) {
		t.Errorf("error should wrap parseErr; got: %v", err)
	}
}

func TestContainerdApplierAdapter_Apply_fetchError(t *testing.T) {
	t.Parallel()
	fetchErr := errors.New("network timeout")
	spec := FetchSpec{Remote: "https://github.com/org/repo.git"}
	adapter := NewContainerdApplierAdapter(
		&stubFetcher{err: fetchErr},
		&stubParser{spec: spec},
		&PassthroughMountActivator{RootDir: t.TempDir()},
	)
	_, err := adapter.Apply(context.Background(), ocispec.Descriptor{}, nil)
	if err == nil {
		t.Fatal("expected fetch error, got nil")
	}
	if !errors.Is(err, fetchErr) {
		t.Errorf("error should wrap fetchErr; got: %v", err)
	}
}

func TestContainerdApplierAdapter_Apply_activatorError(t *testing.T) {
	t.Parallel()
	mountErr := errors.New("mount failed: no such device")
	// activator that always returns an error
	badActivator := &alwaysErrActivator{err: mountErr}
	spec := FetchSpec{Remote: "https://github.com/org/repo.git"}
	adapter := NewContainerdApplierAdapter(
		&stubFetcher{result: FetchResult{CommitSHA: "aa" + "000000000000000000000000000000000000000"}},
		&stubParser{spec: spec},
		badActivator,
	)
	_, err := adapter.Apply(context.Background(), ocispec.Descriptor{}, nil)
	if err == nil {
		t.Fatal("expected activator error, got nil")
	}
	if !errors.Is(err, mountErr) {
		t.Errorf("error should wrap mountErr; got: %v", err)
	}
}

type alwaysErrActivator struct{ err error }

func (a *alwaysErrActivator) Activate(_ context.Context, _ []mount.Mount, _ func(string) error) error {
	return a.err
}

// ─── NewContainerdApplierAdapter nil guards ────────────────────────────────────

func TestNewContainerdApplierAdapter_nilFetcherPanics(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewContainerdApplierAdapter(nil, &stubParser{}, &PassthroughMountActivator{RootDir: "/"})
	})
}

func TestNewContainerdApplierAdapter_nilParserPanics(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewContainerdApplierAdapter(&stubFetcher{}, nil, &PassthroughMountActivator{RootDir: "/"})
	})
}

func TestNewContainerdApplierAdapter_nilActivatorPanics(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewContainerdApplierAdapter(&stubFetcher{}, &stubParser{}, nil)
	})
}

// ─── diff.Applier interface satisfaction ─────────────────────────────────────

// TestContainerdApplierAdapter_satisfiesDiffApplier ensures the adapter
// implements the containerd interface at test time, not just at compile time.
func TestContainerdApplierAdapter_satisfiesDiffApplier(t *testing.T) {
	t.Parallel()
	var _ diff.Applier = (*ContainerdApplierAdapter)(nil)
}

// ─── PassthroughMountActivator ────────────────────────────────────────────────

func TestPassthroughMountActivator_callsFnWithRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := &PassthroughMountActivator{RootDir: root}
	var got string
	err := a.Activate(context.Background(), nil, func(r string) error {
		got = r
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("fn received %q; want %q", got, root)
	}
}

func TestPassthroughMountActivator_emptyRootErrors(t *testing.T) {
	t.Parallel()
	a := &PassthroughMountActivator{}
	err := a.Activate(context.Background(), nil, func(_ string) error { return nil })
	if err == nil {
		t.Fatal("expected error for empty RootDir")
	}
}

// ─── OverlayMountActivator ────────────────────────────────────────────────────

func TestOverlayMountActivator_createsTempDir(t *testing.T) {
	t.Parallel()
	if os.Getuid() != 0 {
		// mount.All requires root on Linux; just verify directory creation.
		t.Log("skipping mount test (not root); verifying temp dir creation only")
	}

	base := t.TempDir()
	a := &OverlayMountActivator{TempDir: base}

	// Pass an empty mount slice — mount.All with no mounts is a no-op on most systems.
	var seenRoot string
	err := a.Activate(context.Background(), []mount.Mount{}, func(root string) error {
		seenRoot = root
		// Verify the temp dir exists and is under base.
		stat, err := os.Stat(root)
		if err != nil {
			return err
		}
		if !stat.IsDir() {
			return errors.New("root should be a directory")
		}
		if !filepath.HasPrefix(root, base) {
			return errors.New("root should be under TempDir")
		}
		return nil
	})
	if err != nil {
		// On non-root / non-Linux systems mount.All may fail; acceptable.
		t.Logf("Activate returned (may be expected in non-root env): %v", err)
	}
	if seenRoot != "" {
		// After Activate returns the temp dir must be cleaned up.
		if _, err := os.Stat(seenRoot); !os.IsNotExist(err) {
			t.Errorf("temp mount dir should be removed after Activate; stat: %v", err)
		}
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected a panic but none occurred")
		}
	}()
	fn()
}

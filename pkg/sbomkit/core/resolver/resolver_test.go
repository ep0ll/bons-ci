package resolver_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/resolver"
)

// ── FilesystemResolver construction ──────────────────────────────────────────

func TestFilesystemResolver_NilBusAndLogger_DoesNotPanic(t *testing.T) {
	// Construction with nil bus and nil logger must be safe.
	r := resolver.NewFilesystemResolver(nil, nil)
	if r == nil {
		t.Fatal("expected non-nil resolver")
	}
}

func TestFilesystemResolver_NilBus_ResolveDoesNotPanic(t *testing.T) {
	// Even with a nil bus at construction, Resolve must not panic because the
	// resolver defaults to an internal no-op bus.
	dir := t.TempDir()
	r := resolver.NewFilesystemResolver(nil, nil)

	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── BusInjector ───────────────────────────────────────────────────────────────

func TestFilesystemResolver_SetBus_InjectsRealBus(t *testing.T) {
	r := resolver.NewFilesystemResolver(nil, nil)

	// Verify it satisfies BusInjector (compile-time check via the type assertion).
	inj, ok := r.(resolver.BusInjector)
	if !ok {
		t.Fatal("FilesystemResolver must implement resolver.BusInjector")
	}

	bus := event.NewBus(0)
	defer bus.Close()

	inj.SetBus(bus) // must not panic

	var received bool
	bus.Subscribe(event.TopicResolveStarted, func(_ event.Event) error {
		received = true
		return nil
	})

	dir := t.TempDir()
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !received {
		t.Error("expected TopicResolveStarted event to be delivered via the injected bus")
	}
}

func TestImageResolver_SetBus_InjectsRealBus(t *testing.T) {
	r := resolver.NewImageResolver(nil, nil)

	inj, ok := r.(resolver.BusInjector)
	if !ok {
		t.Fatal("ImageResolver must implement resolver.BusInjector")
	}
	bus := event.NewBus(0)
	defer bus.Close()
	inj.SetBus(bus) // must not panic
}

func TestBusInjector_SetBus_NilBusIsNoOp(t *testing.T) {
	// SetBus with a nil argument must be a no-op (not replace a valid bus with nil).
	bus := event.NewBus(0)
	defer bus.Close()

	r := resolver.NewFilesystemResolver(nil, bus)
	inj := r.(resolver.BusInjector)
	inj.SetBus(nil) // must not replace the existing bus with nil

	// Subsequent Resolve should still work (bus is still the non-nil one).
	dir := t.TempDir()
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: dir,
	})
	if err != nil {
		t.Fatalf("unexpected resolve error after nil SetBus: %v", err)
	}
}

// ── FilesystemResolver.Resolve ────────────────────────────────────────────────

func TestFilesystemResolver_Resolve_ExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	r := resolver.NewFilesystemResolver(nil, nil)

	got, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Identifier == "" {
		t.Error("expected non-empty resolved identifier")
	}
}

func TestFilesystemResolver_Resolve_NonExistentPath(t *testing.T) {
	r := resolver.NewFilesystemResolver(nil, nil)

	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: "/non/existent/path/xyz-sbomkit-test",
	})
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
	if !domain.IsKind(err, domain.ErrKindNotFound) {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

func TestFilesystemResolver_Resolve_EmptyIdentifier(t *testing.T) {
	r := resolver.NewFilesystemResolver(nil, nil)

	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: "",
	})
	if err == nil {
		t.Fatal("expected validation error for empty identifier")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestFilesystemResolver_Resolve_FileAsDirectory(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "not-a-dir.txt")
	if err := os.WriteFile(f, []byte("hello"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := resolver.NewFilesystemResolver(nil, nil)
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: f,
	})
	if err == nil {
		t.Fatal("expected validation error for file used as directory")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestFilesystemResolver_Resolve_ArchiveExtension(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "image.tar.gz")
	if err := os.WriteFile(archivePath, []byte("fake"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := resolver.NewFilesystemResolver(nil, nil)
	got, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceArchive,
		Identifier: archivePath,
	})
	if err != nil {
		t.Fatalf("unexpected error for valid archive: %v", err)
	}
	if got.Identifier == "" {
		t.Error("expected non-empty resolved identifier")
	}
}

func TestFilesystemResolver_Resolve_InvalidArchiveExtension(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(csvPath, []byte("a,b"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := resolver.NewFilesystemResolver(nil, nil)
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceArchive,
		Identifier: csvPath,
	})
	if err == nil {
		t.Fatal("expected validation error for .csv as archive")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestFilesystemResolver_Resolve_OCILayout_MissingMarker(t *testing.T) {
	dir := t.TempDir() // no oci-layout file

	r := resolver.NewFilesystemResolver(nil, nil)
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceOCILayout,
		Identifier: dir,
	})
	if err == nil {
		t.Fatal("expected validation error for missing oci-layout marker")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestFilesystemResolver_Resolve_OCILayout_WithMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := resolver.NewFilesystemResolver(nil, nil)
	got, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceOCILayout,
		Identifier: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error for valid OCI layout: %v", err)
	}
	if got.Identifier == "" {
		t.Error("expected non-empty resolved identifier")
	}
}

// ── AllowedRoots ──────────────────────────────────────────────────────────────

func TestFilesystemResolver_AllowedRoots_Permits(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "project")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := resolver.NewFilesystemResolver(nil, nil, resolver.WithAllowedRoots(root))
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: sub,
	})
	if err != nil {
		t.Fatalf("expected sub-path to be permitted: %v", err)
	}
}

func TestFilesystemResolver_AllowedRoots_Blocks(t *testing.T) {
	allowed := t.TempDir()
	blocked := t.TempDir() // different temp dir, outside allowed root

	r := resolver.NewFilesystemResolver(nil, nil, resolver.WithAllowedRoots(allowed))
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: blocked,
	})
	if err == nil {
		t.Fatal("expected auth error for path outside allowed roots")
	}
	if !domain.IsKind(err, domain.ErrKindAuth) {
		t.Errorf("expected auth error, got: %v", err)
	}
}

func TestFilesystemResolver_AllowedRoots_ExactRootPermitted(t *testing.T) {
	root := t.TempDir()

	r := resolver.NewFilesystemResolver(nil, nil, resolver.WithAllowedRoots(root))
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: root, // scanning the root itself
	})
	if err != nil {
		t.Fatalf("expected root itself to be permitted: %v", err)
	}
}

// ── ImageResolver ─────────────────────────────────────────────────────────────

func TestImageResolver_Accepts_OnlyImage(t *testing.T) {
	r := resolver.NewImageResolver(nil, nil)
	if !r.Accepts(domain.SourceImage) {
		t.Error("expected ImageResolver to accept SourceImage")
	}
	for _, k := range []domain.SourceKind{
		domain.SourceDirectory, domain.SourceSnapshot,
		domain.SourceArchive, domain.SourceOCILayout,
	} {
		if r.Accepts(k) {
			t.Errorf("expected ImageResolver to reject %q", k)
		}
	}
}

func TestImageResolver_Resolve_EmptyRef(t *testing.T) {
	r := resolver.NewImageResolver(nil, nil)
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceImage,
		Identifier: "",
	})
	if err == nil {
		t.Fatal("expected validation error for empty image ref")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestImageResolver_Resolve_WhitespaceRef(t *testing.T) {
	r := resolver.NewImageResolver(nil, nil)
	_, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceImage,
		Identifier: "ubuntu 22.04",
	})
	if err == nil {
		t.Fatal("expected validation error for ref with whitespace")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestImageResolver_Resolve_ValidRef_NilCredentials(t *testing.T) {
	r := resolver.NewImageResolver(nil, nil)
	got, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceImage,
		Identifier: "  docker.io/ubuntu:22.04  ", // leading/trailing spaces trimmed
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Identifier != "docker.io/ubuntu:22.04" {
		t.Errorf("expected trimmed identifier, got %q", got.Identifier)
	}
}

func TestImageResolver_Resolve_MirrorSubstitution(t *testing.T) {
	r := resolver.NewImageResolver(nil, nil,
		resolver.WithMirror("docker.io", "mirror.corp:5000"),
	)
	got, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceImage,
		Identifier: "docker.io/ubuntu:22.04",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Identifier != "mirror.corp:5000/ubuntu:22.04" {
		t.Errorf("expected mirror substitution, got %q", got.Identifier)
	}
}

func TestImageResolver_Resolve_NoMirrorForOtherRegistry(t *testing.T) {
	r := resolver.NewImageResolver(nil, nil,
		resolver.WithMirror("docker.io", "mirror.corp:5000"),
	)
	got, err := r.Resolve(context.Background(), domain.Source{
		Kind:       domain.SourceImage,
		Identifier: "ghcr.io/org/image:tag",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Identifier != "ghcr.io/org/image:tag" {
		t.Errorf("expected unchanged identifier, got %q", got.Identifier)
	}
}

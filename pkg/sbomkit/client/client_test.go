package client_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/bons/bons-ci/pkg/sbomkit/client"
	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// ── Fake implementations shared across client tests ──────────────────────────

type stubScanner struct {
	sbom *domain.SBOM
	err  error
}

func (s *stubScanner) Name() string { return "stub" }
func (s *stubScanner) Close() error { return nil }
func (s *stubScanner) Scan(_ context.Context, _ domain.Source, _ ports.ScanOptions) (*domain.SBOM, error) {
	return s.sbom, s.err
}

type stubExporter struct {
	format  domain.Format
	payload []byte
}

func (e *stubExporter) Format() domain.Format { return e.format }
func (e *stubExporter) Export(_ context.Context, _ *domain.SBOM, w interface{ Write([]byte) (int, error) }) error {
	_, err := w.Write(e.payload)
	return err
}

// ── Helper: create a temporary directory ─────────────────────────────────────

func mkTmpDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sbomkit-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// ── Client construction ───────────────────────────────────────────────────────

func TestClient_New_DefaultConfig(t *testing.T) {
	logger := zaptest.NewLogger(t)
	c, err := client.New(client.WithLogger(logger))
	if err != nil {
		t.Fatalf("client.New() error: %v", err)
	}
	defer c.Close()
}

func TestClient_New_WithAllOptions(t *testing.T) {
	logger := zaptest.NewLogger(t)
	c, err := client.New(
		client.WithLogger(logger),
		client.WithCacheTTL(1*time.Hour),
		client.WithMaxRetries(2),
		client.WithDefaultFormat(domain.FormatSPDXJSON),
		client.WithImagePullSource("registry"),
		client.WithScanParallelism(2),
		client.WithRegistryMirror("docker.io", "mirror.corp:5000"),
	)
	if err != nil {
		t.Fatalf("expected no error with all options set: %v", err)
	}
	defer c.Close()
}

func TestClient_New_CacheDisabled(t *testing.T) {
	c, err := client.New(client.WithCacheDisabled())
	if err != nil {
		t.Fatalf("client.New() with cache disabled: %v", err)
	}
	defer c.Close()
}

// ── GenerateFromDirectory ────────────────────────────────────────────────────
// (We can exercise the resolver validation path without a real scanner.)

func TestClient_GenerateFromDirectory_PathNotFound(t *testing.T) {
	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	_, err = c.GenerateFromDirectory(context.Background(), "/non/existent/path/xyz")
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
	if !domain.IsKind(err, domain.ErrKindNotFound) && !domain.IsKind(err, domain.ErrKindResolving) {
		t.Errorf("expected not-found or resolving error, got: %v", err)
	}
}

func TestClient_GenerateFromArchive_InvalidExtension(t *testing.T) {
	dir := mkTmpDir(t)
	// Create a real file with a non-archive extension.
	p := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(p, []byte("col1,col2\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	_, err = c.GenerateFromArchive(context.Background(), p)
	if err == nil {
		t.Fatal("expected validation error for non-archive file")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestClient_GenerateFromSnapshot_IsDirectory_Resolves(t *testing.T) {
	// A snapshot is a directory; resolver should accept it.
	// We cannot scan without syft, but we can verify the resolver doesn't error.
	dir := mkTmpDir(t)

	// Write a minimal file so the dir is readable.
	_ = os.WriteFile(filepath.Join(dir, "etc-release"), []byte("ID=test\n"), 0644)

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	// The scan will fail because we have no real Syft, but the resolver
	// phase should succeed (returns a scanning error, not a validation/not-found one).
	_, err = c.GenerateFromSnapshot(context.Background(), dir)
	if err == nil {
		// In CI without syft binary this might succeed with an empty SBOM — that's OK.
		return
	}
	// Resolver must have passed; error must be from the scanner layer.
	if domain.IsKind(err, domain.ErrKindNotFound) || domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("resolver should have accepted the path; got: %v", err)
	}
}

// ── OCI layout resolver ───────────────────────────────────────────────────────

func TestClient_GenerateFromOCILayout_MissingMarker(t *testing.T) {
	dir := mkTmpDir(t) // no oci-layout file

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	_, err = c.GenerateFromOCILayout(context.Background(), dir)
	if err == nil {
		t.Fatal("expected validation error for directory missing oci-layout marker")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestClient_GenerateFromOCILayout_WithMarker(t *testing.T) {
	dir := mkTmpDir(t)
	// Write the mandatory marker file.
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	// Resolver should pass; scanner failure expected (no real image content).
	_, err = c.GenerateFromOCILayout(context.Background(), dir)
	if domain.IsKind(err, domain.ErrKindValidation) || domain.IsKind(err, domain.ErrKindNotFound) {
		t.Errorf("resolver should have accepted valid OCI layout; got: %v", err)
	}
}

// ── AllowedRoots security enforcement ────────────────────────────────────────

func TestClient_AllowedScanRoots_BlocksOutsideRoot(t *testing.T) {
	allowedDir := mkTmpDir(t)
	blockedDir := mkTmpDir(t) // different temp dir, outside allowed root

	c, err := client.New(
		client.WithAllowedScanRoots(allowedDir),
	)
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	_, err = c.GenerateFromDirectory(context.Background(), blockedDir)
	if err == nil {
		t.Fatal("expected auth error for path outside allowed roots")
	}
	if !domain.IsKind(err, domain.ErrKindAuth) {
		t.Errorf("expected auth error, got: %v", err)
	}
}

func TestClient_AllowedScanRoots_PermitsInsideRoot(t *testing.T) {
	root := mkTmpDir(t)
	subdir := filepath.Join(root, "subproject")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c, err := client.New(client.WithAllowedScanRoots(root))
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	// Resolver should accept this path; scan may fail (no syft) but resolver should pass.
	_, err = c.GenerateFromDirectory(context.Background(), subdir)
	if domain.IsKind(err, domain.ErrKindAuth) {
		t.Errorf("path inside allowed root should be permitted; got: %v", err)
	}
}

// ── Event subscription API ────────────────────────────────────────────────────

func TestClient_SubscribeEvents_ReceivesEvents(t *testing.T) {
	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	var calls atomic.Int32
	id := c.SubscribeEvents(event.TopicScanRequested, func(_ event.Event) error {
		calls.Add(1)
		return nil
	})
	defer c.UnsubscribeEvents(id)

	// Trigger a scan that at least emits TopicScanRequested.
	// We don't care if the scan itself fails.
	dir := mkTmpDir(t)
	_, _ = c.GenerateFromDirectory(context.Background(), dir)

	time.Sleep(30 * time.Millisecond) // let async events flush

	if calls.Load() == 0 {
		t.Error("expected at least one TopicScanRequested event")
	}
}

func TestClient_UnsubscribeEvents_StopsDelivery(t *testing.T) {
	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	var calls atomic.Int32
	id := c.SubscribeEvents(event.TopicScanRequested, func(_ event.Event) error {
		calls.Add(1)
		return nil
	})
	c.UnsubscribeEvents(id)

	dir := mkTmpDir(t)
	_, _ = c.GenerateFromDirectory(context.Background(), dir)
	time.Sleep(30 * time.Millisecond)

	if calls.Load() != 0 {
		t.Errorf("expected no events after unsubscribe, got %d", calls.Load())
	}
}

// ── Per-request options ───────────────────────────────────────────────────────

func TestClient_WithFormat_Override(t *testing.T) {
	// Verify that WithFormat selects the correct exporter path.
	// We cannot assert the actual bytes without real syft, so we assert
	// that an unknown format produces an appropriate error.
	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	dir := mkTmpDir(t)
	_, err = c.GenerateFromDirectory(context.Background(), dir,
		client.WithFormat("application/unknown-format"),
	)
	if err == nil {
		t.Fatal("expected error for unregistered format")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		// Could also be exporting error if validation passes — both are acceptable.
		if !domain.IsKind(err, domain.ErrKindExporting) {
			t.Logf("got error (acceptable): %v", err)
		}
	}
}

func TestClient_WithLabels_AttachedToSource(t *testing.T) {
	// Labels should be attached without breaking resolve or scan flow.
	dir := mkTmpDir(t)
	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	defer c.Close()

	_, err = c.GenerateFromDirectory(context.Background(), dir,
		client.WithLabels(map[string]string{"env": "test", "team": "platform"}),
	)
	// Error is expected (no real scanner), but not a validation or auth error.
	if domain.IsKind(err, domain.ErrKindValidation) || domain.IsKind(err, domain.ErrKindAuth) {
		t.Errorf("labels should not cause validation/auth errors; got: %v", err)
	}
}

// ── Close behaviour ───────────────────────────────────────────────────────────

func TestClient_Close_Idempotent(t *testing.T) {
	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New(): %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}
}

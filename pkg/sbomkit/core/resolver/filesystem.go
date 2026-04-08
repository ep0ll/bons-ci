package resolver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// archiveExtensions is the set of file extensions recognised as archive sources.
var archiveExtensions = map[string]struct{}{
	".tar": {}, ".tar.gz": {}, ".tgz": {},
	".tar.bz2": {}, ".tar.xz": {}, ".tar.zst": {},
}

// FilesystemResolver handles domain.SourceSnapshot, domain.SourceDirectory,
// domain.SourceArchive, and domain.SourceOCILayout.
//
// Responsibilities:
//   - Validate that the path exists and has the expected type (dir vs file).
//   - Resolve symlinks to their targets.
//   - Ensure the process has read access.
//   - Enforce optional allowed-root constraints for multi-tenant safety.
type FilesystemResolver struct {
	logger *zap.Logger
	bus    *event.Bus
	// allowedRoots, if non-empty, restricts scanning to listed prefixes.
	allowedRoots []string
}

// FilesystemResolverOption configures a FilesystemResolver.
type FilesystemResolverOption func(*FilesystemResolver)

// WithAllowedRoots restricts the resolver to only accept paths under the given
// prefixes. This is useful for multi-tenant environments where arbitrary paths
// must not be scanned.
func WithAllowedRoots(roots ...string) FilesystemResolverOption {
	return func(r *FilesystemResolver) { r.allowedRoots = roots }
}

// NewFilesystemResolver constructs a FilesystemResolver.
//
// Both logger and bus may be nil; the resolver will use a no-op logger and a
// closed (no-op) bus in those cases, making direct construction always safe.
// Production callers should inject a real bus via SetBus after engine creation.
func NewFilesystemResolver(logger *zap.Logger, bus *event.Bus, opts ...FilesystemResolverOption) ports.Resolver {
	if logger == nil {
		logger = zap.NewNop()
	}
	if bus == nil {
		bus = event.NewBus(0) // synchronous no-op bus; replaced by SetBus in production
	}
	r := &FilesystemResolver{logger: logger, bus: bus}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Accepts implements ports.Resolver.
func (r *FilesystemResolver) Accepts(kind domain.SourceKind) bool {
	switch kind {
	case domain.SourceSnapshot, domain.SourceDirectory, domain.SourceArchive, domain.SourceOCILayout:
		return true
	}
	return false
}

// Resolve implements ports.Resolver.
//
// Validates that the path exists, is accessible, and matches the expected kind
// (directory vs file). Symlinks are resolved to their real path.
func (r *FilesystemResolver) Resolve(ctx context.Context, src domain.Source) (domain.Source, error) {
	if src.Identifier == "" {
		return src, domain.New(domain.ErrKindValidation, "path identifier must not be empty", nil)
	}

	r.bus.Publish(ctx, event.TopicResolveStarted, event.ScanProgressPayload{
		Stage:   "resolving",
		Message: fmt.Sprintf("validating path %q", src.Identifier),
	}, "")

	// Resolve symlinks to canonical path.
	real, err := filepath.EvalSymlinks(src.Identifier)
	if err != nil {
		if os.IsNotExist(err) {
			return src, domain.Newf(domain.ErrKindNotFound, err, "path not found: %s", src.Identifier)
		}
		return src, domain.Newf(domain.ErrKindResolving, err, "cannot resolve path: %s", src.Identifier)
	}
	real = filepath.Clean(real)

	// Enforce allowed-root constraints.
	if err := r.checkAllowedRoot(real); err != nil {
		return src, err
	}

	// Stat the resolved path.
	info, err := os.Stat(real)
	if err != nil {
		return src, domain.Newf(domain.ErrKindResolving, err, "cannot stat path: %s", real)
	}

	// Validate path type matches source kind.
	if err := r.validateKind(src.Kind, real, info); err != nil {
		return src, err
	}

	// Check read permission with a quick open.
	if err := checkReadable(real, info); err != nil {
		return src, domain.Newf(domain.ErrKindAuth, err, "cannot read path: %s", real)
	}

	enriched := src
	enriched.Identifier = real // use canonical, symlink-resolved path

	r.logger.Info("filesystem source resolved",
		zap.String("original", src.Identifier),
		zap.String("real", real),
		zap.String("kind", string(src.Kind)),
	)

	r.bus.PublishAsync(ctx, event.TopicResolveCompleted, event.ResolveCompletedPayload{
		ResolvedIdentity: real,
	}, "")

	return enriched, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// validateKind checks that the filesystem entry matches the expected SourceKind.
func (r *FilesystemResolver) validateKind(kind domain.SourceKind, path string, info os.FileInfo) error {
	switch kind {
	case domain.SourceDirectory, domain.SourceSnapshot, domain.SourceOCILayout:
		if !info.IsDir() {
			return domain.Newf(domain.ErrKindValidation, nil,
				"source kind %q requires a directory but %q is a file", kind, path)
		}
		if kind == domain.SourceOCILayout {
			return validateOCILayout(path)
		}
	case domain.SourceArchive:
		if info.IsDir() {
			return domain.Newf(domain.ErrKindValidation, nil,
				"source kind %q requires a file but %q is a directory", kind, path)
		}
		ext := archiveExt(path)
		if _, ok := archiveExtensions[ext]; !ok {
			return domain.Newf(domain.ErrKindValidation, nil,
				"unrecognised archive extension %q for %q", ext, path)
		}
	}
	return nil
}

// validateOCILayout checks for the mandatory oci-layout marker file.
func validateOCILayout(dir string) error {
	markerPath := filepath.Join(dir, "oci-layout")
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		return domain.Newf(domain.ErrKindValidation, nil,
			"directory %q does not appear to be an OCI image layout (missing oci-layout file)", dir)
	}
	return nil
}

// checkAllowedRoot enforces the allowedRoots constraint.
//
// Each configured root is canonicalised via EvalSymlinks before comparison.
// If EvalSymlinks fails for a root (e.g. the root itself does not exist), the
// original root string is used as a fallback to avoid silently blocking all
// paths when an allowed-root is merely a symlink target that is temporarily
// unavailable.
func (r *FilesystemResolver) checkAllowedRoot(path string) error {
	if len(r.allowedRoots) == 0 {
		return nil
	}
	for _, root := range r.allowedRoots {
		canonRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			// Fall back to the literal root if symlink resolution fails.
			// This avoids treating a temporarily-unresolvable root as
			// "deny all" and preserves intent.
			canonRoot = filepath.Clean(root)
		}
		if isSubPath(canonRoot, path) {
			return nil
		}
	}
	return domain.Newf(domain.ErrKindAuth, nil,
		"path %q is outside the allowed scan roots", path)
}

// isSubPath returns true if child is equal to or a descendant of parent.
// Both paths must already be clean (no trailing slashes, no symlinks).
func isSubPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// filepath.Rel returns a path beginning with ".." when child is outside
	// parent.  "." means equal.  Any other non-dotdot relative path is inside.
	return rel == "." || (rel != ".." && len(rel) > 0 && rel[0] != '.')
}

// checkReadable attempts to open the path to verify read access.
func checkReadable(path string, info os.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if info.IsDir() {
		_, err = f.Readdirnames(1)
		if err != nil && !isEOF(err) {
			return err
		}
	}
	return nil
}

// archiveExt returns the compound extension of a filename (e.g. ".tar.gz").
// It checks multi-component extensions first so ".tar.gz" is preferred over ".gz".
func archiveExt(path string) string {
	base := filepath.Base(path)
	for ext := range archiveExtensions {
		if len(base) > len(ext) && base[len(base)-len(ext):] == ext {
			return ext
		}
	}
	return filepath.Ext(path)
}

// isEOF reports whether err is io.EOF, which is the expected sentinel returned
// when calling Readdirnames on an empty directory.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}

// Compile-time interface satisfaction check.
var _ ports.Resolver = (*FilesystemResolver)(nil)

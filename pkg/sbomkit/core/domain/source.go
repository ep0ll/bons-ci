package domain

import "strings"

// SourceKind identifies what kind of artifact is being scanned.
type SourceKind string

const (
	// SourceImage is a remote or locally-cached OCI/Docker container image.
	// Identifier: full image reference, e.g. "docker.io/ubuntu:22.04" or "ubuntu@sha256:…"
	SourceImage SourceKind = "image"

	// SourceSnapshot is an extracted container rootfs written to local disk
	// (e.g. a BuildKit snapshot or an overlayfs merge directory).
	// Identifier: absolute path to the rootfs directory.
	SourceSnapshot SourceKind = "snapshot"

	// SourceDirectory is an arbitrary local filesystem directory.
	// Identifier: absolute path.
	SourceDirectory SourceKind = "directory"

	// SourceArchive is a tarball (.tar, .tar.gz, .tar.zst, etc.).
	// Identifier: absolute path to the archive file.
	SourceArchive SourceKind = "archive"

	// SourceOCILayout is an OCI image layout directory (contains oci-layout + index.json).
	// Identifier: absolute path to the layout root.
	SourceOCILayout SourceKind = "oci-layout"
)

// Source describes the artifact to scan.
type Source struct {
	// Kind selects which resolver and scanner mode to use.
	Kind SourceKind
	// Identifier is the canonical reference for this source.
	// Its semantics depend on Kind; see SourceKind constants above.
	Identifier string
	// Platform constrains which platform variant to scan for multi-arch images.
	// nil means "use the host platform or the image's default".
	Platform *Platform
	// Credentials carries authentication for private registries or repositories.
	// nil means unauthenticated access.
	Credentials *Credentials
	// Labels are arbitrary metadata attached by the caller.
	Labels map[string]string
}

// Platform identifies a target OS and CPU architecture.
type Platform struct {
	OS      string // e.g. "linux", "windows"
	Arch    string // e.g. "amd64", "arm64"
	Variant string // e.g. "v7" for linux/arm/v7
}

// String returns the canonical "os/arch" or "os/arch/variant" string.
func (p Platform) String() string {
	if p.Variant != "" {
		return p.OS + "/" + p.Arch + "/" + p.Variant
	}
	return p.OS + "/" + p.Arch
}

// ParsePlatform parses a platform string in "os/arch[/variant]" format.
// Returns an error if the string cannot be parsed.
func ParsePlatform(s string) (*Platform, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil, Newf(ErrKindValidation, nil, "invalid platform %q: expected os/arch[/variant]", s)
	}
	p := &Platform{OS: parts[0], Arch: parts[1]}
	if len(parts) == 3 {
		p.Variant = parts[2]
	}
	return p, nil
}

// Credentials carries registry or private repository authentication.
// Exactly one of (Username+Password), Token, or Keychain should be set.
type Credentials struct {
	// Username and Password for HTTP Basic auth.
	Username string
	Password string
	// Token is used for bearer/token authentication (e.g. Docker Hub PAT).
	Token string
	// Keychain names a credential helper: "ecr", "gcr", "acr", "docker-config".
	Keychain string
}

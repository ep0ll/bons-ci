package gitapply

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─── AnnotationDescriptorParser ──────────────────────────────────────────────

func TestAnnotationDescriptorParser_ParseFetchSpec_valid(t *testing.T) {
	t.Parallel()
	desc := ocispec.Descriptor{
		Annotations: map[string]string{
			AnnotationRemote:           "https://github.com/org/repo.git",
			AnnotationRef:              "main",
			AnnotationChecksum:         "abc1234",
			AnnotationSubdir:           "src/app",
			AnnotationKeepGitDir:       "true",
			AnnotationSkipSubmodules:   "false",
			AnnotationAuthTokenSecret:  "my-token",
			AnnotationAuthHeaderSecret: "my-header",
			AnnotationSSHSocketID:      "default",
			AnnotationKnownSSHHosts:    "github.com ssh-rsa AAAA",
		},
	}
	parser := AnnotationDescriptorParser{}
	spec, err := parser.ParseFetchSpec(desc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "Remote", spec.Remote, "https://github.com/org/repo.git")
	assertEqual(t, "Ref", spec.Ref, "main")
	assertEqual(t, "Checksum", spec.Checksum, "abc1234")
	assertEqual(t, "Subdir", spec.Subdir, "src/app")
	assertEqual(t, "AuthTokenSecret", spec.AuthTokenSecret, "my-token")
	assertEqual(t, "AuthHeaderSecret", spec.AuthHeaderSecret, "my-header")
	assertEqual(t, "SSHSocketID", spec.SSHSocketID, "default")
	assertEqual(t, "KnownSSHHosts", spec.KnownSSHHosts, "github.com ssh-rsa AAAA")
	if !spec.KeepGitDir {
		t.Error("KeepGitDir should be true")
	}
	if spec.SkipSubmodules {
		t.Error("SkipSubmodules should be false")
	}
}

func TestAnnotationDescriptorParser_ParseFetchSpec_missingRemote(t *testing.T) {
	t.Parallel()
	cases := []ocispec.Descriptor{
		{},                                 // nil annotations
		{Annotations: map[string]string{}}, // no remote key
		{Annotations: map[string]string{AnnotationRemote: ""}}, // empty remote
	}
	parser := AnnotationDescriptorParser{}
	for _, desc := range cases {
		if _, err := parser.ParseFetchSpec(desc); err == nil {
			t.Errorf("expected error for descriptor %+v, got nil", desc)
		}
	}
}

func TestAnnotationDescriptorParser_ParseFetchSpec_invalidSpec(t *testing.T) {
	t.Parallel()
	// Remote with an unsupported transport should fail Validate().
	desc := ocispec.Descriptor{
		Annotations: map[string]string{
			AnnotationRemote: "ftp://example.com/repo.git",
		},
	}
	parser := AnnotationDescriptorParser{}
	if _, err := parser.ParseFetchSpec(desc); err == nil {
		t.Error("expected error for invalid transport, got nil")
	}
}

// ─── DescriptorFromFetchSpec ──────────────────────────────────────────────────

func TestDescriptorFromFetchSpec_roundTrip(t *testing.T) {
	t.Parallel()
	original := FetchSpec{
		Remote:           "https://github.com/org/repo.git",
		Ref:              "v1.2.3",
		Checksum:         "deadbeef",
		Subdir:           "tools/cmd",
		KeepGitDir:       true,
		SkipSubmodules:   true,
		AuthTokenSecret:  "tok",
		AuthHeaderSecret: "hdr",
		SSHSocketID:      "agent",
		KnownSSHHosts:    "example.com ecdsa-sha2-nistp256 XXXX",
	}
	desc := DescriptorFromFetchSpec(original)
	if desc.MediaType != MediaTypeGitCommit {
		t.Errorf("MediaType: want %q, got %q", MediaTypeGitCommit, desc.MediaType)
	}
	parsed, err := AnnotationDescriptorParser{}.ParseFetchSpec(desc)
	if err != nil {
		t.Fatalf("round-trip parse failed: %v", err)
	}
	assertEqual(t, "Remote", parsed.Remote, original.Remote)
	assertEqual(t, "Ref", parsed.Ref, original.Ref)
	assertEqual(t, "Checksum", parsed.Checksum, original.Checksum)
	assertEqual(t, "Subdir", parsed.Subdir, original.Subdir)
	assertEqual(t, "AuthTokenSecret", parsed.AuthTokenSecret, original.AuthTokenSecret)
	assertEqual(t, "AuthHeaderSecret", parsed.AuthHeaderSecret, original.AuthHeaderSecret)
	assertEqual(t, "SSHSocketID", parsed.SSHSocketID, original.SSHSocketID)
	assertEqual(t, "KnownSSHHosts", parsed.KnownSSHHosts, original.KnownSSHHosts)
	if parsed.KeepGitDir != original.KeepGitDir {
		t.Errorf("KeepGitDir: want %v, got %v", original.KeepGitDir, parsed.KeepGitDir)
	}
	if parsed.SkipSubmodules != original.SkipSubmodules {
		t.Errorf("SkipSubmodules: want %v, got %v", original.SkipSubmodules, parsed.SkipSubmodules)
	}
}

func TestDescriptorFromFetchSpec_omitsEmpty(t *testing.T) {
	t.Parallel()
	spec := FetchSpec{Remote: "https://github.com/a/b.git"}
	desc := DescriptorFromFetchSpec(spec)
	for _, key := range []string{
		AnnotationRef, AnnotationChecksum, AnnotationSubdir,
		AnnotationKeepGitDir, AnnotationSkipSubmodules,
	} {
		if _, ok := desc.Annotations[key]; ok {
			t.Errorf("annotation %q should be absent when value is zero, but it was present", key)
		}
	}
}

// ─── commitDigest ─────────────────────────────────────────────────────────────

func TestCommitDigest_sha1(t *testing.T) {
	t.Parallel()
	sha := "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	d := commitDigest(sha)
	want := "sha1:" + sha
	if string(d) != want {
		t.Errorf("commitDigest(%q) = %q; want %q", sha, d, want)
	}
}

func TestCommitDigest_sha256(t *testing.T) {
	t.Parallel()
	sha := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	d := commitDigest(sha)
	want := "sha256:" + sha
	if string(d) != want {
		t.Errorf("commitDigest(%q) = %q; want %q", sha, d, want)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: want %q, got %q", field, want, got)
	}
}

package gitapply

import (
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// AnnotationDescriptorParser implements [DescriptorParser] by reading git
// fetch parameters from the OCI descriptor's Annotations map.
//
// It understands the annotation keys defined in this package (see the
// Annotation* constants in spec.go) and maps them to the corresponding
// FetchSpec fields.
//
// All fields except [AnnotationRemote] are optional; missing annotations
// produce zero-value FetchSpec fields (which are valid defaults).
type AnnotationDescriptorParser struct{}

var _ DescriptorParser = AnnotationDescriptorParser{}

// ParseFetchSpec reads the descriptor's Annotations and returns a FetchSpec.
// Returns an error if the remote annotation is absent or the spec fails
// validation.
func (AnnotationDescriptorParser) ParseFetchSpec(desc ocispec.Descriptor) (FetchSpec, error) {
	ann := desc.Annotations
	if ann == nil {
		return FetchSpec{}, fmt.Errorf(
			"gitapply: descriptor has no annotations; %q is required",
			AnnotationRemote,
		)
	}

	remote, ok := ann[AnnotationRemote]
	if !ok || remote == "" {
		return FetchSpec{}, fmt.Errorf(
			"gitapply: annotation %q is missing or empty", AnnotationRemote,
		)
	}

	spec := FetchSpec{
		Remote:           remote,
		Ref:              ann[AnnotationRef],
		Checksum:         ann[AnnotationChecksum],
		Subdir:           ann[AnnotationSubdir],
		AuthTokenSecret:  ann[AnnotationAuthTokenSecret],
		AuthHeaderSecret: ann[AnnotationAuthHeaderSecret],
		SSHSocketID:      ann[AnnotationSSHSocketID],
		KnownSSHHosts:    ann[AnnotationKnownSSHHosts],
		KeepGitDir:       strings.EqualFold(ann[AnnotationKeepGitDir], "true"),
		SkipSubmodules:   strings.EqualFold(ann[AnnotationSkipSubmodules], "true"),
	}

	if err := spec.Validate(); err != nil {
		return FetchSpec{}, fmt.Errorf("gitapply: invalid spec from descriptor: %w", err)
	}
	return spec, nil
}

// DescriptorFromFetchSpec builds an OCI descriptor whose Annotations encode
// the supplied FetchSpec.  The returned descriptor has no Digest or Size; the
// caller is responsible for setting those after the content is applied.
// This is the inverse of [AnnotationDescriptorParser.ParseFetchSpec].
func DescriptorFromFetchSpec(spec FetchSpec) ocispec.Descriptor {
	ann := map[string]string{
		AnnotationRemote: spec.Remote,
	}
	setIfNonEmpty(ann, AnnotationRef, spec.Ref)
	setIfNonEmpty(ann, AnnotationChecksum, spec.Checksum)
	setIfNonEmpty(ann, AnnotationSubdir, spec.Subdir)
	setIfNonEmpty(ann, AnnotationAuthTokenSecret, spec.AuthTokenSecret)
	setIfNonEmpty(ann, AnnotationAuthHeaderSecret, spec.AuthHeaderSecret)
	setIfNonEmpty(ann, AnnotationSSHSocketID, spec.SSHSocketID)
	setIfNonEmpty(ann, AnnotationKnownSSHHosts, spec.KnownSSHHosts)
	if spec.KeepGitDir {
		ann[AnnotationKeepGitDir] = "true"
	}
	if spec.SkipSubmodules {
		ann[AnnotationSkipSubmodules] = "true"
	}
	return ocispec.Descriptor{
		MediaType:   MediaTypeGitCommit,
		Annotations: ann,
	}
}

func setIfNonEmpty(m map[string]string, key, value string) {
	if value != "" {
		m[key] = value
	}
}

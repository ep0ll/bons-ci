// Package builder provides a fluent, high-level API for constructing fully
// signed SLSA attestations in a single call chain. It composes the lower-level
// provenance, attestation, signing, and policy packages.
//
// Typical usage:
//
//	kp, _ := signing.GenerateECDSAKeyPair("my-key")
//
//	env, err := builder.New().
//	    WithLevel(types.Level3).
//	    WithBuilder("https://ci.example.com/builder/v1", nil).
//	    WithInvocationID("run-42").
//	    WithGitSource("https://github.com/owner/repo.git", "abc123").
//	    AddSubject("registry.example.com/myapp:1.0", map[string]string{"sha256": "deadbeef"}).
//	    Sign(kp.Signer)
package builder

import (
	"time"

	"github.com/bons/bons-ci/pkg/slsa/attestation"
	"github.com/bons/bons-ci/pkg/slsa/provenance"
	"github.com/bons/bons-ci/pkg/slsa/signing"
	"github.com/bons/bons-ci/pkg/slsa/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Builder
// ─────────────────────────────────────────────────────────────────────────────

// Builder is a fluent helper for constructing SLSA provenance attestations.
// All With* / Add* methods return the receiver for chaining.
// The first error encountered is stored and returned when Sign / BuildPredicate
// is called, so callers can chain freely and check once at the end.
type Builder struct {
	level        types.Level
	builderID    string
	builderVer   map[string]string
	invocationID string
	buildType    string
	started      time.Time
	finished     time.Time
	reproducible bool
	hermeticOver *bool // nil = derive from capture
	customEnv    map[string]any
	buildConfig  *types.BuildConfig
	subjects     []types.Subject
	capture      *provenance.Capture
	firstErr     error
}

// New returns a Builder with sensible defaults (SLSA L1, generic build type).
func New() *Builder {
	return &Builder{
		level:     types.Level1,
		buildType: types.BuildTypeGenericV1,
		capture:   &provenance.Capture{},
	}
}

// ─── Level / identifiers ──────────────────────────────────────────────────────

// WithLevel sets the target SLSA level (used for documentation only; compliance
// is enforced by the policy passed to the verifier).
func (b *Builder) WithLevel(l types.Level) *Builder {
	b.level = l
	return b
}

// WithBuilder sets the builder URI and optional version map.
func (b *Builder) WithBuilder(id string, version map[string]string) *Builder {
	b.builderID = id
	b.builderVer = version
	return b
}

// WithInvocationID sets a unique build-invocation identifier.
func (b *Builder) WithInvocationID(id string) *Builder {
	b.invocationID = id
	return b
}

// WithBuildType overrides the default build-type URI.
func (b *Builder) WithBuildType(bt string) *Builder {
	b.buildType = bt
	return b
}

// ─── Timestamps ───────────────────────────────────────────────────────────────

// WithTimes records the build start and finish timestamps explicitly.
// If not called, both are set to time.Now() when BuildPredicate is called.
func (b *Builder) WithTimes(started, finished time.Time) *Builder {
	b.started = started
	b.finished = finished
	return b
}

// ─── Build flags ──────────────────────────────────────────────────────────────

// WithReproducible marks the build as reproducible (SLSA L4 requirement).
func (b *Builder) WithReproducible(v bool) *Builder {
	b.reproducible = v
	return b
}

// WithHermetic overrides the hermetic flag. By default it is derived
// automatically: a build is hermetic when there is no network access, no
// incomplete materials, and no local sources.
func (b *Builder) WithHermetic(v bool) *Builder {
	b.hermeticOver = &v
	return b
}

// ─── Internal parameters ──────────────────────────────────────────────────────

// WithCustomEnv merges key-value pairs into InternalParameters.CustomEnv.
func (b *Builder) WithCustomEnv(env map[string]any) *Builder {
	if b.customEnv == nil {
		b.customEnv = make(map[string]any, len(env))
	}
	for k, v := range env {
		b.customEnv[k] = v
	}
	return b
}

// WithBuildConfig embeds the resolved build graph into InternalParameters.
func (b *Builder) WithBuildConfig(bc *types.BuildConfig) *Builder {
	b.buildConfig = bc
	return b
}

// ─── Subjects ─────────────────────────────────────────────────────────────────

// WithSubjects replaces the subject list.
func (b *Builder) WithSubjects(subjects []types.Subject) *Builder {
	b.subjects = subjects
	return b
}

// AddSubject appends a single subject artifact.
func (b *Builder) AddSubject(name string, digest map[string]string) *Builder {
	b.subjects = append(b.subjects, types.Subject{Name: name, Digest: digest})
	return b
}

// ─── Sources ──────────────────────────────────────────────────────────────────

// WithGitSource records a Git repository as a build input.
func (b *Builder) WithGitSource(url, commit string) *Builder {
	b.capture.AddGit(types.GitSource{URL: url, Commit: commit})
	return b
}

// WithGitSourceRef records a Git repository with an explicit ref (branch/tag).
func (b *Builder) WithGitSourceRef(url, commit, ref string) *Builder {
	b.capture.AddGit(types.GitSource{URL: url, Commit: commit, Ref: ref})
	return b
}

// WithImageSource records a container image as a build input.
func (b *Builder) WithImageSource(ref string) *Builder {
	b.capture.AddImage(types.ImageSource{Ref: ref})
	return b
}

// WithImageSourceDigest records a container image with a resolved digest.
func (b *Builder) WithImageSourceDigest(ref, digest string) *Builder {
	b.capture.AddImage(types.ImageSource{Ref: ref, Digest: digest})
	return b
}

// WithHTTPSource records an HTTP artifact with its digest.
func (b *Builder) WithHTTPSource(url, digest string) *Builder {
	b.capture.AddHTTP(types.HTTPSource{URL: url, Digest: digest})
	return b
}

// WithLocalSource records a named local build context (makes the build non-hermetic).
func (b *Builder) WithLocalSource(name string) *Builder {
	b.capture.AddLocal(types.LocalSource{Name: name})
	return b
}

// WithNetworkAccess records that the build required network access.
func (b *Builder) WithNetworkAccess() *Builder {
	b.capture.NetworkAccess = true
	return b
}

// WithIncompleteMaterials flags that the dependency graph is incomplete.
func (b *Builder) WithIncompleteMaterials() *Builder {
	b.capture.IncompleteMaterials = true
	return b
}

// ─── Frontend / args ──────────────────────────────────────────────────────────

// WithFrontend sets the build frontend name (e.g. "dockerfile.v0") and
// optional build arguments.
func (b *Builder) WithFrontend(frontend string, args map[string]string) *Builder {
	b.capture.Frontend = frontend
	if args != nil {
		b.capture.Args = args
	}
	return b
}

// ─── Secrets / SSH ────────────────────────────────────────────────────────────

// WithSecret records a secret mounted during the build.
func (b *Builder) WithSecret(id string, optional bool) *Builder {
	b.capture.AddSecret(types.Secret{ID: id, Optional: optional})
	return b
}

// WithSSH records an SSH agent socket used during the build.
func (b *Builder) WithSSH(id string, optional bool) *Builder {
	b.capture.AddSSH(types.SSH{ID: id, Optional: optional})
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Build / Sign
// ─────────────────────────────────────────────────────────────────────────────

// BuildPredicate constructs the SLSA v1 predicate without signing it.
// Returns the first error encountered during chaining, if any.
func (b *Builder) BuildPredicate() (*provenance.PredicateV1, error) {
	if b.firstErr != nil {
		return nil, b.firstErr
	}

	b.capture.Sort()
	if err := b.capture.OptimizeImageSources(); err != nil {
		return nil, err
	}

	pred, err := provenance.NewPredicateV1(b.capture)
	if err != nil {
		return nil, err
	}

	pred.SetBuildType(b.buildType)

	if b.builderID != "" {
		pred.SetBuilder(b.builderID, b.builderVer)
	}
	if b.invocationID != "" {
		pred.SetInvocationID(b.invocationID)
	}

	started := b.started
	if started.IsZero() {
		started = time.Now().UTC()
	}
	finished := b.finished
	if finished.IsZero() {
		finished = time.Now().UTC()
	}
	pred.SetTimes(started, finished)

	if b.reproducible {
		pred.SetReproducible(true)
	}

	// Apply hermetic override if set.
	if b.hermeticOver != nil {
		pred.SetHermetic(*b.hermeticOver)
	}

	if len(b.customEnv) > 0 {
		pred.SetCustomEnv(b.customEnv)
	}
	if b.buildConfig != nil {
		pred.SetBuildConfig(b.buildConfig)
	}

	return pred, nil
}

// BuildStatement constructs the in-toto Statement without signing.
func (b *Builder) BuildStatement() (*attestation.Statement, error) {
	if len(b.subjects) == 0 {
		return nil, errorf("at least one subject is required")
	}
	pred, err := b.BuildPredicate()
	if err != nil {
		return nil, err
	}
	return attestation.NewStatement(types.PredicateSLSAProvenanceV1, b.subjects, pred)
}

// Sign builds the predicate, wraps it in an in-toto statement, and produces
// a DSSE envelope signed by every provided signer.
//
// At least one signer is required. Multiple signers produce a multi-signature
// envelope (useful for co-signing scenarios).
func (b *Builder) Sign(signers ...signing.Signer) (*signing.Envelope, error) {
	if len(signers) == 0 {
		return nil, errorf("at least one signer is required")
	}
	stmt, err := b.BuildStatement()
	if err != nil {
		return nil, err
	}
	payload, err := stmt.Marshal()
	if err != nil {
		return nil, err
	}
	env := signing.StatementEnvelope(payload)
	if len(signers) == 1 {
		if err := env.AddSignature(signers[0]); err != nil {
			return nil, err
		}
	} else {
		ms := signing.NewMultiSigner(signers...)
		if err := ms.AddSignatures(env); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// SignV02 builds the predicate, converts it to SLSA v0.2, and signs the result.
// Use this for compatibility with legacy consumers.
func (b *Builder) SignV02(signers ...signing.Signer) (*signing.Envelope, error) {
	if len(signers) == 0 {
		return nil, errorf("at least one signer is required")
	}
	if len(b.subjects) == 0 {
		return nil, errorf("at least one subject is required")
	}
	pred, err := b.BuildPredicate()
	if err != nil {
		return nil, err
	}
	v02 := provenance.ConvertToV02(pred)
	stmt, err := attestation.NewStatement(types.PredicateSLSAProvenanceV02, b.subjects, v02)
	if err != nil {
		return nil, err
	}
	payload, err := stmt.Marshal()
	if err != nil {
		return nil, err
	}
	env := signing.StatementEnvelope(payload)
	if len(signers) == 1 {
		if err := env.AddSignature(signers[0]); err != nil {
			return nil, err
		}
	} else {
		ms := signing.NewMultiSigner(signers...)
		if err := ms.AddSignatures(env); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// ─── Advanced access ──────────────────────────────────────────────────────────

// Capture returns the underlying provenance.Capture for advanced callers that
// need to record sources not covered by the With* helpers.
func (b *Builder) Capture() *provenance.Capture { return b.capture }

// Level returns the configured target SLSA level.
func (b *Builder) Level() types.Level { return b.level }

// ─── private ──────────────────────────────────────────────────────────────────

type builderError string

func (e builderError) Error() string { return "builder: " + string(e) }

func errorf(msg string) error { return builderError(msg) }

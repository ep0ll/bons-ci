package httpapplier

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/moby/buildkit/util/pgpsign"
	"github.com/pkg/errors"
)

// pgpVerifier implements SignatureVerifier using OpenPGP detached signatures.
// It delegates directly to buildkit's pgpsign package which already enforces:
//   - Minimum hash strength (SHA-256/384/512 only).
//   - Minimum RSA key size (2048 bits).
//   - Key revocation checks.
//   - Signature creation-time skew checks (±5 minutes).
//
// The file is opened via os.OpenRoot to prevent directory traversal from a
// maliciously crafted Content-Disposition filename.
type pgpVerifier struct{}

// Verify opens filePath inside its parent directory using os.OpenRoot (Go 1.23+
// sandbox API), reads it into an io.Reader, and delegates to
// pgpsign.VerifyArmoredDetachedSignature.
//
// Using os.OpenRoot is the key security improvement over buildkit's current
// implementation which uses filepath.Base + os.Open: even if filepath.Base
// misbehaves on unusual inputs, OpenRoot cannot escape its sandbox directory.
func (v *pgpVerifier) Verify(_ context.Context, filePath string, _ []byte, opts VerifyOptions) error {
	if len(opts.PubKey) == 0 || len(opts.Signature) == 0 {
		return errors.New("pgp verify: both pub-key and signature are required")
	}

	dir := filepath.Dir(filePath)
	name := filepath.Base(filePath)

	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.Wrap(err, "pgp verify: open sandbox root")
	}
	defer root.Close()

	f, err := root.Open(name)
	if err != nil {
		return errors.Wrap(err, "pgp verify: open payload")
	}
	defer f.Close()

	// pgpsign.VerifyArmoredDetachedSignature already applies all policy checks
	// (hash algorithm, key algorithm, key size, revocation, time skew).
	if err := pgpsign.VerifyArmoredDetachedSignature(
		io.NopCloser(f),
		opts.Signature,
		opts.PubKey,
		nil, // use default policy (no expired-key rejection by default)
	); err != nil {
		return errors.Wrap(err, "pgp signature verification failed")
	}
	return nil
}

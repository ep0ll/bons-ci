package writer

import (
	"fmt"

	"github.com/opencontainers/go-digest"
)

const DefaultBlobsPrefix    = "blobs/"

func DigestToPath(dgst digest.Digest) (string, error) {
	if err := dgst.Validate(); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s/%s/%s", DefaultBlobsPrefix, dgst.Algorithm(), dgst.Encoded()), nil
}

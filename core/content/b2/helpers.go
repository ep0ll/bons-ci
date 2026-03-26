package b2

import (
	"fmt"
	"strings"

	digest "github.com/opencontainers/go-digest"
)

// digestFromPath extracts a digest from an S3 object key that contains
// the tenant-prefixed blob path: {tenant}/{blobsPrefix}/{algorithm}/{encoded}
func digestFromPath(path string, prefixer object_folder) (digest.Digest, error) {
	trimmed := prefixer.Trim(path)
	if trimmed == "" {
		return "", fmt.Errorf("invalid blob path: %s", path)
	}

	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid blob path after prefix trim: %s", path)
	}

	return digest.Parse(fmt.Sprintf("%s:%s", parts[0], parts[1]))
}

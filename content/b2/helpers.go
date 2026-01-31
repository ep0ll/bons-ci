package b2

import (
	"fmt"
	"strings"

	digest "github.com/opencontainers/go-digest"
)

func digestFromPath(path string) (digest.Digest, error) {
	parts := strings.Split(strings.TrimPrefix(path, default_blobs_prefix), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid blob path: %s", path)
	}
	return digest.Parse(fmt.Sprintf("%s:%s", parts[0], parts[1]))
}

package llb

import (
	"github.com/containerd/platforms"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// defaultPlatformSpec returns the normalized default platform for the current host.
func defaultPlatformSpec() ocispecs.Platform {
	return platforms.Normalize(platforms.DefaultSpec())
}

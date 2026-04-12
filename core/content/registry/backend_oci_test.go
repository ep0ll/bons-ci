package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOCIHost_Extraction(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"docker.io/library/nginx:latest", "docker.io"},
		{"quay.io/prometheus/node-exporter", "quay.io"},
		{"ghcr.io/owner/repo@sha256:abc", "ghcr.io"},
		{"registry.k8s.io/pause:3.9", "registry.k8s.io"},
		{"localhost:5000/myimage", "localhost"},
		{"singleword", "singleword"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got := ociHost(tc.ref)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNewOCIBackend_ImplementsInterface(t *testing.T) {
	// NewOCIBackend requires real containerd at runtime; just verify the
	// return type satisfies the interface at compile time.
	var _ RegistryBackend = (*ociBackend)(nil)
}

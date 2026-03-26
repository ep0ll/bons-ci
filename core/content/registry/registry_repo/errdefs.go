package registry_repo

import "errors"

var (
	ErrInvalidRegistryRef     = errors.New("registry reference is invalid")
	ErrRegistryRefExists      = errors.New("registry reference already exists")
	ErrInvalidRegistry        = errors.New("invalid oci registry")
	ErrRegistryNotFound       = errors.New("registry not found")
	ErrRegistryCreationFailed = errors.New("failed to create registry")
)

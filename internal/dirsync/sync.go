package differ

import (
	"errors"
	"sync"
)

// noCopy is a sync.Mutex wrapper that prevents accidental value-copy of types
// that embed it. go vet catches struct copies that include a sync.Mutex.
// Embedding noCopy instead of sync.Mutex keeps the Mutex unexported while
// still triggering the vet warning.
type noCopy struct{ sync.Mutex }

// joinErrors joins a slice of errors using errors.Join.
// Returns nil when errs is empty or contains only nil values.
// This is intentionally a package-private free function — it must use
// errors.Join (lossless) rather than any concatenation approach that could
// silently drop later errors.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

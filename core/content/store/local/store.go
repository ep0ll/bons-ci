// Package local provides a content store backed by the local filesystem,
// delegating to github.com/containerd/containerd's proven local store
// implementation.
package local

import "github.com/containerd/containerd/v2/plugins/content/local"

// NewStore creates a new local filesystem-backed content store rooted at root.
// Equivalent to containerd's local.NewStore.
var NewStore = local.NewStore

// LabelStore is a content store that additionally manages labels on content.
type LabelStore = local.LabelStore

// NewLabelStore creates a local store that supports label-based metadata.
var NewLabelStore = local.NewLabeledStore

// OpenReader opens a read-only handle to content identified by its path.
var OpenReader = local.OpenReader

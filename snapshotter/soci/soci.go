//go:build linux

package soci

import (
	"github.com/awslabs/soci-snapshotter/snapshot"
	"github.com/awslabs/soci-snapshotter/soci"
)

var NewSnapshotter = snapshot.NewSnapshotter
type Converter = soci.Index

//go:build linux

package soci

import (
	"github.com/awslabs/soci-snapshotter/snapshot"
	// "github.com/awslabs/soci-snapshotter/snapshot/soci"
)

var NewSnapshotter = snapshot.NewSnapshotter
// var Converter = soci.ImageBuilder

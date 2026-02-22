package estargz

import "github.com/containerd/containerd/v2/core/diff"

type Differ struct {
	diff.Applier
	diff.Comparer
}

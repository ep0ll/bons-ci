package local

import "github.com/containerd/containerd/v2/plugins/content/local"

var NewStore = local.NewStore

type LabelStore = local.LabelStore

var NewLabelStore = local.NewLabeledStore
var OpenReader = local.OpenReader

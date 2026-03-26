package reader

import (
	"github.com/containerd/containerd/v2/core/content"
	"github.com/minio/minio-go/v7"
)

func NewReader(obj *minio.Object, stat minio.ObjectInfo) content.ReaderAt {
	return &reader{
		Object: obj,
		stat:   stat,
	}
}

type reader struct {
	*minio.Object
	stat minio.ObjectInfo
}

func (r *reader) Size() int64 {
	return r.stat.Size
}

package compression

import "bytes"

type matcher = func([]byte) bool

func magicNumberMatcher(m []byte) matcher {
	return func(source []byte) bool {
		return bytes.HasPrefix(source, m)
	}
}

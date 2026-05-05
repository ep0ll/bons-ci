package merkle_test

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/merkle"
)

func BenchmarkMerkleTree_Seal(b *testing.B) {
	for _, n := range []int{10, 100, 1000, 10000} {
		n := n
		b.Run(fmt.Sprintf("leaves=%d", n), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				reg := merkle.NewRegistry()
				d := layer.Digest("bench")
				hash := make([]byte, 32)
				for j := 0; j < n; j++ {
					rand.Read(hash) //nolint:errcheck
					reg.AddLeaf(d, fmt.Sprintf("/bench/file/%d", j), hash)
				}
				b.StartTimer()
				reg.Seal(d)
			}
		})
	}
}

func BenchmarkMerkleTree_Proof(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		n := n
		b.Run(fmt.Sprintf("leaves=%d", n), func(b *testing.B) {
			reg := merkle.NewRegistry()
			d := layer.Digest("bench")
			hash := make([]byte, 32)
			for j := 0; j < n; j++ {
				rand.Read(hash) //nolint:errcheck
				reg.AddLeaf(d, fmt.Sprintf("/bench/file/%d", j), append([]byte(nil), hash...))
			}
			reg.Seal(d)
			target := "/bench/file/0"
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = reg.Proof(d, target)
			}
		})
	}
}

package access

import (
	"math"
	"sync"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// BloomFilter is a space-efficient probabilistic set membership tester.
// Uses Kirsch-Mitzenmacher dual hashing for O(1) per-query cost.
type BloomFilter struct {
	mu   sync.Mutex
	bits []uint64
	k    uint
	m    uint
	n    uint
	mask uint64
}

// NewBloomFilter creates a bloom filter sized for expectedItems at fpRate.
func NewBloomFilter(expectedItems uint, fpRate float64) *BloomFilter {
	if expectedItems == 0 {
		expectedItems = 1024
	}
	if fpRate <= 0 || fpRate >= 1 {
		fpRate = 0.001
	}

	mf := -float64(expectedItems) * math.Log(fpRate) / (math.Ln2 * math.Ln2)
	m := uint(mf)

	p := uint(1)
	for p < m {
		p <<= 1
	}
	m = p

	k := uint(float64(m) / float64(expectedItems) * math.Ln2)
	if k < 1 {
		k = 1
	}

	words := m / 64
	if words == 0 {
		words = 1
	}

	return &BloomFilter{
		bits: make([]uint64, words),
		k:    k,
		m:    m,
		mask: uint64(m - 1),
	}
}

// Test reports whether the (layerID, path) pair might be in the filter.
func (bf *BloomFilter) Test(layerID core.LayerID, path string) bool {
	h1, h2 := bf.dualHash(layerID, path)

	bf.mu.Lock()
	defer bf.mu.Unlock()

	for i := uint(0); i < bf.k; i++ {
		pos := (h1 + uint64(i)*h2) & bf.mask
		if bf.bits[pos/64]&(1<<(pos%64)) == 0 {
			return false
		}
	}
	return true
}

// Add inserts a (layerID, path) pair into the filter.
func (bf *BloomFilter) Add(layerID core.LayerID, path string) {
	h1, h2 := bf.dualHash(layerID, path)

	bf.mu.Lock()
	defer bf.mu.Unlock()

	for i := uint(0); i < bf.k; i++ {
		pos := (h1 + uint64(i)*h2) & bf.mask
		bf.bits[pos/64] |= 1 << (pos % 64)
	}
	bf.n++
}

// TestAndAdd atomically tests and adds. Returns true if already present.
func (bf *BloomFilter) TestAndAdd(layerID core.LayerID, path string) bool {
	h1, h2 := bf.dualHash(layerID, path)

	bf.mu.Lock()
	defer bf.mu.Unlock()

	present := true
	for i := uint(0); i < bf.k; i++ {
		pos := (h1 + uint64(i)*h2) & bf.mask
		word, bit := pos/64, pos%64
		if bf.bits[word]&(1<<bit) == 0 {
			present = false
		}
		bf.bits[word] |= 1 << bit
	}
	bf.n++
	return present
}

// Reset clears the filter for a new session.
func (bf *BloomFilter) Reset() {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	for i := range bf.bits {
		bf.bits[i] = 0
	}
	bf.n = 0
}

// Count returns inserted item count.
func (bf *BloomFilter) Count() uint {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	return bf.n
}

// FillRatio returns the proportion of set bits (0.0 to 1.0).
func (bf *BloomFilter) FillRatio() float64 {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	set := uint64(0)
	for _, word := range bf.bits {
		set += uint64(popcount(word))
	}
	return float64(set) / float64(bf.m)
}

func (bf *BloomFilter) dualHash(layerID core.LayerID, path string) (uint64, uint64) {
	key := layerID.String() + "\x00" + path
	const (
		offset1 = 14695981039346656037
		offset2 = 2166136261
		prime   = 1099511628211
	)
	h1, h2 := uint64(offset1), uint64(offset2)
	for i := 0; i < len(key); i++ {
		c := uint64(key[i])
		h1 ^= c
		h1 *= prime
		h2 ^= c
		h2 *= prime
	}
	return h1, h2
}

func popcount(x uint64) int {
	count := 0
	for x != 0 {
		x &= x - 1
		count++
	}
	return count
}

package pool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGet_CapSufficient(t *testing.T) {
	sizes := []int{0, 1, 512, 513, 1023, 1024, DefaultSize, 1 << 20, 4 << 20}
	for _, sz := range sizes {
		b := Get(sz)
		require.NotNil(t, b, "size=%d", sz)
		if sz > 0 {
			assert.GreaterOrEqual(t, cap(*b), sz, "cap should be >= requested size %d", sz)
		}
		assert.Equal(t, cap(*b), len(*b), "len must equal cap after Get")
		Put(b)
	}
}

func TestGet_ZeroSize(t *testing.T) {
	b := Get(0)
	assert.GreaterOrEqual(t, cap(*b), 1<<minShift)
	Put(b)
}

func TestPut_NilPointer(t *testing.T) {
	assert.NotPanics(t, func() { Put(nil) })
}

func TestPut_NilSlice(t *testing.T) {
	p := new([]byte)
	assert.NotPanics(t, func() { Put(p) })
}

func TestPut_OversizedSlice(t *testing.T) {
	b := make([]byte, 8<<20) // > maxShift: not pooled
	assert.NotPanics(t, func() { Put(&b) })
}

func TestPut_NonPowerOf2Cap(t *testing.T) {
	b := make([]byte, 777)
	assert.NotPanics(t, func() { Put(&b) })
}

func TestRoundTrip_ReusesBuffer(t *testing.T) {
	b1 := Get(DefaultSize)
	(*b1)[0] = 0xDE
	(*b1)[1] = 0xAD
	Put(b1)

	// Pool may return the same buffer; either way we just need no panic + valid cap.
	b2 := Get(DefaultSize)
	assert.GreaterOrEqual(t, cap(*b2), DefaultSize)
	Put(b2)
}

func TestGet_WriteAndRead(t *testing.T) {
	payload := []byte("hello pool")
	b := Get(len(payload))
	n := copy(*b, payload)
	assert.Equal(t, len(payload), n)
	assert.Equal(t, payload, (*b)[:n])
	Put(b)
}

func BenchmarkGet_DefaultSize(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := Get(DefaultSize)
		Put(buf)
	}
}

func BenchmarkGet_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := Get(DefaultSize)
			Put(buf)
		}
	})
}

func BenchmarkGet_SmallSize(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := Get(512)
			Put(buf)
		}
	})
}

func BenchmarkGet_LargeSize(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := Get(1 << 20)
			Put(buf)
		}
	})
}

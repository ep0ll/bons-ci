package registry

import (
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeInfo(dgst digest.Digest, size int64) content.Info {
	return content.Info{Digest: dgst, Size: size}
}

func TestInfoCache_GetSet(t *testing.T) {
	c := newInfoCache(5 * time.Minute)
	dgst := digest.FromBytes([]byte("test"))
	info := makeInfo(dgst, 42)

	c.Set(dgst, info)
	got, ok := c.Get(dgst)
	require.True(t, ok)
	assert.Equal(t, info, got)
}

func TestInfoCache_Miss(t *testing.T) {
	c := newInfoCache(5 * time.Minute)
	_, ok := c.Get(digest.FromBytes([]byte("missing")))
	assert.False(t, ok)
}

func TestInfoCache_Expiry(t *testing.T) {
	c := newInfoCache(10 * time.Millisecond)
	dgst := digest.FromBytes([]byte("ttl"))
	c.Set(dgst, makeInfo(dgst, 10))

	_, ok := c.Get(dgst)
	require.True(t, ok, "should be present before TTL")

	time.Sleep(30 * time.Millisecond)
	_, ok = c.Get(dgst)
	assert.False(t, ok, "should be expired after TTL")
}

func TestInfoCache_Delete(t *testing.T) {
	c := newInfoCache(5 * time.Minute)
	dgst := digest.FromBytes([]byte("del"))
	c.Set(dgst, makeInfo(dgst, 1))
	c.Delete(dgst)
	_, ok := c.Get(dgst)
	assert.False(t, ok)
}

func TestInfoCache_Flush(t *testing.T) {
	c := newInfoCache(5 * time.Minute)
	for i := 0; i < 10; i++ {
		dgst := digest.FromBytes([]byte{byte(i)})
		c.Set(dgst, makeInfo(dgst, int64(i)))
	}
	c.Flush()
	for i := 0; i < 10; i++ {
		_, ok := c.Get(digest.FromBytes([]byte{byte(i)}))
		assert.False(t, ok, "entry %d should be flushed", i)
	}
}

func TestInfoCache_ConcurrentAccess(t *testing.T) {
	c := newInfoCache(5 * time.Minute)
	const goroutines = 200
	const entries = 50

	// Pre-seed entries.
	digests := make([]digest.Digest, entries)
	for i := range digests {
		digests[i] = digest.FromBytes([]byte{byte(i)})
		c.Set(digests[i], makeInfo(digests[i], int64(i)))
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			dgst := digests[i%entries]
			switch i % 3 {
			case 0:
				c.Get(dgst)
			case 1:
				c.Set(dgst, makeInfo(dgst, int64(i)))
			case 2:
				c.Delete(dgst)
			}
		}()
	}
	wg.Wait()
}

func BenchmarkInfoCache_Get_Parallel(b *testing.B) {
	c := newInfoCache(5 * time.Minute)
	dgst := digest.FromBytes([]byte("bench"))
	c.Set(dgst, makeInfo(dgst, 1024))

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Get(dgst)
		}
	})
}

func BenchmarkInfoCache_Set(b *testing.B) {
	c := newInfoCache(5 * time.Minute)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			dgst := digest.FromBytes([]byte{byte(i), byte(i >> 8)})
			c.Set(dgst, makeInfo(dgst, int64(i)))
			i++
		}
	})
}

package dirsync_test

import (
	"sync"
	"testing"

	"github.com/bons/bons-ci/internal/dirsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufPool_GetPut_DefaultSize(t *testing.T) {
	t.Parallel()
	p := dirsync.NewBufPool(0) // 0 → defaultBufSize (64 KiB)
	buf := p.Get()
	require.NotNil(t, buf)
	assert.Equal(t, 64*1024, len(*buf))
	p.Put(buf)
}

func TestBufPool_GetPut_CustomSize(t *testing.T) {
	t.Parallel()
	p := dirsync.NewBufPool(4096)
	buf := p.Get()
	require.NotNil(t, buf)
	assert.Equal(t, 4096, len(*buf))
	p.Put(buf)
}

func TestBufPool_GetPut_ReusesBuffer(t *testing.T) {
	t.Parallel()
	p := dirsync.NewBufPool(1024)
	b1 := p.Get()
	p.Put(b1)
	b2 := p.Get()
	// Go's sync.Pool may or may not return the same pointer, but it must
	// never panic and must return a valid buffer.
	require.NotNil(t, b2)
	assert.Equal(t, 1024, len(*b2))
	p.Put(b2)
}

func TestBufPool_ConcurrentGetPut_RaceDetector(t *testing.T) {
	t.Parallel()
	p := dirsync.NewBufPool(512)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := p.Get()
			(*buf)[0] = 0xFF // write to verify it's writable
			p.Put(buf)
		}()
	}
	wg.Wait()
}

package registry

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeIngestion(ref string) *activeIngestion {
	return &activeIngestion{ref: ref}
}

func TestIngestionTracker_AddGet(t *testing.T) {
	tr := newIngestionTracker()
	ing := makeIngestion("ref-1")

	ok := tr.Add("ref-1", ing)
	require.True(t, ok)

	got, found := tr.Get("ref-1")
	require.True(t, found)
	assert.Equal(t, ing, got)
}

func TestIngestionTracker_Add_Duplicate(t *testing.T) {
	tr := newIngestionTracker()
	tr.Add("ref-dup", makeIngestion("ref-dup"))

	ok := tr.Add("ref-dup", makeIngestion("ref-dup"))
	assert.False(t, ok, "second Add should return false for duplicate ref")
}

func TestIngestionTracker_Remove(t *testing.T) {
	tr := newIngestionTracker()
	tr.Add("ref-rm", makeIngestion("ref-rm"))

	removed := tr.Remove("ref-rm")
	require.NotNil(t, removed)

	_, found := tr.Get("ref-rm")
	assert.False(t, found)
}

func TestIngestionTracker_Remove_NotFound(t *testing.T) {
	tr := newIngestionTracker()
	assert.Nil(t, tr.Remove("no-such-ref"))
}

func TestIngestionTracker_All(t *testing.T) {
	tr := newIngestionTracker()
	for i := 0; i < 20; i++ {
		ref := fmt.Sprintf("ref-%d", i)
		tr.Add(ref, makeIngestion(ref))
	}
	all := tr.All()
	assert.Len(t, all, 20)
}

func TestIngestionTracker_RemoveAll(t *testing.T) {
	tr := newIngestionTracker()
	for i := 0; i < 10; i++ {
		ref := fmt.Sprintf("ref-%d", i)
		tr.Add(ref, makeIngestion(ref))
	}
	removed := tr.RemoveAll()
	assert.Len(t, removed, 10)
	assert.Empty(t, tr.All())
}

func TestIngestionTracker_Concurrent(t *testing.T) {
	tr := newIngestionTracker()
	const goroutines = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			ref := fmt.Sprintf("ref-%d", i%50)
			switch i % 4 {
			case 0:
				tr.Add(ref, makeIngestion(ref))
			case 1:
				tr.Get(ref)
			case 2:
				tr.Remove(ref)
			case 3:
				tr.All()
			}
		}()
	}
	wg.Wait()
}

func BenchmarkIngestionTracker_Add(b *testing.B) {
	tr := newIngestionTracker()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			ref := fmt.Sprintf("bench-%d", i)
			if tr.Add(ref, makeIngestion(ref)) {
				tr.Remove(ref)
			}
			i++
		}
	})
}

func BenchmarkIngestionTracker_Get(b *testing.B) {
	tr := newIngestionTracker()
	for i := 0; i < 64; i++ {
		ref := fmt.Sprintf("bench-%d", i)
		tr.Add(ref, makeIngestion(ref))
	}
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tr.Get(fmt.Sprintf("bench-%d", i%64))
			i++
		}
	})
}

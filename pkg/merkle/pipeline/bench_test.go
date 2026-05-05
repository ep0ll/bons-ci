package pipeline_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/pipeline"
)

func BenchmarkPipeline_Submit(b *testing.B) {
	for _, workers := range []int{1, 2, 4, 8} {
		workers := workers
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			p, err := pipeline.New(
				pipeline.WithHashProvider(hash.NewSyntheticProvider()),
				pipeline.WithWorkers(workers),
				pipeline.WithResultBuffer(0),
			)
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			l := layer.Digest("bench-layer")
			stack := layer.MustNew(l)

			// Build a batch of unique events.
			batch := make([]*event.FileAccessEvent, 100)
			for i := range batch {
				batch[i] = &event.FileAccessEvent{
					FilePath:   fmt.Sprintf("/bench/file/%d", i),
					LayerStack: stack,
					AccessType: event.AccessRead,
					Timestamp:  time.Now(),
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p.Submit(ctx, batch)
			}
		})
	}
}

func BenchmarkPipeline_Run(b *testing.B) {
	p, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithWorkers(4),
		pipeline.WithBufferSize(512),
		pipeline.WithResultBuffer(0),
	)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	l := layer.Digest("bench-run")
	stack := layer.MustNew(l)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch := make(chan *event.FileAccessEvent, 10)
		ch <- &event.FileAccessEvent{
			FilePath:   fmt.Sprintf("/bench/%d", i),
			LayerStack: stack,
			AccessType: event.AccessRead,
			Timestamp:  time.Now(),
		}
		close(ch)
		p.Run(ctx, ch) //nolint:errcheck
	}
}

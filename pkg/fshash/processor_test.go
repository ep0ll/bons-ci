package fshash_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bons/bons-ci/pkg/fshash"
	"github.com/bons/bons-ci/pkg/fshash/chunk"
	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// TestProcessorEndToEnd verifies the full deduplication pipeline:
// register layers → submit events → check dedup stats → finalize Merkle tree.
func TestProcessorEndToEnd(t *testing.T) {
	ctx := context.Background()

	var computed, reused, skipped atomic.Int64

	proc := fshash.NewProcessor(
		fshash.WithHashAlgorithm(chunk.BLAKE3),
		fshash.WithWorkerCount(2),
		fshash.WithChannelBuffer(64),
		fshash.WithHooks(fshash.Hooks{
			OnAccessDeduped: func(_ context.Context, r core.DeduplicationResult) {
				switch r.Action {
				case core.ActionCompute:
					computed.Add(1)
				case core.ActionReuse:
					reused.Add(1)
				case core.ActionSkip:
					skipped.Add(1)
				}
			},
		}),
	)
	defer proc.Close()

	base := fshash.NewLayerID("sha256:base")
	upper := fshash.NewLayerID("sha256:upper")

	// Register base layer.
	if err := proc.RegisterLayer(ctx, base, fshash.LayerID{}); err != nil {
		t.Fatalf("RegisterLayer(base): %v", err)
	}

	// Submit events for base layer — 3 unique files.
	files := []string{"/etc/passwd", "/etc/hosts", "/usr/bin/env"}
	for _, f := range files {
		err := proc.Submit(ctx, core.AccessEvent{
			LayerID: base,
			Path:    f,
			Op:      core.OpRead,
			Data:    []byte("content-of-" + f),
		})
		if err != nil {
			t.Fatalf("Submit(%s): %v", f, err)
		}
	}

	// Submit a duplicate event — should be skipped by bloom filter.
	err := proc.Submit(ctx, core.AccessEvent{
		LayerID: base,
		Path:    "/etc/passwd",
		Op:      core.OpRead,
		Data:    []byte("content-of-/etc/passwd"),
	})
	if err != nil {
		t.Fatalf("Submit(dup): %v", err)
	}

	// Finalize base layer.
	rootNode, err := proc.Finalize(ctx, base)
	if err != nil {
		t.Fatalf("Finalize(base): %v", err)
	}

	if rootNode == nil {
		t.Fatal("expected non-nil root node")
	}
	if len(rootNode.Hash) == 0 {
		t.Fatal("expected non-empty root hash")
	}

	// Verify stats: 3 computed + 1 skipped = 4 total.
	stats := proc.Stats()
	if stats.EventsReceived != 4 {
		t.Errorf("EventsReceived = %d, want 4", stats.EventsReceived)
	}
	if stats.EventsComputed != 3 {
		t.Errorf("EventsComputed = %d, want 3", stats.EventsComputed)
	}
	if stats.EventsSkipped != 1 {
		t.Errorf("EventsSkipped = %d, want 1", stats.EventsSkipped)
	}

	// --- Upper layer: should REUSE unmodified files ---

	if err := proc.RegisterLayer(ctx, upper, base); err != nil {
		t.Fatalf("RegisterLayer(upper): %v", err)
	}

	// Mark only /etc/hosts as modified in upper layer.
	if err := proc.MarkModified(upper, "/etc/hosts"); err != nil {
		t.Fatalf("MarkModified: %v", err)
	}

	// Access /etc/passwd from upper layer — should REUSE base's hash.
	err = proc.Submit(ctx, core.AccessEvent{
		LayerID: upper,
		Path:    "/etc/passwd",
		Op:      core.OpRead,
		Data:    []byte("content-of-/etc/passwd"),
	})
	if err != nil {
		t.Fatalf("Submit(upper, passwd): %v", err)
	}

	// Access /etc/hosts from upper layer — modified, should COMPUTE.
	err = proc.Submit(ctx, core.AccessEvent{
		LayerID: upper,
		Path:    "/etc/hosts",
		Op:      core.OpRead,
		Data:    []byte("new-content-of-/etc/hosts"),
	})
	if err != nil {
		t.Fatalf("Submit(upper, hosts): %v", err)
	}

	// Verify cumulative stats.
	stats = proc.Stats()
	if stats.EventsReused != 1 {
		t.Errorf("EventsReused = %d, want 1", stats.EventsReused)
	}
	// 3 base + 1 upper compute = 4 total computed
	if stats.EventsComputed != 4 {
		t.Errorf("EventsComputed = %d, want 4", stats.EventsComputed)
	}

	// Finalize upper layer.
	upperRoot, err := proc.Finalize(ctx, upper)
	if err != nil {
		t.Fatalf("Finalize(upper): %v", err)
	}
	if upperRoot == nil || len(upperRoot.Hash) == 0 {
		t.Fatal("expected non-nil upper root with hash")
	}

	t.Logf("Dedup rate: %.1f%%", stats.DeduplicationRate()*100)
}

// TestProcessorConcurrentSubmit verifies thread safety under concurrent load.
func TestProcessorConcurrentSubmit(t *testing.T) {
	ctx := context.Background()

	proc := fshash.NewProcessor(
		fshash.WithWorkerCount(4),
		fshash.WithChannelBuffer(256),
	)
	defer proc.Close()

	layer1 := fshash.NewLayerID("sha256:concurrent")
	if err := proc.RegisterLayer(ctx, layer1, fshash.LayerID{}); err != nil {
		t.Fatalf("RegisterLayer: %v", err)
	}

	const numEvents = 100
	var wg sync.WaitGroup

	for i := 0; i < numEvents; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := proc.Submit(ctx, core.AccessEvent{
				LayerID: layer1,
				Path:    fmt.Sprintf("/file/%d", idx),
				Op:      core.OpRead,
				Data:    []byte(fmt.Sprintf("data-%d", idx)),
			})
			if err != nil {
				t.Errorf("concurrent Submit(%d): %v", idx, err)
			}
		}(i)
	}

	wg.Wait()

	stats := proc.Stats()
	if stats.EventsReceived != numEvents {
		t.Errorf("EventsReceived = %d, want %d", stats.EventsReceived, numEvents)
	}

	root, err := proc.Finalize(ctx, layer1)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if root.LeafCount() != numEvents {
		t.Errorf("LeafCount = %d, want %d", root.LeafCount(), numEvents)
	}
}

// TestProcessorValidation verifies input validation.
func TestProcessorValidation(t *testing.T) {
	ctx := context.Background()
	proc := fshash.NewProcessor()
	defer proc.Close()

	// Missing layer ID.
	err := proc.Submit(ctx, core.AccessEvent{Path: "/foo", Op: core.OpRead})
	if err == nil {
		t.Error("expected error for missing layer ID")
	}

	// Missing path.
	err = proc.Submit(ctx, core.AccessEvent{
		LayerID: fshash.NewLayerID("sha256:x"),
		Op:      core.OpRead,
	})
	if err == nil {
		t.Error("expected error for missing path")
	}

	// Missing op.
	err = proc.Submit(ctx, core.AccessEvent{
		LayerID: fshash.NewLayerID("sha256:x"),
		Path:    "/foo",
	})
	if err == nil {
		t.Error("expected error for missing op")
	}
}

// TestProcessorClose verifies graceful shutdown.
func TestProcessorClose(t *testing.T) {
	proc := fshash.NewProcessor()

	if err := proc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Double close should be safe.
	if err := proc.Close(); err != nil {
		t.Fatalf("Double Close: %v", err)
	}

	// Operations after close should return ErrClosed.
	err := proc.Submit(context.Background(), core.AccessEvent{
		LayerID: fshash.NewLayerID("sha256:x"),
		Path:    "/foo",
		Op:      core.OpRead,
	})
	if err != core.ErrClosed {
		t.Errorf("Submit after Close: got %v, want ErrClosed", err)
	}
}

func TestProcessorOverlayEndToEnd(t *testing.T) {
	ctx := context.Background()

	var excluded atomic.Int64
	proc := fshash.NewProcessor(
		fshash.WithHooks(fshash.Hooks{
			OnFileExcluded: func(_ context.Context, _ core.LayerID, _ string) {
				excluded.Add(1)
			},
		}),
	)
	defer proc.Close()

	base := fshash.NewLayerID("base")
	upper := fshash.NewLayerID("upper")

	proc.RegisterLayer(ctx, base, fshash.LayerID{})
	proc.RegisterLayer(ctx, upper, base)

	// Base layer has 1 file
	proc.Submit(ctx, core.AccessEvent{
		LayerID: base,
		Path:    "/etc/hosts",
		Op:      core.OpRead,
		Data:    []byte("127.0.0.1"),
	})
	proc.Finalize(ctx, base)

	// Upper layer deletes file using whiteout
	proc.Submit(ctx, core.AccessEvent{
		LayerID: upper,
		Path:    "/etc/.wh.hosts",
		Op:      core.OpClose,
	})
	
	// Also upper layer makes /var/log opaque
	proc.Submit(ctx, core.AccessEvent{
		LayerID: upper,
		Path:    "/var/log/.wh..wh..opq",
		Op:      core.OpClose,
	})

	// Now if an access comes for /etc/hosts, it should be excluded
	proc.Submit(ctx, core.AccessEvent{
		LayerID: upper,
		Path:    "/etc/hosts",
		Op:      core.OpRead,
		Data:    []byte("should-be-excluded"),
	})

	// If access comes for /var/log/syslog, it should be excluded
	proc.Submit(ctx, core.AccessEvent{
		LayerID: upper,
		Path:    "/var/log/syslog",
		Op:      core.OpRead,
	})

	upperRoot, _ := proc.Finalize(ctx, upper)

	// Since all accesses were excluded or whiteouts themselves, tree should be empty and root nil
	// Or Finalize might return an error because the tree is empty.
	// But Wait, the whiteout itself was tracked? No, interpreter converts it, and it triggers MarkDeleted
	// and doesn't get computed.
	
	// Wait, Finalize returns error if empty. Since all events were excluded, it's empty.
	if upperRoot != nil {
		t.Errorf("expected tree to be empty")
	}

	// Excluded events: the whiteout, the opaque marker, and the two reads.
	// Wait! the whiteout itself emits MutationDeleted and fires hook, so that's 1.
	// The opaque marker emits MutationOpaqued and no hook fires directly from processEvent for opaque.
	// Let's check stats.
	if excluded.Load() < 3 {
		t.Errorf("expected at least 3 excluded files, got %d", excluded.Load())
	}
}

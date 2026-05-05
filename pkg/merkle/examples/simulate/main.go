// Package main demonstrates the layermerkle pipeline with a realistic
// multi-layer ExecOp simulation. No filesystem I/O is performed — the
// SyntheticProvider hashes (path, layerDigest) deterministically.
//
// Scenario:
//
//	Layer base:   /bin/sh, /etc/passwd, /lib/libc.so
//	Layer exec1:  reads base files; writes /app/server; overwrites /etc/passwd
//	Layer exec2:  reads exec1+base; deletes /etc/passwd; writes /app/config.json
//
// Key dedup outcomes to observe in the output:
//   - exec1 reads /bin/sh  → cache HIT   (promoted from base)
//   - exec2 reads /app/server → cache HIT (promoted from exec1)
//   - exec2 deletes /etc/passwd → tombstone; no Merkle leaf
//   - Duplicate reads in exec2 → all cache HITs after first promotion
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/user/layermerkle/event"
	"github.com/user/layermerkle/hash"
	"github.com/user/layermerkle/hook"
	"github.com/user/layermerkle/layer"
	"github.com/user/layermerkle/pipeline"
)

func main() {
	ctx := context.Background()

	base  := layer.Digest("sha256:baselayer0000000")
	exec1 := layer.Digest("sha256:exec1layer1111111")
	exec2 := layer.Digest("sha256:exec2layer2222222")

	recorder := hook.NewRecordingHook()
	logHook := hook.HookFunc(func(_ context.Context, e hook.HookEvent) error {
		path := "<pipeline>"
		if e.Event != nil {
			path = e.Event.FilePath
		}
		extra := ""
		if len(e.Hash) > 0 {
			extra += " hash=" + hex.EncodeToString(e.Hash)[:12] + "…"
		}
		if len(e.MerkleRoot) > 0 {
			extra += fmt.Sprintf(" root=%s… leaves=%d",
				hex.EncodeToString(e.MerkleRoot)[:12], e.LeafCount)
		}
		fmt.Printf("  [%-18s] layer=%-20s path=%q%s\n",
			e.Type, short(string(e.LayerDigest)), path, extra)
		return nil
	})

	p, err := pipeline.New(
		pipeline.WithHashProvider(hash.NewSyntheticProvider()),
		pipeline.WithWorkers(4),
		pipeline.WithBufferSize(128),
		pipeline.WithResultBuffer(256),
		pipeline.WithHook(recorder),
		pipeline.WithHook(hook.NewTypedHook(logHook,
			hook.HookCacheHit,
			hook.HookHashComputed,
			hook.HookTombstone,
			hook.HookLayerSealed,
		)),
	)
	if err != nil {
		log.Fatalf("pipeline.New: %v", err)
	}

	eventCh := make(chan *event.FileAccessEvent, 64)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if rerr := p.Run(ctx, eventCh); rerr != nil && rerr != context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "Run: %v\n", rerr)
		}
	}()

	// ── ExecOp 1 ──────────────────────────────────────────────────────────
	fmt.Println("\n╔═══ ExecOp-1  (base → exec1) ═════════════╗")
	// Base-layer reads
	emit(eventCh, "/bin/sh",      layer.MustNew(base), event.AccessRead)
	emit(eventCh, "/etc/passwd",  layer.MustNew(base), event.AccessRead)
	emit(eventCh, "/lib/libc.so", layer.MustNew(base), event.AccessRead)
	// exec1 writes
	emit(eventCh, "/app/server",  layer.MustNew(base, exec1), event.AccessCreate)
	emit(eventCh, "/etc/passwd",  layer.MustNew(base, exec1), event.AccessWrite)
	// exec1 re-reads base files (should promote from base cache)
	emit(eventCh, "/bin/sh",      layer.MustNew(base, exec1), event.AccessRead)
	emit(eventCh, "/lib/libc.so", layer.MustNew(base, exec1), event.AccessRead)
	time.Sleep(30 * time.Millisecond)

	// ── ExecOp 2 ──────────────────────────────────────────────────────────
	fmt.Println("\n╔═══ ExecOp-2  (base+exec1 → exec2) ═══════╗")
	stack2 := layer.MustNew(base, exec1, exec2)
	emit(eventCh, "/bin/sh",           stack2, event.AccessRead)
	emit(eventCh, "/app/server",       stack2, event.AccessRead)
	emit(eventCh, "/lib/libc.so",      stack2, event.AccessRead)
	emit(eventCh, "/etc/passwd",       stack2, event.AccessDelete)
	emit(eventCh, "/app/config.json",  stack2, event.AccessCreate)
	emit(eventCh, "/app/data.db",      stack2, event.AccessCreate)
	// Duplicate reads — all should cache-hit
	emit(eventCh, "/app/config.json",  stack2, event.AccessRead)
	emit(eventCh, "/app/config.json",  stack2, event.AccessRead)
	emit(eventCh, "/app/data.db",      stack2, event.AccessRead)

	close(eventCh)
	wg.Wait()

	// ── Seal ──────────────────────────────────────────────────────────────
	fmt.Println("\n╔═══ Sealing Merkle Trees ══════════════════╗")
	roots := p.SealAll(ctx)
	for d, root := range roots {
		fmt.Printf("  %-22s root=%s\n", short(string(d)), hex.EncodeToString(root))
	}

	// ── Leaves ────────────────────────────────────────────────────────────
	fmt.Println("\n╔═══ Layer Merkle Leaves ════════════════════╗")
	for _, d := range []layer.Digest{base, exec1, exec2} {
		leaves, lerr := p.Leaves(d)
		if lerr != nil {
			fmt.Printf("  [%s] (no leaves: %v)\n", short(string(d)), lerr)
			continue
		}
		fmt.Printf("  [%s] %d leaf(ves):\n", short(string(d)), len(leaves))
		for _, l := range leaves {
			fmt.Printf("    %-28s %s:%.16s…\n",
				l.FilePath, l.Algorithm, hex.EncodeToString(l.Hash))
		}
	}

	// ── Proofs ────────────────────────────────────────────────────────────
	fmt.Println("\n╔═══ Merkle Inclusion Proofs ════════════════╗")
	checkProof(p, base,  "/bin/sh")
	checkProof(p, base,  "/etc/passwd")
	checkProof(p, exec1, "/app/server")
	checkProof(p, exec1, "/etc/passwd")
	checkProof(p, exec2, "/app/config.json")
	checkProof(p, exec2, "/bin/sh")
	checkProof(p, exec2, "/etc/passwd") // deleted → expect error

	// ── Stats ─────────────────────────────────────────────────────────────
	fmt.Println("\n╔═══ Pipeline Stats ═════════════════════════╗")
	fmt.Printf("  %s\n", p.Stats())
	fmt.Println("\n  Hook event counts:")
	fmt.Printf("    %-20s %d\n", "cache_hit:",     recorder.CountByType(hook.HookCacheHit))
	fmt.Printf("    %-20s %d\n", "hash_computed:", recorder.CountByType(hook.HookHashComputed))
	fmt.Printf("    %-20s %d\n", "tombstone:",     recorder.CountByType(hook.HookTombstone))
	fmt.Printf("    %-20s %d\n", "layer_sealed:",  recorder.CountByType(hook.HookLayerSealed))
	fmt.Printf("    %-20s %d\n", "error:",         recorder.CountByType(hook.HookError))
}

func emit(ch chan<- *event.FileAccessEvent, path string, stack layer.Stack, at event.AccessType) {
	top, _ := stack.Top()
	ch <- &event.FileAccessEvent{
		FilePath:     path,
		LayerStack:   stack,
		VertexDigest: "vtx:" + string(top),
		AccessType:   at,
		Timestamp:    time.Now(),
	}
}

func checkProof(p *pipeline.Pipeline, d layer.Digest, filePath string) {
	proof, err := p.Proof(d, filePath)
	if err != nil {
		fmt.Printf("  [%s] %-28s — no proof (%v)\n", short(string(d)), filePath, err)
		return
	}
	if verr := proof.Verify(); verr != nil {
		fmt.Printf("  [%s] %-28s — ✗ INVALID: %v\n", short(string(d)), filePath, verr)
		return
	}
	fmt.Printf("  [%s] %-28s — ✓ valid (%d sibling(s))\n",
		short(string(d)), filePath, len(proof.Siblings))
}

func short(s string) string {
	const max = 20
	if len(s) > max {
		return s[len(s)-max:]
	}
	return s
}

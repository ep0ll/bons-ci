package layermerkle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/bons/bons-ci/pkg/layermerkle/internal/digest"
)

// ─────────────────────────────────────────────────────────────────────────────
// Wire types — JSON-serializable representations
// ─────────────────────────────────────────────────────────────────────────────

// wireForest is the JSON wire format for a MerkleForest.
type wireForest struct {
	Version int        `json:"version"`
	Trees   []wireTree `json:"trees"`
}

// wireTree is the JSON wire format for a MerkleTree.
type wireTree struct {
	VertexID      string     `json:"vertex_id"`
	LayerStack    []string   `json:"layer_stack"`
	Root          string     `json:"root"`
	Leaves        []wireLeaf `json:"leaves"`
	LeafCount     int        `json:"leaf_count"`
	CacheHitCount int        `json:"cache_hit_count"`
	FinalizedAt   time.Time  `json:"finalized_at"`
}

// wireLeaf is the JSON wire format for a MerkleLeaf.
type wireLeaf struct {
	RelPath      string `json:"rel_path"`
	Hash         string `json:"hash"`
	OwnerLayerID string `json:"owner_layer_id"`
	FromCache    bool   `json:"from_cache"`
}

const forestWireVersion = 1

// ─────────────────────────────────────────────────────────────────────────────
// Marshal / Unmarshal
// ─────────────────────────────────────────────────────────────────────────────

// MarshalForest serializes a MerkleForest to JSON.
// The output is stable across runs for identical access patterns.
func MarshalForest(forest *MerkleForest) ([]byte, error) {
	wf := wireForest{Version: forestWireVersion}
	for _, tree := range forest.All() {
		wf.Trees = append(wf.Trees, treeToWire(tree))
	}
	return json.Marshal(wf)
}

// UnmarshalForest deserializes a MerkleForest from JSON produced by MarshalForest.
func UnmarshalForest(data []byte) (*MerkleForest, error) {
	var wf wireForest
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("layermerkle: unmarshal forest: %w", err)
	}
	if wf.Version != forestWireVersion {
		return nil, fmt.Errorf("layermerkle: unsupported wire version %d (want %d)",
			wf.Version, forestWireVersion)
	}
	forest := NewMerkleForest()
	for _, wt := range wf.Trees {
		tree, err := wireToTree(wt)
		if err != nil {
			return nil, err
		}
		forest.Add(tree)
	}
	return forest, nil
}

// WriteForest writes a MerkleForest as JSON to w.
func WriteForest(w io.Writer, forest *MerkleForest) error {
	b, err := MarshalForest(forest)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadForest reads a MerkleForest from JSON in r.
func ReadForest(r io.Reader) (*MerkleForest, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("layermerkle: read forest: %w", err)
	}
	return UnmarshalForest(b)
}

// ─────────────────────────────────────────────────────────────────────────────
// Single-tree marshal / unmarshal
// ─────────────────────────────────────────────────────────────────────────────

// MarshalTree serializes a single MerkleTree to JSON.
func MarshalTree(tree *MerkleTree) ([]byte, error) {
	return json.Marshal(treeToWire(tree))
}

// UnmarshalTree deserializes a single MerkleTree from JSON.
func UnmarshalTree(data []byte) (*MerkleTree, error) {
	var wt wireTree
	if err := json.Unmarshal(data, &wt); err != nil {
		return nil, fmt.Errorf("layermerkle: unmarshal tree: %w", err)
	}
	return wireToTree(wt)
}

// ─────────────────────────────────────────────────────────────────────────────
// Conversion helpers
// ─────────────────────────────────────────────────────────────────────────────

func treeToWire(t *MerkleTree) wireTree {
	wt := wireTree{
		VertexID:      string(t.VertexID),
		Root:          string(t.Root),
		LeafCount:     t.LeafCount,
		CacheHitCount: t.CacheHitCount,
		FinalizedAt:   t.FinalizedAt,
	}
	for _, id := range t.LayerStack {
		wt.LayerStack = append(wt.LayerStack, string(id))
	}
	for _, leaf := range t.Leaves {
		wt.Leaves = append(wt.Leaves, wireLeaf{
			RelPath:      leaf.RelPath,
			Hash:         string(leaf.Hash),
			OwnerLayerID: string(leaf.OwnerLayerID),
			FromCache:    leaf.FromCache,
		})
	}
	return wt
}

func wireToTree(wt wireTree) (*MerkleTree, error) {
	stack := make(LayerStack, len(wt.LayerStack))
	for i, s := range wt.LayerStack {
		d := digest.Digest(s)
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("layermerkle: invalid layer digest %q: %w", s, err)
		}
		stack[i] = d
	}

	leaves := make([]*MerkleLeaf, len(wt.Leaves))
	for i, wl := range wt.Leaves {
		leaves[i] = &MerkleLeaf{
			RelPath:      wl.RelPath,
			Hash:         FileHash(wl.Hash),
			OwnerLayerID: LayerID(wl.OwnerLayerID),
			FromCache:    wl.FromCache,
		}
	}

	return &MerkleTree{
		VertexID:      VertexID(wt.VertexID),
		LayerStack:    stack,
		Root:          FileHash(wt.Root),
		Leaves:        leaves,
		LeafCount:     wt.LeafCount,
		CacheHitCount: wt.CacheHitCount,
		FinalizedAt:   wt.FinalizedAt,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// PrettyPrint — human-readable tree rendering
// ─────────────────────────────────────────────────────────────────────────────

// PrettyPrintTree writes a human-readable tree diagram to w.
// Format:
//
//	MerkleTree{vertex=sha256:ab... root=sha256:cd... leaves=42 hit_rate=87%}
//	  usr/
//	    bin/
//	      python3   sha256:ef...  [layer-0]
//	      sh        sha256:gh...  [layer-1] (cache)
func PrettyPrintTree(w io.Writer, t *MerkleTree) error {
	header := fmt.Sprintf("MerkleTree{vertex=%s root=%s leaves=%d hit_rate=%.0f%%}\n",
		shortDigest(t.VertexID), shortDigest(t.Root),
		t.LeafCount, t.CacheHitRate()*100,
	)
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}

	type prefixedLeaf struct {
		prefix string
		leaf   *MerkleLeaf
	}
	// Group leaves by directory prefix.
	dirs := make(map[string][]*MerkleLeaf)
	for _, l := range t.Leaves {
		i := lastSlash(l.RelPath)
		dir := ""
		if i >= 0 {
			dir = l.RelPath[:i]
		}
		dirs[dir] = append(dirs[dir], l)
	}

	// Emit in sorted order.
	sortedDirs := make([]string, 0, len(dirs))
	for d := range dirs {
		sortedDirs = append(sortedDirs, d)
	}
	sortStrings(sortedDirs)

	for _, dir := range sortedDirs {
		if dir != "" {
			fmt.Fprintf(w, "  %s/\n", dir)
		}
		for _, leaf := range dirs[dir] {
			name := leaf.RelPath[lastSlash(leaf.RelPath)+1:]
			cache := ""
			if leaf.FromCache {
				cache = " (cache)"
			}
			fmt.Fprintf(w, "    %-40s  %s  [%s]%s\n",
				name, shortDigest(leaf.Hash), shortDigest(leaf.OwnerLayerID), cache)
		}
	}
	return nil
}

// PrettyPrintForest writes all trees in a forest.
func PrettyPrintForest(w io.Writer, forest *MerkleForest) error {
	buf := &bytes.Buffer{}
	for i, t := range forest.All() {
		if i > 0 {
			buf.WriteString("\n")
		}
		if err := PrettyPrintTree(buf, t); err != nil {
			return err
		}
	}
	_, err := io.Copy(w, buf)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

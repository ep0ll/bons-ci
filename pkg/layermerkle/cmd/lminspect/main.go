// Command lminspect reads a serialized MerkleForest from stdin or a file and
// renders a human-readable summary to stdout.
//
// Usage:
//
//	lminspect [flags] [file]
//	lminspect < forest.json
//	lminspect -diff before.json after.json
//	lminspect -vertex sha256:abc123 forest.json
//
// Flags:
//
//	-diff     Compare two forests and print the diff
//	-vertex   Filter to a single vertex ID
//	-json     Emit JSON instead of human-readable text
//	-stats    Print cache-hit statistics per vertex
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bons/bons-ci/pkg/layermerkle"
)

// ─────────────────────────────────────────────────────────────────────────────
// CLI flags
// ─────────────────────────────────────────────────────────────────────────────

type config struct {
	diffMode   bool
	vertexID   string
	jsonOutput bool
	statsMode  bool
	args       []string
}

func parseFlags() config {
	var cfg config
	flag.BoolVar(&cfg.diffMode, "diff", false, "compare two forest files")
	flag.StringVar(&cfg.vertexID, "vertex", "", "filter to a specific vertex digest")
	flag.BoolVar(&cfg.jsonOutput, "json", false, "emit JSON output")
	flag.BoolVar(&cfg.statsMode, "stats", false, "print per-vertex cache statistics")
	flag.Parse()
	cfg.args = flag.Args()
	return cfg
}

// ─────────────────────────────────────────────────────────────────────────────
// main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	cfg := parseFlags()
	if err := run(cfg, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lminspect:", err)
		os.Exit(1)
	}
}

func run(cfg config, out, errOut io.Writer) error {
	switch {
	case cfg.diffMode:
		return runDiff(cfg, out)
	default:
		return runInspect(cfg, out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Inspect mode
// ─────────────────────────────────────────────────────────────────────────────

func runInspect(cfg config, out io.Writer) error {
	forest, err := readForestFromArgs(cfg.args)
	if err != nil {
		return err
	}

	trees := forest.All()
	if cfg.vertexID != "" {
		trees = filterByVertex(trees, cfg.vertexID)
	}

	if cfg.jsonOutput {
		return emitJSON(out, trees)
	}

	if cfg.statsMode {
		return emitStats(out, trees)
	}

	return emitHuman(out, trees)
}

// ─────────────────────────────────────────────────────────────────────────────
// Diff mode
// ─────────────────────────────────────────────────────────────────────────────

func runDiff(cfg config, out io.Writer) error {
	if len(cfg.args) < 2 {
		return fmt.Errorf("diff mode requires two forest files: lminspect -diff before.json after.json")
	}
	before, err := readForestFromFile(cfg.args[0])
	if err != nil {
		return fmt.Errorf("reading before: %w", err)
	}
	after, err := readForestFromFile(cfg.args[1])
	if err != nil {
		return fmt.Errorf("reading after: %w", err)
	}

	d := layermerkle.Diff(before, after)
	return emitDiff(out, d, cfg.jsonOutput)
}

// ─────────────────────────────────────────────────────────────────────────────
// Output formatters
// ─────────────────────────────────────────────────────────────────────────────

func emitHuman(out io.Writer, trees []*layermerkle.MerkleTree) error {
	if len(trees) == 0 {
		fmt.Fprintln(out, "(no trees found)")
		return nil
	}
	for i, t := range trees {
		if i > 0 {
			fmt.Fprintln(out)
		}
		if err := layermerkle.PrettyPrintTree(out, t); err != nil {
			return err
		}
	}
	return nil
}

func emitStats(out io.Writer, trees []*layermerkle.MerkleTree) error {
	fmt.Fprintf(out, "%-20s  %8s  %10s  %10s  %8s\n",
		"vertex", "leaves", "cache_hits", "misses", "hit_rate")
	fmt.Fprintln(out, strings.Repeat("-", 65))
	for _, t := range trees {
		misses := t.LeafCount - t.CacheHitCount
		fmt.Fprintf(out, "%-20s  %8d  %10d  %10d  %7.1f%%\n",
			shortDigestStr(string(t.VertexID)),
			t.LeafCount,
			t.CacheHitCount,
			misses,
			t.CacheHitRate()*100,
		)
	}
	return nil
}

func emitJSON(out io.Writer, trees []*layermerkle.MerkleTree) error {
	f := layermerkle.NewMerkleForest()
	for _, t := range trees {
		f.Add(t)
	}
	data, err := layermerkle.MarshalForest(f)
	if err != nil {
		return err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, data, "", "  "); err != nil {
		return err
	}
	fmt.Fprintln(out, indented.String())
	return nil
}

func emitDiff(out io.Writer, d *layermerkle.ForestDiff, asJSON bool) error {
	if asJSON {
		return emitDiffJSON(out, d)
	}
	return emitDiffHuman(out, d)
}

func emitDiffHuman(out io.Writer, d *layermerkle.ForestDiff) error {
	fmt.Fprintln(out, d.Summary())

	if len(d.Added) > 0 {
		fmt.Fprintln(out, "\nAdded vertices:")
		for _, t := range d.Added {
			fmt.Fprintf(out, "  + %s  (root=%s leaves=%d)\n",
				shortDigestStr(string(t.VertexID)), shortDigestStr(string(t.Root)), t.LeafCount)
		}
	}

	if len(d.Removed) > 0 {
		fmt.Fprintln(out, "\nRemoved vertices:")
		for _, t := range d.Removed {
			fmt.Fprintf(out, "  - %s  (root=%s leaves=%d)\n",
				shortDigestStr(string(t.VertexID)), shortDigestStr(string(t.Root)), t.LeafCount)
		}
	}

	if len(d.Changed) > 0 {
		fmt.Fprintln(out, "\nChanged vertices:")
		for _, c := range d.Changed {
			fmt.Fprintf(out, "  ~ %s\n    before: root=%s\n    after:  root=%s\n",
				shortDigestStr(string(c.VertexID)),
				shortDigestStr(string(c.Before.Root)),
				shortDigestStr(string(c.After.Root)),
			)
			fd := c.ComputeFileDiff()
			if len(fd.Added) > 0 {
				fmt.Fprintf(out, "    files added: %d\n", len(fd.Added))
			}
			if len(fd.Removed) > 0 {
				fmt.Fprintf(out, "    files removed: %d\n", len(fd.Removed))
			}
			if len(fd.Modified) > 0 {
				fmt.Fprintf(out, "    files modified: %d\n", len(fd.Modified))
			}
		}
	}

	if d.IsIdentical() {
		fmt.Fprintln(out, "Forests are identical.")
	}
	return nil
}

func emitDiffJSON(out io.Writer, d *layermerkle.ForestDiff) error {
	type jsonChange struct {
		VertexID     string `json:"vertex_id"`
		BeforeRoot   string `json:"before_root"`
		AfterRoot    string `json:"after_root"`
		FilesAdded   int    `json:"files_added"`
		FilesRemoved int    `json:"files_removed"`
		FilesChanged int    `json:"files_modified"`
	}
	type jsonDiff struct {
		Added     []string     `json:"added"`
		Removed   []string     `json:"removed"`
		Changed   []jsonChange `json:"changed"`
		Unchanged []string     `json:"unchanged"`
	}

	jd := jsonDiff{}
	for _, t := range d.Added {
		jd.Added = append(jd.Added, string(t.VertexID))
	}
	for _, t := range d.Removed {
		jd.Removed = append(jd.Removed, string(t.VertexID))
	}
	for _, c := range d.Changed {
		fd := c.ComputeFileDiff()
		jd.Changed = append(jd.Changed, jsonChange{
			VertexID:     string(c.VertexID),
			BeforeRoot:   string(c.Before.Root),
			AfterRoot:    string(c.After.Root),
			FilesAdded:   len(fd.Added),
			FilesRemoved: len(fd.Removed),
			FilesChanged: len(fd.Modified),
		})
	}
	for _, t := range d.Unchanged {
		jd.Unchanged = append(jd.Unchanged, string(t.VertexID))
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(jd)
}

// ─────────────────────────────────────────────────────────────────────────────
// I/O helpers
// ─────────────────────────────────────────────────────────────────────────────

func readForestFromArgs(args []string) (*layermerkle.MerkleForest, error) {
	if len(args) == 0 {
		return layermerkle.ReadForest(os.Stdin)
	}
	return readForestFromFile(args[0])
}

func readForestFromFile(path string) (*layermerkle.MerkleForest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return layermerkle.ReadForest(f)
}

func filterByVertex(trees []*layermerkle.MerkleTree, vertexID string) []*layermerkle.MerkleTree {
	var out []*layermerkle.MerkleTree
	for _, t := range trees {
		if strings.HasPrefix(string(t.VertexID), vertexID) {
			out = append(out, t)
		}
	}
	return out
}

func shortDigestStr(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

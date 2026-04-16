// accelctl is the command-line client for AccelRegistry.
//
// Usage:
//
//	accelctl query   <digest>           Query all accel variants for a source digest
//	accelctl pull    <digest> [types…]  Pull specific accel types for a source digest
//	accelctl dag     <repo> <digest>    Show the OCI content DAG for a digest
//	accelctl meta    <repo> <digest>    Show image metadata
//	accelctl exists  <digest>           Fast existence check (bloom + definitive)
//	accelctl stats                      Show registry index statistics
//	accelctl types                      List registered acceleration types
//	accelctl push    <repo> <file>      Push a manifest file to the registry
//	accelctl gc      [--dry-run]        Trigger GC on the registry (admin endpoint)
//	accelctl health                     Check registry health
//
// Environment variables:
//
//	ACCELREG_URL     Registry base URL (default: http://localhost:5000)
//	ACCELREG_TOKEN   Bearer token for authentication (optional)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	digest "github.com/opencontainers/go-digest"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// CLI config
// ────────────────────────────────────────────────────────────────────────────

type config struct {
	baseURL string
	token   string
	client  *http.Client
	output  io.Writer
}

func newConfig() *config {
	baseURL := os.Getenv("ACCELREG_URL")
	if baseURL == "" {
		baseURL = "http://localhost:5000"
	}
	return &config{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   os.Getenv("ACCELREG_TOKEN"),
		client:  &http.Client{Timeout: 30 * time.Second},
		output:  os.Stdout,
	}
}

// ────────────────────────────────────────────────────────────────────────────
// main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	cfg := newConfig()
	args := os.Args[1:]

	if len(args) == 0 {
		printUsage(cfg.output)
		os.Exit(1)
	}

	var err error
	switch args[0] {
	case "query":
		err = cmdQuery(cfg, args[1:])
	case "pull":
		err = cmdPull(cfg, args[1:])
	case "dag":
		err = cmdDAG(cfg, args[1:])
	case "meta", "metadata":
		err = cmdMeta(cfg, args[1:])
	case "exists":
		err = cmdExists(cfg, args[1:])
	case "stats":
		err = cmdStats(cfg, args[1:])
	case "types":
		err = cmdTypes(cfg, args[1:])
	case "health":
		err = cmdHealth(cfg, args[1:])
	case "version", "--version", "-v":
		fmt.Fprintln(cfg.output, "accelctl v1.0.0")
	case "help", "--help", "-h":
		printUsage(cfg.output)
	default:
		fmt.Fprintf(cfg.output, "unknown command: %q\n\n", args[0])
		printUsage(cfg.output)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Commands
// ────────────────────────────────────────────────────────────────────────────

func cmdQuery(cfg *config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: accelctl query <digest>")
	}
	dgst := args[0]
	url := fmt.Sprintf("%s/accel/v1/query/%s", cfg.baseURL, dgst)

	var result types.AccelQueryResult
	if err := cfg.getJSON(url, &result); err != nil {
		return err
	}

	if !result.Found {
		fmt.Fprintf(cfg.output, "✗ No accelerated variants found for digest: %s\n", dgst)
		return nil
	}

	fmt.Fprintf(cfg.output, "✓ Source digest: %s\n", result.SourceDigest)
	fmt.Fprintf(cfg.output, "  Total variants: %d\n", result.TotalVariants)
	fmt.Fprintf(cfg.output, "  Supported types: %s\n\n", joinTypes(result.SupportedTypes))

	for accelType, variants := range result.Variants {
		fmt.Fprintf(cfg.output, "  ── %s (%d variant(s)) ──\n", accelType, len(variants))
		for _, v := range variants {
			fmt.Fprintf(cfg.output, "     Manifest:    %s\n", v.ManifestDigest)
			fmt.Fprintf(cfg.output, "     Repository:  %s\n", v.Repository)
			if v.Tag != "" {
				fmt.Fprintf(cfg.output, "     Tag:         %s\n", v.Tag)
			}
			fmt.Fprintf(cfg.output, "     Size:        %s\n", humanBytes(v.Size))
			fmt.Fprintf(cfg.output, "     Visibility:  %s\n", v.Visibility)
			fmt.Fprintf(cfg.output, "     Created:     %s\n", v.CreatedAt.Format(time.RFC3339))
			fmt.Fprintf(cfg.output, "     Source refs: %d\n\n", len(v.SourceRefs))
		}
	}
	return nil
}

func cmdPull(cfg *config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: accelctl pull <source-digest> [accel-type…]")
	}
	req := types.PullRequest{SourceDigest: mustParseDigest(args[0])}
	for _, t := range args[1:] {
		req.AccelTypes = append(req.AccelTypes, types.AccelType(t))
	}

	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/accel/v1/pull", cfg.baseURL)

	var result types.PullResult
	if err := cfg.postJSON(url, body, &result); err != nil {
		return err
	}

	if len(result.Pulled) == 0 {
		fmt.Fprintf(cfg.output, "✗ No variants pulled for %s\n", result.SourceDigest)
		if len(result.Errors) > 0 {
			for k, v := range result.Errors {
				fmt.Fprintf(cfg.output, "  %s: %s\n", k, v)
			}
		}
		return nil
	}

	fmt.Fprintf(cfg.output, "✓ Pulled %d variant(s) for %s\n\n", len(result.Pulled), result.SourceDigest)
	for _, v := range result.Pulled {
		fmt.Fprintf(cfg.output, "  [%s] %s  %s  (%s)\n",
			v.AccelType, v.Repository,
			v.ManifestDigest.String()[:19]+"...",
			humanBytes(v.Size))
	}
	return nil
}

func cmdDAG(cfg *config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: accelctl dag <repo> <digest>")
	}
	repo, dgst := args[0], args[1]
	url := fmt.Sprintf("%s/accel/v1/dag/%s/%s", cfg.baseURL, repo, dgst)

	var result types.DAGQueryResult
	if err := cfg.getJSON(url, &result); err != nil {
		return err
	}

	complete := "✓ complete"
	if !result.IsComplete {
		complete = fmt.Sprintf("✗ incomplete (%d missing)", result.MissingNodes)
	}

	fmt.Fprintf(cfg.output, "DAG for %s\n", result.RootDigest)
	fmt.Fprintf(cfg.output, "  Total nodes:    %d\n", result.TotalNodes)
	fmt.Fprintf(cfg.output, "  Existing:       %d\n", result.ExistingNodes)
	fmt.Fprintf(cfg.output, "  Missing:        %d\n", result.MissingNodes)
	fmt.Fprintf(cfg.output, "  Completeness:   %s\n", complete)
	if len(result.AccelTypes) > 0 {
		fmt.Fprintf(cfg.output, "  Accel types:    %s\n", joinTypes(result.AccelTypes))
	}

	if result.Root != nil {
		fmt.Fprintln(cfg.output, "\n  Tree:")
		printDAGNode(cfg.output, result.Root, "    ", true)
	}
	return nil
}

func cmdMeta(cfg *config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: accelctl meta <repo> <digest>")
	}
	repo, dgst := args[0], args[1]
	url := fmt.Sprintf("%s/accel/v1/metadata/%s/%s", cfg.baseURL, repo, dgst)

	var meta types.ImageMetadata
	if err := cfg.getJSON(url, &meta); err != nil {
		return err
	}

	fmt.Fprintf(cfg.output, "Image Metadata\n")
	fmt.Fprintf(cfg.output, "  Digest:       %s\n", meta.Digest)
	fmt.Fprintf(cfg.output, "  Repository:   %s\n", meta.Repository)
	fmt.Fprintf(cfg.output, "  Tags:         %s\n", strings.Join(meta.Tags, ", "))
	fmt.Fprintf(cfg.output, "  Visibility:   %s\n", meta.Visibility)
	fmt.Fprintf(cfg.output, "  Accelerated:  %v\n", meta.IsAccel)
	if meta.IsAccel {
		fmt.Fprintf(cfg.output, "  Accel Type:   %s\n", meta.AccelType)
	}
	if meta.SourceDigest != "" {
		fmt.Fprintf(cfg.output, "  Source:       %s\n", meta.SourceDigest)
	}
	fmt.Fprintf(cfg.output, "  Total size:   %s\n", humanBytes(meta.TotalSize))
	fmt.Fprintf(cfg.output, "  Created:      %s\n", meta.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(cfg.output, "  Updated:      %s\n", meta.UpdatedAt.Format(time.RFC3339))
	if len(meta.Annotations) > 0 {
		fmt.Fprintln(cfg.output, "  Annotations:")
		for k, v := range meta.Annotations {
			fmt.Fprintf(cfg.output, "    %s = %s\n", k, v)
		}
	}
	return nil
}

func cmdExists(cfg *config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: accelctl exists <digest>")
	}
	dgst := args[0]
	url := fmt.Sprintf("%s/accel/v1/exists/%s", cfg.baseURL, dgst)

	resp, err := cfg.do(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&result)

	if resp.StatusCode == http.StatusOK {
		total, _ := result["totalVariants"].(float64)
		typesRaw, _ := result["supportedTypes"].([]interface{})
		typs := make([]string, len(typesRaw))
		for i, t := range typesRaw {
			typs[i] = fmt.Sprintf("%v", t)
		}
		fmt.Fprintf(cfg.output, "✓ Accelerated variants exist for %s\n", dgst)
		fmt.Fprintf(cfg.output, "  Total variants: %d\n", int(total))
		fmt.Fprintf(cfg.output, "  Types:          %s\n", strings.Join(typs, ", "))
	} else {
		fmt.Fprintf(cfg.output, "✗ No accelerated variants for %s\n", dgst)
	}
	return nil
}

func cmdStats(cfg *config, args []string) error {
	url := fmt.Sprintf("%s/accel/v1/stats", cfg.baseURL)
	var stats types.IndexStats
	if err := cfg.getJSON(url, &stats); err != nil {
		return err
	}

	fmt.Fprintf(cfg.output, "AccelIndex Statistics\n")
	fmt.Fprintf(cfg.output, "  Total source digests: %d\n", stats.TotalSourceDigests)
	fmt.Fprintf(cfg.output, "  Shards:               %d\n", stats.ShardCount)
	fmt.Fprintf(cfg.output, "  Bloom FP rate:        %.4f%%\n", stats.BloomFalsePositiveRate*100)
	fmt.Fprintln(cfg.output, "  Variants by type:")
	for t, count := range stats.TotalVariants {
		fmt.Fprintf(cfg.output, "    %-12s %d\n", t, count)
	}
	return nil
}

func cmdTypes(cfg *config, args []string) error {
	url := fmt.Sprintf("%s/accel/v1/types", cfg.baseURL)
	var result map[string]interface{}
	if err := cfg.getJSON(url, &result); err != nil {
		return err
	}
	fmt.Fprintln(cfg.output, "Registered acceleration types:")
	if ts, ok := result["accelTypes"].([]interface{}); ok {
		for i, t := range ts {
			fmt.Fprintf(cfg.output, "  %d. %s\n", i+1, t)
		}
	}
	return nil
}

func cmdHealth(cfg *config, args []string) error {
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := cfg.do(http.MethodGet, cfg.baseURL+path, nil)
		if err != nil {
			fmt.Fprintf(cfg.output, "✗ %s: %v\n", path, err)
			continue
		}
		resp.Body.Close()
		status := "✓"
		if resp.StatusCode != http.StatusOK {
			status = "✗"
		}
		fmt.Fprintf(cfg.output, "%s %s → %d\n", status, path, resp.StatusCode)
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// HTTP helpers
// ────────────────────────────────────────────────────────────────────────────

func (c *config) do(method, url string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.client.Do(req)
}

func (c *config) getJSON(url string, out interface{}) error {
	resp, err := c.do(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *config) postJSON(url string, body []byte, out interface{}) error {
	resp, err := c.do(http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ────────────────────────────────────────────────────────────────────────────
// Formatting helpers
// ────────────────────────────────────────────────────────────────────────────

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func joinTypes(ts []types.AccelType) string {
	strs := make([]string, len(ts))
	for i, t := range ts {
		strs[i] = string(t)
	}
	return strings.Join(strs, ", ")
}

func mustParseDigest(s string) digest.Digest {
	d, err := digest.Parse(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid digest %q: %v\n", s, err)
		os.Exit(1)
	}
	return d
}

func printDAGNode(w io.Writer, node *types.DAGNode, prefix string, last bool) {
	if node == nil {
		return
	}
	connector := "├── "
	if last {
		connector = "└── "
	}
	exists := "✓"
	if !node.Exists {
		exists = "✗"
	}
	dgstShort := node.Digest.String()
	if len(dgstShort) > 19 {
		dgstShort = dgstShort[:19] + "..."
	}
	mt := node.MediaType
	if mt == "" {
		mt = "unknown"
	}
	// Shorten well-known media types
	mt = strings.TrimPrefix(mt, "application/vnd.oci.image.")
	mt = strings.TrimPrefix(mt, "application/vnd.docker.distribution.")
	fmt.Fprintf(w, "%s%s [%s] %s  %s\n", prefix, connector, exists, dgstShort, mt)

	childPrefix := prefix + "│   "
	if last {
		childPrefix = prefix + "    "
	}
	for i, child := range node.Children {
		printDAGNode(w, child, childPrefix, i == len(node.Children)-1)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `accelctl — AccelRegistry CLI client

Commands:
  query   <digest>                  Query all accel variants for a source digest
  pull    <digest> [types...]       Pull specific accel types (nydus, estargz, soci, overlaybd)
  dag     <repo> <digest>          Show the full OCI content DAG
  meta    <repo> <digest>          Show image metadata
  exists  <digest>                  Fast bloom + definitive existence check
  stats                             Show index statistics
  types                             List registered accel types
  health                            Check registry health endpoints
  version                           Print version

Environment:
  ACCELREG_URL     Registry base URL (default: http://localhost:5000)
  ACCELREG_TOKEN   Bearer token for auth (optional)

Examples:
  # Query all accel variants for a node:20 image
  accelctl query sha256:abc123...

  # Pull only nydus and estargz variants
  accelctl pull sha256:abc123... nydus estargz

  # Inspect the full DAG of an image
  accelctl dag library/node sha256:abc123...

  # Check if any accel exists quickly
  accelctl exists sha256:abc123...
`)
}

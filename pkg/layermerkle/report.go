package layermerkle

import (
	"fmt"
	"html"
	"io"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// HTMLReport — human-readable HTML comparison report
// ─────────────────────────────────────────────────────────────────────────────

// HTMLReportConfig configures an HTMLReport.
type HTMLReportConfig struct {
	// Title is the page title and h1 heading.
	Title string

	// BuildID is shown in the report header.
	BuildID string

	// ShowUnchanged controls whether identical vertices appear in the report.
	ShowUnchanged bool

	// MaxFilesPerVertex limits the number of files shown per vertex.
	// 0 means unlimited.
	MaxFilesPerVertex int
}

// WriteHTMLReport renders a ForestDiff as a self-contained HTML page to w.
// The report includes per-vertex summaries, file-level diffs, cache hit rates,
// and a top-level summary table.
func WriteHTMLReport(w io.Writer, d *ForestDiff, cfg HTMLReportConfig) error {
	if cfg.Title == "" {
		cfg.Title = "layermerkle Build Comparison Report"
	}
	r := &htmlReportRenderer{w: w, cfg: cfg, d: d}
	return r.render()
}

// ─────────────────────────────────────────────────────────────────────────────
// htmlReportRenderer — internal renderer
// ─────────────────────────────────────────────────────────────────────────────

type htmlReportRenderer struct {
	w   io.Writer
	cfg HTMLReportConfig
	d   *ForestDiff
	err error
}

func (r *htmlReportRenderer) write(s string) {
	if r.err != nil {
		return
	}
	_, r.err = io.WriteString(r.w, s)
}

func (r *htmlReportRenderer) writef(format string, args ...any) {
	r.write(fmt.Sprintf(format, args...))
}

func (r *htmlReportRenderer) render() error {
	r.writeHeader()
	r.writeSummaryTable()
	r.writeChangedVertices()
	r.writeAddedVertices()
	r.writeRemovedVertices()
	if r.cfg.ShowUnchanged {
		r.writeUnchangedVertices()
	}
	r.writeFooter()
	return r.err
}

func (r *htmlReportRenderer) writeHeader() {
	r.writef(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', monospace; margin: 2rem; color: #1a1a1a; }
  h1   { color: #0f172a; }
  h2   { color: #1e3a5f; border-bottom: 2px solid #e2e8f0; padding-bottom: .4rem; }
  h3   { color: #334155; }
  .summary-table { border-collapse: collapse; width: 100%%; margin-bottom: 2rem; }
  .summary-table th, .summary-table td { border: 1px solid #cbd5e1; padding: .5rem .75rem; text-align: left; }
  .summary-table th { background: #f1f5f9; font-weight: 600; }
  .summary-table tr:hover { background: #f8fafc; }
  .badge { display: inline-block; padding: .2rem .5rem; border-radius: .25rem; font-size: .8rem; font-weight: 600; }
  .badge-added    { background: #dcfce7; color: #166534; }
  .badge-removed  { background: #fee2e2; color: #991b1b; }
  .badge-changed  { background: #fef9c3; color: #713f12; }
  .badge-same     { background: #f1f5f9; color: #475569; }
  .file-added     { background: #dcfce7; }
  .file-removed   { background: #fee2e2; }
  .file-changed   { background: #fef9c3; }
  .file-table     { border-collapse: collapse; width: 100%%; font-size: .85rem; margin-bottom: 1rem; }
  .file-table th, .file-table td { border: 1px solid #e2e8f0; padding: .3rem .6rem; }
  .file-table th  { background: #f8fafc; }
  .digest         { font-family: monospace; font-size: .8rem; color: #64748b; }
  .hit-rate       { font-size: .85rem; color: #475569; }
  footer          { margin-top: 3rem; color: #94a3b8; font-size: .8rem; }
</style>
</head>
<body>
<h1>%s</h1>
`, html.EscapeString(r.cfg.Title), html.EscapeString(r.cfg.Title))

	if r.cfg.BuildID != "" {
		r.writef("<p><strong>Build:</strong> <code>%s</code></p>\n",
			html.EscapeString(r.cfg.BuildID))
	}
	r.writef("<p><strong>Generated:</strong> %s</p>\n", time.Now().UTC().Format(time.RFC3339))
}

func (r *htmlReportRenderer) writeSummaryTable() {
	r.write("<h2>Summary</h2>\n")
	r.write(`<table class="summary-table">
<thead><tr><th>Category</th><th>Count</th></tr></thead>
<tbody>
`)
	rows := []struct {
		label, class string
		count        int
	}{
		{"Added vertices", "added", len(r.d.Added)},
		{"Removed vertices", "removed", len(r.d.Removed)},
		{"Changed vertices", "changed", len(r.d.Changed)},
		{"Unchanged vertices", "same", len(r.d.Unchanged)},
	}
	for _, row := range rows {
		r.writef("<tr><td><span class=\"badge badge-%s\">%s</span></td><td>%d</td></tr>\n",
			row.class, html.EscapeString(row.label), row.count)
	}
	r.write("</tbody></table>\n")
}

func (r *htmlReportRenderer) writeChangedVertices() {
	if len(r.d.Changed) == 0 {
		return
	}
	r.write("<h2>Changed Vertices</h2>\n")
	for _, c := range r.d.Changed {
		fd := c.ComputeFileDiff()
		r.writef("<h3>%s</h3>\n", html.EscapeString(shortDigest(c.VertexID)))
		r.writef("<p class=\"hit-rate\">Before root: <code class=\"digest\">%s</code> → After root: <code class=\"digest\">%s</code></p>\n",
			html.EscapeString(shortDigest(c.Before.Root)),
			html.EscapeString(shortDigest(c.After.Root)),
		)
		r.writef("<p>Files: +%d added / -%d removed / ~%d modified / =%d unchanged</p>\n",
			len(fd.Added), len(fd.Removed), len(fd.Modified), len(fd.Unchanged))
		r.writeFileChanges(fd, r.cfg.MaxFilesPerVertex)
	}
}

func (r *htmlReportRenderer) writeAddedVertices() {
	if len(r.d.Added) == 0 {
		return
	}
	r.write("<h2>Added Vertices</h2>\n")
	r.write(`<table class="summary-table">
<thead><tr><th>Vertex</th><th>Root</th><th>Leaves</th><th>Cache Rate</th></tr></thead>
<tbody>
`)
	for _, t := range r.d.Added {
		r.writef("<tr><td class=\"digest\">%s</td><td class=\"digest\">%s</td><td>%d</td><td>%.0f%%</td></tr>\n",
			html.EscapeString(shortDigest(t.VertexID)),
			html.EscapeString(shortDigest(t.Root)),
			t.LeafCount,
			t.CacheHitRate()*100,
		)
	}
	r.write("</tbody></table>\n")
}

func (r *htmlReportRenderer) writeRemovedVertices() {
	if len(r.d.Removed) == 0 {
		return
	}
	r.write("<h2>Removed Vertices</h2>\n")
	r.write(`<table class="summary-table">
<thead><tr><th>Vertex</th><th>Root</th><th>Leaves</th></tr></thead>
<tbody>
`)
	for _, t := range r.d.Removed {
		r.writef("<tr><td class=\"digest\">%s</td><td class=\"digest\">%s</td><td>%d</td></tr>\n",
			html.EscapeString(shortDigest(t.VertexID)),
			html.EscapeString(shortDigest(t.Root)),
			t.LeafCount,
		)
	}
	r.write("</tbody></table>\n")
}

func (r *htmlReportRenderer) writeUnchangedVertices() {
	if len(r.d.Unchanged) == 0 {
		return
	}
	r.writef("<h2>Unchanged Vertices (%d)</h2>\n", len(r.d.Unchanged))
	r.write(`<table class="summary-table">
<thead><tr><th>Vertex</th><th>Root</th><th>Leaves</th></tr></thead>
<tbody>
`)
	for _, t := range r.d.Unchanged {
		r.writef("<tr><td class=\"digest\">%s</td><td class=\"digest\">%s</td><td>%d</td></tr>\n",
			html.EscapeString(shortDigest(t.VertexID)),
			html.EscapeString(shortDigest(t.Root)),
			t.LeafCount,
		)
	}
	r.write("</tbody></table>\n")
}

func (r *htmlReportRenderer) writeFileChanges(fd *FileDiff, maxPerSection int) {
	sections := []struct {
		label   string
		class   string
		changes []FileChange
	}{
		{"Added files", "file-added", fd.Added},
		{"Removed files", "file-removed", fd.Removed},
		{"Modified files", "file-changed", fd.Modified},
	}
	for _, sec := range sections {
		if len(sec.changes) == 0 {
			continue
		}
		r.writef("<p><strong>%s (%d)</strong></p>\n",
			html.EscapeString(sec.label), len(sec.changes))
		r.writef(`<table class="file-table"><thead><tr>
<th>Path</th><th>Content Digest</th><th>Owner Layer</th>
</tr></thead><tbody>
`)
		limit := len(sec.changes)
		if maxPerSection > 0 && limit > maxPerSection {
			limit = maxPerSection
		}
		for _, fc := range sec.changes[:limit] {
			digestStr := string(fc.AfterHash)
			if digestStr == "" {
				digestStr = string(fc.BeforeHash)
			}
			r.writef("<tr class=\"%s\"><td>%s</td><td class=\"digest\">%s</td><td class=\"digest\">%s</td></tr>\n",
				sec.class,
				html.EscapeString(fc.RelPath),
				html.EscapeString(shortDigest(FileHash(digestStr))),
				html.EscapeString(shortDigest(fc.OwnerLayerID)),
			)
		}
		if limit < len(sec.changes) {
			r.writef("<tr><td colspan=\"3\">… and %d more</td></tr>\n",
				len(sec.changes)-limit)
		}
		r.write("</tbody></table>\n")
	}
}

func (r *htmlReportRenderer) writeFooter() {
	r.write("<footer>Generated by layermerkle · github.com/bons/bons-ci/pkg/layermerkle</footer>\n")
	r.write("</body></html>\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// MarkdownReport — lightweight text report for PR comments
// ─────────────────────────────────────────────────────────────────────────────

// WriteMarkdownReport renders a ForestDiff as GitHub-flavored Markdown to w.
// Suitable for posting to PR review comments or CI job summaries.
func WriteMarkdownReport(w io.Writer, d *ForestDiff, buildID string) error {
	rr := &mdReportRenderer{w: w, d: d}
	return rr.render(buildID)
}

type mdReportRenderer struct {
	w   io.Writer
	d   *ForestDiff
	err error
}

func (r *mdReportRenderer) write(s string) {
	if r.err == nil {
		_, r.err = io.WriteString(r.w, s)
	}
}

func (r *mdReportRenderer) writef(format string, args ...any) {
	r.write(fmt.Sprintf(format, args...))
}

func (r *mdReportRenderer) render(buildID string) error {
	r.write("## layermerkle Build Comparison\n\n")
	if buildID != "" {
		r.writef("**Build:** `%s`\n\n", buildID)
	}

	r.write("### Summary\n\n")
	r.write("| Category | Count |\n|----------|-------|\n")
	r.writef("| ✅ Unchanged | %d |\n", len(r.d.Unchanged))
	r.writef("| 🆕 Added | %d |\n", len(r.d.Added))
	r.writef("| 🗑️ Removed | %d |\n", len(r.d.Removed))
	r.writef("| 🔄 Changed | %d |\n\n", len(r.d.Changed))

	if len(r.d.Changed) > 0 {
		r.write("### Changed Vertices\n\n")
		for _, c := range r.d.Changed {
			fd := c.ComputeFileDiff()
			r.writef("#### `%s`\n\n", shortDigest(c.VertexID))
			r.writef("- Root: `%s` → `%s`\n",
				shortDigest(c.Before.Root), shortDigest(c.After.Root))
			r.writef("- Files: +%d / -%d / ~%d\n\n",
				len(fd.Added), len(fd.Removed), len(fd.Modified))
			if len(fd.Added)+len(fd.Removed)+len(fd.Modified) > 0 {
				r.write("<details><summary>File changes</summary>\n\n")
				r.write("| Status | Path | Digest |\n|--------|------|--------|\n")
				writeFileRows(r, fd.Added, "➕")
				writeFileRows(r, fd.Removed, "➖")
				writeFileRows(r, fd.Modified, "🔄")
				r.write("\n</details>\n\n")
			}
		}
	}

	if d := r.d; !d.IsIdentical() {
		r.write("> 💡 Run `lminspect -diff before.json after.json` for the full report.\n")
	} else {
		r.write("> ✅ Forests are identical — no changes detected.\n")
	}

	return r.err
}

func writeFileRows(r *mdReportRenderer, changes []FileChange, symbol string) {
	for _, fc := range changes {
		digest := string(fc.AfterHash)
		if digest == "" {
			digest = string(fc.BeforeHash)
		}
		r.writef("| %s | `%s` | `%s` |\n",
			symbol,
			strings.ReplaceAll(fc.RelPath, "`", "'"),
			shortDigest(FileHash(digest)),
		)
	}
}

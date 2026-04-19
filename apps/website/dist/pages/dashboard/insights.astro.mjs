import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute, F as Fragment } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../chunks/DashboardLayout_CthgR4Bm.mjs';
import { g as MOCK_METRICS, f as formatDuration } from '../../chunks/mock_JVAQtub_.mjs';
export { renderers } from '../../renderers.mjs';

const $$Index = createComponent(($$result, $$props, $$slots) => {
  const projectPercentiles = MOCK_METRICS.waterfall_percentiles.slice(0, 4).map((wf) => ({
    project: wf.project,
    p50: wf.p50 * 1e3,
    p75: wf.p75 * 1e3,
    p90: wf.p90 * 1e3,
    p95: wf.p95 * 1e3,
    p99: wf.p99 * 1e3,
    max: wf.max * 1e3
  }));
  function seeded(seed) {
    let s = seed;
    return () => {
      s = s * 1664525 + 1013904223 & 4294967295;
      return (s >>> 0) / 4294967295;
    };
  }
  const r = seeded(42);
  const heatCells = [];
  for (let day = 0; day < 7; day++) {
    for (let hour = 0; hour < 24; hour++) {
      const workday = day >= 1 && day <= 5;
      const workhour = hour >= 9 && hour <= 18;
      const base = workday && workhour ? 14 : workday ? 4 : 1;
      heatCells.push({ day, hour, count: Math.floor(base * (0.4 + r() * 1.6)), successRate: 82 + r() * 17 });
    }
  }
  const maxCount = Math.max(...heatCells.map((c) => c.count), 1);
  function countToColStr(count) {
    const t = count / maxCount;
    if (t === 0) return "#1A1A1A";
    if (t < 0.2) return "#1A2A1A";
    if (t < 0.4) return "#1A4A1A";
    if (t < 0.7) return "#00AA55";
    return "#00FF88";
  }
  const DAYS_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
  const buildCountSeries = MOCK_METRICS.series.build_count.slice(-30);
  const successRateSeries = MOCK_METRICS.series.success_rate.slice(-30);
  const p99Series = MOCK_METRICS.series.avg_duration.slice(-14);
  const maxBuildVol = Math.max(...buildCountSeries.map((v) => v.value), 1);
  const p99Max = Math.max(...p99Series.map((v) => v.value), 1);
  const memberLeaderboard = MOCK_METRICS.by_member.slice().sort((a, b) => b.build_count - a.build_count);
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Insights \u2014 Forge CI", "activeNav": "insights" }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-6 space-y-6"> <!-- Page header --> <div class="flex items-center justify-between"> <div> <h1 class="text-xl font-bold" style="font-family:'Space Grotesk';color:#F0F0F0">Build Insights</h1> <p class="text-xs font-mono mt-1" style="color:#666666">30-day window · auto-refresh every 60s</p> </div> <div class="flex gap-2"> <button class="btn-secondary btn-sm text-xs">Export CSV</button> <select class="input text-xs px-3 py-1.5 w-28"> <option>Last 30 days</option> <option>Last 7 days</option> <option>Last 90 days</option> </select> </div> </div> <!-- KPI metric cards --> <div class="grid grid-cols-2 lg:grid-cols-4 gap-0 border-l-2 border-t-2" style="border-color:#2C2C2C"> ${[
    { l: "P50 duration", v: formatDuration(MOCK_METRICS.duration_percentiles.p50.at(-1) ?? 0), delta: "-12%", up: false, col: "#00FF88", icon: "\u25C8" },
    { l: "P99 duration", v: formatDuration(MOCK_METRICS.duration_percentiles.p99.at(-1) ?? 0), delta: "-8%", up: false, col: "#FFEE00", icon: "\u25CE" },
    { l: "Cache hit rate", v: `${MOCK_METRICS.cache_hit_rate.toFixed(0)}%`, delta: "+4%", up: true, col: "#33AAFF", icon: "\u25C6" },
    { l: "Build failure rate", v: `${(100 - MOCK_METRICS.success_rate).toFixed(1)}%`, delta: "-1.2%", up: false, col: "#FF7700", icon: "\u25A4" }
  ].map((m) => renderTemplate`<div class="border-r-2 border-b-2 p-5" style="border-color:#2C2C2C"> <div class="flex items-center gap-2 mb-2"> <span${addAttribute(`color:${m.col};font-size:0.75rem`, "style")}>${m.icon}</span> <span class="text-xs font-mono" style="color:#666666">${m.l}</span> </div> <div class="text-2xl font-bold"${addAttribute(`font-family:'Space Grotesk';color:${m.col};letter-spacing:-0.04em`, "style")}>${m.v}</div> <div class="text-xs font-mono mt-1"${addAttribute(`color:${m.up ? "#00FF88" : "#FF7700"}`, "style")}>${m.delta} vs prior period</div> </div>`)} </div> <!-- Build activity heatmap + P99 trend --> <div class="grid lg:grid-cols-[1fr_300px] gap-6"> <!-- Heatmap --> <div class="card overflow-hidden"> <div class="px-5 py-3 border-b-2 flex items-center gap-3" style="border-color:#2C2C2C;background:#111111"> <span style="color:#FFEE00">◈</span> <span class="text-xs font-mono font-bold uppercase tracking-widest" style="color:#AAAAAA">Build Activity Heatmap — Day × Hour (UTC)</span> </div> <div class="p-5 overflow-x-auto"> <!-- Heatmap grid --> <div style="font-family:'JetBrains Mono',monospace;font-size:9px"> <svg style="display:block"${addAttribute(32 + 24 * 23 + 22, "width")}${addAttribute(20 + 7 * 19 + 20, "height")}> <!-- Day labels --> ${DAYS_SHORT.map((d, di) => renderTemplate`<text${addAttribute(28, "x")}${addAttribute(20 + di * 19 + 13, "y")} text-anchor="end"${addAttribute(9, "font-size")} fill="#3C3C3C">${d}</text>`)} <!-- Hour labels --> ${[0, 4, 8, 12, 16, 20, 23].map((h) => renderTemplate`<text${addAttribute(32 + h * 23 + 11, "x")}${addAttribute(20 + 7 * 19 + 14, "y")} text-anchor="middle"${addAttribute(9, "font-size")} fill="#3C3C3C">${String(h).padStart(2, "0")}</text>`)} <!-- Cells --> ${heatCells.map((cell, i) => renderTemplate`<rect${addAttribute(32 + cell.hour * 23, "x")}${addAttribute(20 + cell.day * 19, "y")}${addAttribute(21, "width")}${addAttribute(17, "height")}${addAttribute(countToColStr(cell.count), "fill")} stroke="#111111"${addAttribute(1, "stroke-width")}> <title>${DAYS_SHORT[cell.day]} ${String(cell.hour).padStart(2, "0")}:00 — ${cell.count} builds · ${cell.successRate.toFixed(0)}% success</title> </rect>`)} </svg> <!-- Legend --> <div class="flex items-center gap-2 mt-3 text-xs font-mono" style="color:#3C3C3C"> <span>Less</span> ${["#1A1A1A", "#1A2A1A", "#1A4A1A", "#00AA55", "#00FF88"].map((c) => renderTemplate`<div${addAttribute(`width:14px;height:14px;background:${c};border:1px solid #2C2C2C`, "style")}></div>`)} <span>More</span> </div> </div> </div> </div> <!-- P99 trend sparkline --> <div class="card overflow-hidden"> <div class="px-5 py-3 border-b-2 flex items-center gap-3" style="border-color:#2C2C2C;background:#111111"> <span style="color:#FF7700">◎</span> <span class="text-xs font-mono font-bold uppercase tracking-widest" style="color:#AAAAAA">P99 Trend (14 days)</span> </div> <div class="p-5"> <svg${addAttribute(`0 0 280 100`, "viewBox")} style="width:100%;height:100px"> ${[0, 33, 66, 100].map((y) => renderTemplate`<line${addAttribute(0, "x1")}${addAttribute(y, "y1")}${addAttribute(280, "x2")}${addAttribute(y, "y2")} stroke="#2C2C2C"${addAttribute(0.5, "stroke-width")}></line>`)} <polyline${addAttribute(p99Series.map(
    (h, i) => `${i / (p99Series.length - 1) * 280},${100 - h.value / p99Max * 90}`
  ).join(" "), "points")} fill="none" stroke="#FF7700"${addAttribute(2, "stroke-width")} stroke-linejoin="round"></polyline> </svg> <div class="mt-3 space-y-2"> ${[
    { l: "Current P99", v: formatDuration(p99Series.at(-1)?.value ?? 0), col: "#FF7700" },
    { l: "7-day avg P99", v: formatDuration(Math.round(p99Series.slice(-7).reduce((s, h) => s + h.value, 0) / 7)), col: "#FFEE00" }
  ].map((m) => renderTemplate`<div class="flex justify-between text-xs font-mono"> <span style="color:#666666">${m.l}</span> <strong${addAttribute(`color:${m.col}`, "style")}>${m.v}</strong> </div>`)} </div> </div> </div> </div> <!-- Per-project percentile bars --> <div class="card overflow-hidden"> <div class="px-5 py-3 border-b-2 flex items-center gap-3" style="border-color:#2C2C2C;background:#111111"> <span style="color:#FFEE00">▤</span> <span class="text-xs font-mono font-bold uppercase tracking-widest" style="color:#AAAAAA">Build Duration Percentiles — per Project</span> </div> <div class="p-5 space-y-5"> <!-- Legend --> <div class="flex gap-4 flex-wrap"> ${[
    { l: "P50", col: "#00FF88" },
    { l: "P75", col: "#FFEE00" },
    { l: "P90", col: "#FF7700" },
    { l: "P95", col: "#FF5500" },
    { l: "P99", col: "#FF3333" }
  ].map((p) => renderTemplate`<div class="flex items-center gap-2 text-xs font-mono" style="color:#AAAAAA"> <div${addAttribute(`width:10px;height:10px;background:${p.col}`, "style")}></div> ${p.l} </div>`)} </div> ${projectPercentiles.map((row) => {
    const globalMax = Math.max(...projectPercentiles.map((r2) => r2.max), 1);
    const pcts = [
      { key: "p50", col: "#00FF88" },
      { key: "p75", col: "#FFEE00" },
      { key: "p90", col: "#FF7700" },
      { key: "p95", col: "#FF5500" },
      { key: "p99", col: "#FF3333" }
    ];
    return renderTemplate`<div> <div class="text-xs font-mono mb-2" style="color:#AAAAAA">${row.project}</div> <div class="relative h-5" style="background:#111111;border:1px solid #2C2C2C"> ${pcts.map((p) => renderTemplate`<div${addAttribute(`${p.key.toUpperCase()}: ${formatDuration(row[p.key])}`, "title")}${addAttribute(`position:absolute;left:0;top:0;width:${row[p.key] / globalMax * 100}%;height:100%;background:${p.col};opacity:0.85`, "style")}></div>`)} </div> <div class="flex gap-4 mt-1"> ${pcts.map((p) => renderTemplate`<span class="text-xs font-mono"${addAttribute(`color:${p.col}`, "style")}> ${p.key.toUpperCase()}: ${formatDuration(row[p.key])} </span>`)} </div> </div>`;
  })} </div> </div> <!-- Build volume + success rate (stacked bar sparkline) --> <div class="grid lg:grid-cols-2 gap-6"> <!-- Volume bar chart (SVG) --> <div class="card overflow-hidden"> <div class="px-5 py-3 border-b-2 flex items-center gap-3" style="border-color:#2C2C2C;background:#111111"> <span style="color:#00FF88">◆</span> <span class="text-xs font-mono font-bold uppercase tracking-widest" style="color:#AAAAAA">Build Volume (30 days)</span> </div> <div class="p-5"> <svg viewBox="0 0 600 100" style="width:100%;height:100px"> ${buildCountSeries.map((h, i) => {
    const total = h.value;
    const failed = Math.round(total * (1 - MOCK_METRICS.success_rate / 100));
    const success = total - failed;
    const barH = total / maxBuildVol * 90;
    const failH = failed / maxBuildVol * 90;
    const x = i / buildCountSeries.length * 600;
    const bw = 600 / buildCountSeries.length - 2;
    return renderTemplate`${renderComponent($$result2, "Fragment", Fragment, {}, { "default": ($$result3) => renderTemplate` <rect${addAttribute(x, "x")}${addAttribute(100 - barH, "y")}${addAttribute(bw, "width")}${addAttribute(barH - failH, "height")} fill="#00FF88" opacity="0.8"> <title>${h.date}: ${success} passed, ${failed} failed</title> </rect> ${failed > 0 && renderTemplate`<rect${addAttribute(x, "x")}${addAttribute(100 - failH, "y")}${addAttribute(bw, "width")}${addAttribute(failH, "height")} fill="#FF3333" opacity="0.85"> <title>${failed} failed</title> </rect>`}` })}`;
  })} </svg> <div class="flex items-center gap-4 mt-2 text-xs font-mono" style="color:#3C3C3C"> <div class="flex items-center gap-1.5"><div class="w-3 h-3" style="background:#00FF88"></div>Passed</div> <div class="flex items-center gap-1.5"><div class="w-3 h-3" style="background:#FF3333"></div>Failed</div> </div> </div> </div> <!-- Success rate trend --> <div class="card overflow-hidden"> <div class="px-5 py-3 border-b-2 flex items-center gap-3" style="border-color:#2C2C2C;background:#111111"> <span style="color:#00FF88">◎</span> <span class="text-xs font-mono font-bold uppercase tracking-widest" style="color:#AAAAAA">Success Rate (30 days)</span> </div> <div class="p-5"> ${(() => {
    const rates = successRateSeries;
    const minR = 80;
    const maxR = 100;
    const points = rates.map((h, i) => `${i / (rates.length - 1) * 600},${100 - (h.value - minR) / (maxR - minR) * 90}`).join(" ");
    const area = `M 0,100 L ${points.split(" ").join(" L ")} L 600,100 Z`;
    return renderTemplate`${renderComponent($$result2, "Fragment", Fragment, {}, { "default": ($$result3) => renderTemplate` <svg viewBox="0 0 600 100" style="width:100%;height:100px"> <defs> <linearGradient id="rg" x1="0" y1="0" x2="0" y2="1"> <stop offset="0%" stop-color="#00FF88" stop-opacity="0.18"></stop> <stop offset="100%" stop-color="#00FF88" stop-opacity="0"></stop> </linearGradient> </defs> ${[80, 90, 95, 100].map((y) => renderTemplate`${renderComponent($$result3, "Fragment", Fragment, {}, { "default": ($$result4) => renderTemplate` <line${addAttribute(0, "x1")}${addAttribute(100 - (y - minR) / (maxR - minR) * 90, "y1")}${addAttribute(600, "x2")}${addAttribute(100 - (y - minR) / (maxR - minR) * 90, "y2")} stroke="#2C2C2C"${addAttribute(0.5, "stroke-width")}></line> <text${addAttribute(2, "x")}${addAttribute(100 - (y - minR) / (maxR - minR) * 90 - 2, "y")}${addAttribute(8, "font-size")} fill="#3C3C3C" font-family="&quot;JetBrains Mono&quot;,monospace">${y}%</text> ` })}`)} <!-- 95% SLA line --> <line${addAttribute(0, "x1")}${addAttribute(100 - (95 - minR) / (maxR - minR) * 90, "y1")}${addAttribute(600, "x2")}${addAttribute(100 - (95 - minR) / (maxR - minR) * 90, "y2")} stroke="#FF7700"${addAttribute(1, "stroke-width")} stroke-dasharray="4 4"></line> <path${addAttribute(area, "d")} fill="url(#rg)"></path> <polyline${addAttribute(points, "points")} fill="none" stroke="#00FF88"${addAttribute(2, "stroke-width")} stroke-linejoin="round"></polyline> </svg> <div class="flex justify-between mt-2 text-xs font-mono"> <span style="color:#666666">Current: <strong style="color:#00FF88">${(rates.at(-1)?.value ?? 0).toFixed(1)}%</strong></span> <span style="color:#FF7700">— 95% SLA</span> </div> ` })}`;
  })()} </div> </div> </div> <!-- Member leaderboard --> <div class="card overflow-hidden"> <div class="px-5 py-3 border-b-2 flex items-center gap-3" style="border-color:#2C2C2C;background:#111111"> <span style="color:#33AAFF">▣</span> <span class="text-xs font-mono font-bold uppercase tracking-widest" style="color:#AAAAAA">Author Build Leaderboard (30 days)</span> </div> <div class="overflow-x-auto"> <table class="table-brut w-full"> <thead> <tr> <th>#</th> <th>Author</th> <th>Builds</th> <th>Success rate</th> <th>Avg duration</th> <th>Cache hits</th> </tr> </thead> <tbody> ${memberLeaderboard.map((m, i) => renderTemplate`<tr> <td><span class="font-mono text-xs" style="color:#3C3C3C">#${i + 1}</span></td> <td> <div class="flex items-center gap-2"> <div class="avatar-sm">${m.initials}</div> <span class="text-sm font-medium" style="color:#F0F0F0">${m.user_name}</span> </div> </td> <td><span class="font-mono font-bold" style="color:#FFEE00">${m.build_count}</span></td> <td> <span class="font-mono"${addAttribute(`color:${m.success_rate >= 95 ? "#00FF88" : "#FF7700"}`, "style")}> ${m.success_rate.toFixed(0)}%
</span> </td> <td><span class="font-mono text-xs" style="color:#AAAAAA">${formatDuration(m.avg_duration_ms)}</span></td> <td> <span class="font-mono" style="color:#33AAFF">${Math.round(m.build_count * 0.88)}%</span> </td> </tr>`)} </tbody> </table> </div> </div> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/insights/index.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/insights/index.astro";
const $$url = "/dashboard/insights";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Index,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

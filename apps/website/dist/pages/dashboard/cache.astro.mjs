import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../chunks/DashboardLayout_CthgR4Bm.mjs';
import { e as MOCK_CACHE_KEYS, g as MOCK_METRICS, h as formatBytes } from '../../chunks/mock_JVAQtub_.mjs';
import { s as seriesToSmoothPath, a as seriesToAreaPath } from '../../chunks/charts_Be-e38Q_.mjs';
export { renderers } from '../../renderers.mjs';

const $$Cache = createComponent(($$result, $$props, $$slots) => {
  const keys = MOCK_CACHE_KEYS;
  const totalUsed = keys.reduce((s, k) => s + k.size_bytes, 0);
  const totalLimit = 107374182400;
  const usedPct = Math.round(totalUsed / totalLimit * 100);
  const cacheData = MOCK_METRICS.series.cache_hit_rate;
  const sparkPath = seriesToSmoothPath(cacheData, { width: 560, height: 80, paddingTop: 4, paddingBottom: 4 });
  const sparkArea = seriesToAreaPath(cacheData, { width: 560, height: 80, paddingTop: 4, paddingBottom: 4 });
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Cache", "activeNav": "cache", "breadcrumbs": [{ label: "Cache" }] }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-4 sm:p-6 lg:p-8 max-w-[1200px] mx-auto space-y-5"> <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4"> <div><h1 class="font-bold text-2xl text-[#F0F0F0]">Cache</h1><p class="text-[#666666] text-sm mt-0.5">Content-addressed build cache · ${formatBytes(totalUsed)} / 100 GB used</p></div> <div class="flex gap-2"> <a href="/dashboard/settings/cache" class="btn-secondary btn-sm">Cache settings</a> <button class="btn-danger btn-sm">Invalidate all</button> </div> </div> <!-- Stats --> <div class="grid grid-cols-2 sm:grid-cols-4 gap-3"> ${[
    { l: "Hit rate (7d)", v: "89.2%", c: "text-[#00FF88]" },
    { l: "Storage used", v: formatBytes(totalUsed), c: "text-[#F0F0F0]" },
    { l: "Active keys", v: String(keys.length), c: "text-[#F0F0F0]" },
    { l: "Time saved (30d)", v: "42h", c: "text-[#FFEE00]" }
  ].map((s) => renderTemplate`<div class="metric-card"><div class="text-xs text-[#666666]">${s.l}</div><div${addAttribute(`font-bold text-2xl mt-1 ${s.c}`, "class")}>${s.v}</div></div>`)} </div> <!-- Hit rate chart --> <div class="card p-5"> <div class="flex items-center justify-between mb-4"> <div><h2 class="font-bold text-base text-[#F0F0F0]">Hit rate over 30 days</h2></div> <span class="badge-success border border-[rgba(0,255,136,0.2)] text-xs">Trending up +3.1%</span> </div> <div class="relative" style="height:96px"> <svg class="w-full h-full" viewBox="0 0 560 80" preserveAspectRatio="none"> <defs><linearGradient id="cacheGrad" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#00FF88" stop-opacity="0.2"></stop><stop offset="100%" stop-color="#00FF88" stop-opacity="0"></stop></linearGradient></defs> ${[0, 20, 40, 60, 80].map((y) => renderTemplate`<line x1="0"${addAttribute(y, "y1")} x2="560"${addAttribute(y, "y2")} stroke="rgba(30,45,66,0.5)" stroke-width="0.5"></line>`)} <path${addAttribute(sparkArea, "d")} fill="url(#cacheGrad)"></path> <path${addAttribute(sparkPath, "d")} fill="none" stroke="#00FF88" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"></path> </svg> <div class="absolute top-0 left-0 h-full flex flex-col justify-between text-xs font-mono text-[#666666] py-0.5"> <span>100%</span><span>50%</span><span>0%</span> </div> <div class="absolute bottom-0 left-4 right-0 flex justify-between -mb-5 text-xs font-mono text-[#666666]"> <span>Mar 16</span><span>Mar 23</span><span>Mar 30</span><span>Apr 7</span><span>Today</span> </div> </div> </div> <!-- Storage bar --> <div class="card p-5"> <div class="flex justify-between text-xs mb-2"><span class="text-[#666666]">Storage used</span><span class="font-mono text-[#F0F0F0]">${formatBytes(totalUsed)} / 100 GB · ${usedPct}%</span></div> <div class="progress h-2.5"><div class="progress-bar"${addAttribute(`width:${usedPct}%`, "style")}></div></div> <div class="flex justify-between text-xs text-[#666666] mt-2"> ${keys.sort((a, b) => b.size_bytes - a.size_bytes).slice(0, 3).map((k) => renderTemplate`<div class="flex items-center gap-1.5"><div class="w-2 h-2 rounded-full bg-[#FFEE00] opacity-70"></div><span>${k.project_name}/${k.key} · ${formatBytes(k.size_bytes)}</span></div>`)} </div> </div> <!-- Keys table --> <div class="card overflow-hidden"> <div class="flex items-center justify-between px-5 py-4 border-b border-[#2C2C2C]"> <h2 class="font-bold text-base text-[#F0F0F0]">Cache keys (${keys.length})</h2> <div class="flex gap-2"> <select class="input !py-1.5 !text-xs w-36"><option>All projects</option>${[...new Set(keys.map((k) => k.project_name))].map((p) => renderTemplate`<option>${p}</option>`)}</select> <button class="btn-danger btn-sm text-xs">Invalidate all</button> </div> </div> <table class="table-forge"> <thead><tr><th>Cache key</th><th>Project</th><th>Hit rate</th><th class="hidden md:table-cell">Hits / Misses</th><th>Size</th><th class="hidden lg:table-cell">Last hit</th><th class="hidden xl:table-cell">TTL</th><th></th></tr></thead> <tbody> ${keys.map((k) => renderTemplate`<tr class="group"> <td><code class="text-xs font-mono text-[#FFEE00]">${k.key}</code></td> <td><span class="text-xs text-[#AAAAAA]">${k.project_name}</span></td> <td> <div class="flex items-center gap-2"> <div class="w-16 h-1.5 rounded-full bg-[#1A1A1A] overflow-hidden"><div${addAttribute(["h-full rounded-full", k.hit_rate > 95 ? "bg-[#00FF88]" : k.hit_rate > 85 ? "bg-[#FF7700]" : "bg-[#FF3333]"], "class:list")}${addAttribute(`width:${k.hit_rate}%`, "style")}></div></div> <span${addAttribute(["text-xs font-mono", k.hit_rate > 95 ? "text-[#00FF88]" : k.hit_rate > 85 ? "text-[#FF7700]" : "text-[#FF3333]"], "class:list")}>${k.hit_rate}%</span> </div> </td> <td class="hidden md:table-cell"><span class="text-xs font-mono text-[#AAAAAA]">${k.hit_count}/${k.miss_count}</span></td> <td><span class="text-xs font-mono text-[#AAAAAA]">${formatBytes(k.size_bytes)}</span></td> <td class="hidden lg:table-cell"><span class="text-xs text-[#666666]">${k.last_hit_at.slice(11, 16)} UTC</span></td> <td class="hidden xl:table-cell"><span class="text-xs font-mono text-[#666666]">${k.ttl_days}d</span></td> <td><button class="btn-danger btn-sm text-xs opacity-0 group-hover:opacity-100 transition-opacity">Invalidate</button></td> </tr>`)} </tbody> </table> </div> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/cache.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/cache.astro";
const $$url = "/dashboard/cache";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Cache,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

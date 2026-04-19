import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../chunks/DashboardLayout_CthgR4Bm.mjs';
export { renderers } from '../../renderers.mjs';

const $$Artifacts = createComponent(($$result, $$props, $$slots) => {
  const artifacts = [
    { id: "a1", build: "#1847", project: "api-service", branch: "main", name: "coverage-report.html", type: "text/html", size: 21e5, created: "2 min ago", expires: "88 days", icon: "\u{1F4CA}" },
    { id: "a2", build: "#1847", project: "api-service", branch: "main", name: "test-results.xml", type: "application/xml", size: 48e3, created: "2 min ago", expires: "88 days", icon: "\u{1F9EA}" },
    { id: "a3", build: "#1847", project: "api-service", branch: "main", name: "build.log", type: "text/plain", size: 131e3, created: "2 min ago", expires: "88 days", icon: "\u{1F4C4}" },
    { id: "a4", build: "#1843", project: "mobile", branch: "release/2.4", name: "app-release.apk", type: "application/vnd.android.package-archive", size: 894e5, created: "41 min ago", expires: "87 days", icon: "\u{1F4F1}" },
    { id: "a5", build: "#1843", project: "mobile", branch: "release/2.4", name: "app-release.ipa", type: "application/octet-stream", size: 112e6, created: "41 min ago", expires: "87 days", icon: "\u{1F34E}" },
    { id: "a6", build: "#1843", project: "mobile", branch: "release/2.4", name: "dsyms.zip", type: "application/zip", size: 24e6, created: "41 min ago", expires: "87 days", icon: "\u{1F5DC}" },
    { id: "a7", build: "#1844", project: "infra", branch: "main", name: "terraform-plan.txt", type: "text/plain", size: 18e3, created: "22 min ago", expires: "87 days", icon: "\u{1F4CB}" },
    { id: "a8", build: "#1844", project: "infra", branch: "main", name: "tfstate.json", type: "application/json", size: 44e3, created: "22 min ago", expires: "87 days", icon: "\u{1F4E6}" },
    { id: "a9", build: "#1842", project: "web-app", branch: "main", name: "bundle-analysis.html", type: "text/html", size: 34e5, created: "1 hour ago", expires: "87 days", icon: "\u{1F4CA}" },
    { id: "a10", build: "#1842", project: "web-app", branch: "main", name: "lighthouse-report.json", type: "application/json", size: 22e4, created: "1 hour ago", expires: "87 days", icon: "\u{1F4A1}" },
    { id: "a11", build: "#1840", project: "docs", branch: "main", name: "site.tar.gz", type: "application/gzip", size: 58e5, created: "3 hours ago", expires: "87 days", icon: "\u{1F5DC}" },
    { id: "a12", build: "#1838", project: "mobile", branch: "fix/crash", name: "crashlog.txt", type: "text/plain", size: 8400, created: "4 hours ago", expires: "86 days", icon: "\u{1F4A5}" }
  ];
  const typeGroups = {
    "All": [],
    "Reports": ["text/html", "application/json"],
    "Logs": ["text/plain"],
    "Binaries": ["application/vnd.android.package-archive", "application/octet-stream"],
    "Archives": ["application/zip", "application/gzip", "application/x-tar"],
    "Data": ["application/xml"]
  };
  function fmtSize(b) {
    if (b >= 1e9) return (b / 1e9).toFixed(1) + " GB";
    if (b >= 1e6) return (b / 1e6).toFixed(1) + " MB";
    if (b >= 1e3) return (b / 1e3).toFixed(0) + " KB";
    return b + " B";
  }
  const totalSize = artifacts.reduce((s, a) => s + a.size, 0);
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Artifacts", "activeNav": "artifacts", "breadcrumbs": [{ label: "Artifacts" }] }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-4 sm:p-6 lg:p-8 max-w-[1400px] mx-auto space-y-5"> <!-- Header --> <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4"> <div> <h1 class="font-bold text-2xl text-[#F0F0F0]">Artifacts</h1> <p class="text-[#F0F0F0]-3 text-sm mt-0.5"> ${artifacts.length} artifacts · ${fmtSize(totalSize)} total · retained for 90 days
</p> </div> <div class="flex gap-2"> <button class="btn-secondary btn-sm"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"></path> </svg>
Retention settings
</button> <button class="btn-primary btn-sm"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"></path> </svg>
Download selected
</button> </div> </div> <!-- Filter bar --> <div class="flex flex-col sm:flex-row gap-3"> <!-- Type tabs --> <div class="flex gap-1 bg-[#111111] border border-[#2C2C2C] rounded-none p-1 flex-shrink-0 flex-wrap"> ${Object.keys(typeGroups).map((type, i) => renderTemplate`<button${addAttribute(`type-tab px-3 py-1.5 rounded-md text-xs font-medium transition-all ${i === 0 ? "bg-[#111111]2 text-[#F0F0F0]" : "text-[#F0F0F0]-3 hover:text-[#F0F0F0]"}`, "class")}${addAttribute(type, "data-type")} onclick="filterType(this)"> ${type} </button>`)} </div> <div class="flex gap-2 flex-1"> <div class="relative flex-1"> <svg class="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-[#F0F0F0]-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path> </svg> <input type="search" id="artifact-search" placeholder="Search artifacts, build IDs, branches…" class="input !pl-9 !py-2 !text-xs w-full"> </div> <select id="proj-filter" class="input !py-2 !text-xs w-36"> <option value="">All projects</option> ${["api-service", "web-app", "mobile", "infra", "docs"].map((p) => renderTemplate`<option${addAttribute(p, "value")}>${p}</option>`)} </select> </div> </div> <!-- Select all banner (when items checked) --> <div id="selection-bar" class="hidden card p-3 border-forge-accent/30 bg-forge-accent-muted/20 flex items-center gap-3"> <span class="text-sm text-[#F0F0F0]" id="selection-count">0 selected</span> <div class="ml-auto flex gap-2"> <button class="btn-secondary btn-sm text-xs">Download all</button> <button class="btn-danger btn-sm text-xs">Delete selected</button> <button class="btn-ghost btn-sm text-xs text-[#F0F0F0]-3" onclick="clearSelection()">Clear</button> </div> </div> <!-- Artifacts table --> <div class="card overflow-hidden"> <table class="table-forge" id="artifacts-table"> <thead> <tr> <th class="w-6"><input type="checkbox" id="select-all" class="rounded border-[#2C2C2C]"></th> <th>Name</th> <th>Build</th> <th class="hidden md:table-cell">Project / Branch</th> <th class="hidden sm:table-cell">Type</th> <th>Size</th> <th class="hidden lg:table-cell">Created</th> <th class="hidden xl:table-cell">Expires in</th> <th></th> </tr> </thead> <tbody id="artifacts-tbody"> ${artifacts.map((a) => renderTemplate`<tr class="artifact-row group"${addAttribute(a.type, "data-type")}${addAttribute(a.project, "data-project")}${addAttribute(`${a.name} ${a.build} ${a.branch} ${a.project}`.toLowerCase(), "data-text")}> <td onclick="event.stopPropagation()"> <input type="checkbox" class="artifact-check rounded border-[#2C2C2C]"${addAttribute(a.id, "data-id")}> </td> <td> <div class="flex items-center gap-2.5"> <span class="text-base flex-shrink-0" aria-hidden="true">${a.icon}</span> <div> <div class="text-xs font-medium text-[#F0F0F0] font-mono">${a.name}</div> </div> </div> </td> <td> <a${addAttribute(`/dashboard/builds/${a.build.replace("#", "")}`, "href")} class="text-xs font-mono text-[#FFEE00] hover:text-[#FFEE00]-dim transition-colors" onclick="event.stopPropagation()"> ${a.build} </a> </td> <td class="hidden md:table-cell"> <div class="text-xs text-[#F0F0F0]-2">${a.project}</div> <div class="text-2xs text-[#F0F0F0]-3 font-mono">${a.branch}</div> </td> <td class="hidden sm:table-cell"> <span class="text-2xs font-mono text-[#F0F0F0]-3">${a.type.split("/")[1]}</span> </td> <td> <span class="text-xs font-mono text-[#F0F0F0]-2">${fmtSize(a.size)}</span> </td> <td class="hidden lg:table-cell"> <span class="text-xs text-[#F0F0F0]-3">${a.created}</span> </td> <td class="hidden xl:table-cell"> <span${addAttribute(`text-xs font-mono ${parseInt(a.expires) < 7 ? "text-[#FF7700]" : "text-[#F0F0F0]-3"}`, "class")}> ${a.expires} </span> </td> <td> <div class="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity"> <button class="btn-ghost btn-sm !p-1.5 text-[#F0F0F0]-3 hover:text-[#FFEE00]" title="Download"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"></path> </svg> </button> <button class="btn-ghost btn-sm !p-1.5 text-[#F0F0F0]-3 hover:text-[#F0F0F0]" title="Copy URL"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"></path> </svg> </button> <button class="btn-ghost btn-sm !p-1.5 text-[#FF3333]" title="Delete"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"></path> </svg> </button> </div> </td> </tr>`)} </tbody> </table> <div id="artifacts-empty" class="hidden py-14 text-center"> <div class="text-3xl mb-3">📦</div> <h3 class="font-bold text-lg text-[#F0F0F0] mb-1">No artifacts found</h3> <p class="text-[#F0F0F0]-2 text-sm">Try changing filters or run a build that produces artifacts.</p> </div> </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/artifacts.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/artifacts.astro";
const $$url = "/dashboard/artifacts";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Artifacts,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

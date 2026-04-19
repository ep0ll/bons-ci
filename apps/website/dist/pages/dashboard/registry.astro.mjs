import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../chunks/DashboardLayout_CthgR4Bm.mjs';
export { renderers } from '../../renderers.mjs';

const $$Registry = createComponent(($$result, $$props, $$slots) => {
  const repos = [
    {
      id: "api-service",
      name: "acme-corp/api-service",
      description: "Core REST API service image",
      isPublic: false,
      tagCount: 48,
      sizeBytes: 384e7,
      pullCount: 12440,
      lastPushed: "2 minutes ago",
      lastTag: "main-a4f9c2b",
      vuln: { critical: 0, high: 1, medium: 4, low: 12 }
    },
    {
      id: "web-app",
      name: "acme-corp/web-app",
      description: "Next.js frontend application",
      isPublic: false,
      tagCount: 62,
      sizeBytes: 124e7,
      pullCount: 8820,
      lastPushed: "47 minutes ago",
      lastTag: "main-3d8e1a0",
      vuln: { critical: 0, high: 0, medium: 2, low: 6 }
    },
    {
      id: "worker",
      name: "acme-corp/worker",
      description: "Background job processor",
      isPublic: false,
      tagCount: 29,
      sizeBytes: 78e7,
      pullCount: 3210,
      lastPushed: "3 hours ago",
      lastTag: "main-e5f6a7b",
      vuln: { critical: 1, high: 2, medium: 5, low: 8 }
    },
    {
      id: "ml-runner",
      name: "acme-corp/ml-runner",
      description: "CUDA-enabled ML training environment",
      isPublic: false,
      tagCount: 11,
      sizeBytes: 142e8,
      pullCount: 440,
      lastPushed: "1 day ago",
      lastTag: "cuda12-py311",
      vuln: { critical: 0, high: 3, medium: 11, low: 24 }
    },
    {
      id: "base-node",
      name: "acme-corp/base-node",
      description: "Hardened Node.js base image",
      isPublic: true,
      tagCount: 18,
      sizeBytes: 22e7,
      pullCount: 98400,
      lastPushed: "3 days ago",
      lastTag: "node20-alpine",
      vuln: { critical: 0, high: 0, medium: 0, low: 2 }
    }
  ];
  const selectedRepo = repos[0];
  const tags = [
    { name: "main-a4f9c2b", digest: "sha256:3a1f9b\u2026", size: "384 MB", pushed: "2 min ago", pulled: "8 sec ago", os: "linux/amd64", vuln: { critical: 0, high: 1 } },
    { name: "main-3d8e1a0", digest: "sha256:7c8d2e\u2026", size: "381 MB", pushed: "1h ago", pulled: "3 min ago", os: "linux/amd64", vuln: { critical: 0, high: 1 } },
    { name: "main-f2a3b4c", digest: "sha256:1b2c3d\u2026", size: "379 MB", pushed: "3h ago", pulled: "28 min ago", os: "linux/amd64", vuln: { critical: 0, high: 1 } },
    { name: "v2.4.0", digest: "sha256:9a8b7c\u2026", size: "376 MB", pushed: "2d ago", pulled: "1h ago", os: "linux/amd64,linux/arm64", vuln: { critical: 0, high: 0 } },
    { name: "v2.3.1", digest: "sha256:4e5f6a\u2026", size: "371 MB", pushed: "5d ago", pulled: "2h ago", os: "linux/amd64,linux/arm64", vuln: { critical: 0, high: 0 } },
    { name: "v2.3.0", digest: "sha256:2b3c4d\u2026", size: "368 MB", pushed: "8d ago", pulled: "4h ago", os: "linux/amd64", vuln: { critical: 0, high: 2 } }
  ];
  function fmtBytes(b) {
    if (b >= 1e12) return (b / 1e12).toFixed(1) + " TB";
    if (b >= 1e9) return (b / 1e9).toFixed(1) + " GB";
    if (b >= 1e6) return (b / 1e6).toFixed(0) + " MB";
    return b + " B";
  }
  const totalUsed = repos.reduce((s, r) => s + r.sizeBytes, 0);
  const totalLimit = 107374182400;
  const usedPct = Math.round(totalUsed / totalLimit * 100);
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Image Registry", "activeNav": "registry", "breadcrumbs": [{ label: "Image Registry" }] }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-4 sm:p-6 lg:p-8 max-w-[1400px] mx-auto space-y-5"> <!-- Header --> <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4"> <div> <h1 class="font-bold text-2xl text-[#F0F0F0]">Image Registry</h1> <p class="text-[#F0F0F0]-3 text-sm mt-0.5">
OCI-compatible · <span class="font-mono">${fmtBytes(totalUsed)}</span> used of <span class="font-mono">100 GB</span> </p> </div> <div class="flex gap-2"> <button class="btn-secondary btn-sm"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"></path> </svg>
Retention policy
</button> <button class="btn-primary btn-sm"> <svg class="w-3.5 h-3.5" fill="currentColor" viewBox="0 0 20 20"> <path fill-rule="evenodd" d="M10 3a1 1 0 011 1v5h5a1 1 0 110 2h-5v5a1 1 0 11-2 0v-5H4a1 1 0 110-2h5V4a1 1 0 011-1z" clip-rule="evenodd"></path> </svg>
New repository
</button> </div> </div> <!-- Usage bar --> <div class="card p-4 flex items-center gap-5"> <div class="flex-1"> <div class="flex justify-between mb-2 text-xs"> <span class="text-[#F0F0F0]-2">Storage used</span> <span class="font-mono text-[#F0F0F0]">${fmtBytes(totalUsed)} / 100 GB · ${usedPct}%</span> </div> <div class="progress"> <div class="progress-bar"${addAttribute(`width: ${usedPct}%`, "style")}></div> </div> </div> <div class="flex gap-6 text-center flex-shrink-0"> ${[
    { label: "Repositories", value: repos.length },
    { label: "Total tags", value: repos.reduce((s, r) => s + r.tagCount, 0) },
    { label: "Total pulls (30d)", value: repos.reduce((s, r) => s + r.pullCount, 0).toLocaleString() }
  ].map(({ label, value }) => renderTemplate`<div> <div class="font-mono font-bold text-lg text-[#F0F0F0]">${value}</div> <div class="text-2xs text-[#F0F0F0]-3">${label}</div> </div>`)} </div> </div> <!-- Login command snippet --> <div class="card p-4"> <div class="flex items-center justify-between mb-2"> <span class="text-xs font-medium text-[#F0F0F0]-2">Registry endpoint</span> <span class="badge-accent text-2xs">OCI v1.1</span> </div> <div class="flex items-center gap-3"> <code class="flex-1 font-mono text-xs text-[#F0F0F0]-2 bg-[#111111]2 rounded-none px-4 py-2.5 overflow-x-auto">
docker login registry.forge-ci.dev -u acme-corp -p $FORGE_REGISTRY_TOKEN
</code> <button class="btn-secondary btn-sm flex-shrink-0" onclick="navigator.clipboard.writeText('docker login registry.forge-ci.dev -u acme-corp -p $FORGE_REGISTRY_TOKEN'); this.textContent='Copied!'; setTimeout(()=>this.textContent='Copy',1500)">Copy</button> </div> </div> <!-- Main content: repo list + tag detail --> <div class="grid lg:grid-cols-[320px_1fr] gap-4 items-start"> <!-- Repo list --> <div class="card overflow-hidden"> <div class="px-4 py-3.5 border-b border-[#2C2C2C]"> <div class="relative"> <svg class="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-[#F0F0F0]-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path> </svg> <input type="search" placeholder="Filter repositories…" class="input !pl-9 !py-2 !text-xs w-full"> </div> </div> <div class="divide-y divide-forge-border/60"> ${repos.map((repo, i) => {
    repo.vuln.critical + repo.vuln.high + repo.vuln.medium + repo.vuln.low;
    const hasCritical = repo.vuln.critical > 0;
    const hasHigh = repo.vuln.high > 0;
    return renderTemplate`<button${addAttribute(`w-full text-left px-4 py-3.5 hover:bg-[#111111]2/60 transition-colors ${i === 0 ? "bg-forge-accent-muted/20 border-l-2 border-forge-accent" : ""}`, "class")}${addAttribute(`selectRepo('${repo.id}')`, "onclick")}> <div class="flex items-start justify-between gap-2 mb-1"> <div> <div class="flex items-center gap-2"> <span class="text-xs font-medium text-[#F0F0F0] truncate">${repo.name.split("/")[1]}</span> <span${addAttribute(`badge text-2xs ${repo.isPublic ? "badge-info border border-[rgba(51,170,255,0.3)]" : "badge-neutral border border-[#2C2C2C]"}`, "class")}> ${repo.isPublic ? "Public" : "Private"} </span> </div> <div class="text-2xs text-[#F0F0F0]-3 mt-0.5 truncate">${repo.description}</div> </div> ${(hasCritical || hasHigh) && renderTemplate`<span${addAttribute(`badge text-2xs flex-shrink-0 ${hasCritical ? "badge-danger border border-forge-danger/25" : "badge-warning border border-forge-warning/25"}`, "class")}> ${hasCritical ? `${repo.vuln.critical} crit` : `${repo.vuln.high} high`} </span>`} </div> <div class="flex items-center gap-3 text-2xs font-mono text-[#F0F0F0]-3"> <span>${repo.tagCount} tags</span> <span>·</span> <span>${fmtBytes(repo.sizeBytes)}</span> <span>·</span> <span>${repo.lastPushed}</span> </div> </button>`;
  })} </div> </div> <!-- Tag detail panel --> <div class="space-y-4"> <!-- Repo header --> <div class="card p-5"> <div class="flex flex-col sm:flex-row sm:items-start gap-4"> <div class="flex-1 min-w-0"> <div class="flex items-center gap-2.5 mb-2 flex-wrap"> <h2 class="font-bold text-xl text-[#F0F0F0]">${selectedRepo.name}</h2> <span class="badge-neutral border border-[#2C2C2C] text-xs">Private</span> </div> <p class="text-sm text-[#F0F0F0]-3 mb-3">${selectedRepo.description}</p> <!-- Pull command --> <div class="flex items-center gap-2"> <code class="text-xs font-mono text-[#F0F0F0]-2 bg-[#111111]2 rounded px-3 py-1.5">
docker pull registry.forge-ci.dev/${selectedRepo.name}:${selectedRepo.lastTag} </code> <button class="text-[#F0F0F0]-3 hover:text-[#F0F0F0] transition-colors flex-shrink-0" onclick="navigator.clipboard.writeText('docker pull registry.forge-ci.dev/acme-corp/api-service:main-a4f9c2b')" title="Copy"> <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"></path> </svg> </button> </div> </div> <!-- Vuln summary --> <div class="card p-4 flex-shrink-0 min-w-[180px]"> <div class="text-xs font-medium text-[#F0F0F0] mb-3">Vulnerability scan</div> <div class="space-y-1.5"> ${[
    { label: "Critical", count: selectedRepo.vuln.critical, color: "text-[#FF3333]" },
    { label: "High", count: selectedRepo.vuln.high, color: "text-[#FF7700]" },
    { label: "Medium", count: selectedRepo.vuln.medium, color: "text-[#33AAFF]" },
    { label: "Low", count: selectedRepo.vuln.low, color: "text-[#F0F0F0]-3" }
  ].map(({ label, count, color }) => renderTemplate`<div class="flex items-center justify-between text-xs"> <span${addAttribute(color, "class")}>${label}</span> <span${addAttribute(`font-mono font-bold ${count > 0 ? color : "text-[#F0F0F0]-3"}`, "class")}>${count}</span> </div>`)} </div> <button class="btn-secondary btn-sm w-full justify-center mt-3 text-xs">
Rescan now
</button> </div> </div> </div> <!-- Tags table --> <div class="card overflow-hidden"> <div class="flex items-center justify-between px-5 py-3.5 border-b border-[#2C2C2C]"> <h3 class="font-bold text-sm text-[#F0F0F0]">Tags <span class="text-[#F0F0F0]-3 font-normal">(${selectedRepo.tagCount})</span></h3> <div class="flex gap-2"> <select class="input !py-1.5 !text-xs w-32"> <option>All platforms</option> <option>linux/amd64</option> <option>linux/arm64</option> </select> <input type="search" placeholder="Filter tags…" class="input !py-1.5 !text-xs w-36"> </div> </div> <table class="table-forge"> <thead> <tr> <th>Tag</th> <th>Digest</th> <th>Size</th> <th>Platforms</th> <th>Pushed</th> <th>Last pulled</th> <th>Vulns</th> <th></th> </tr> </thead> <tbody> ${tags.map((tag) => renderTemplate`<tr class="group"> <td> <div class="flex items-center gap-2"> <div class="w-2 h-2 rounded-full bg-forge-success flex-shrink-0"></div> <code class="text-xs font-mono text-[#FFEE00]">${tag.name}</code> </div> </td> <td> <code class="text-2xs font-mono text-[#F0F0F0]-3">${tag.digest}</code> </td> <td><span class="text-xs font-mono text-[#F0F0F0]-2">${tag.size}</span></td> <td> <div class="flex flex-wrap gap-1"> ${tag.os.split(",").map((p) => renderTemplate`<span class="badge-neutral border border-[#2C2C2C] text-2xs">${p.trim()}</span>`)} </div> </td> <td><span class="text-xs text-[#F0F0F0]-3">${tag.pushed}</span></td> <td><span class="text-xs text-[#F0F0F0]-3">${tag.pulled}</span></td> <td> ${tag.vuln.critical > 0 ? renderTemplate`<span class="badge-danger border border-[rgba(255,51,51,0.3)] text-2xs">${tag.vuln.critical} critical</span>` : tag.vuln.high > 0 ? renderTemplate`<span class="badge-warning border border-[rgba(255,119,0,0.3)] text-2xs">${tag.vuln.high} high</span>` : renderTemplate`<span class="text-[#00FF88] text-2xs">Clean</span>`} </td> <td> <div class="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity"> <button class="btn-ghost btn-sm !p-1.5 text-[#F0F0F0]-3" title="Pull command"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"></path> </svg> </button> <button class="btn-ghost btn-sm !p-1.5 text-[#FF3333]" title="Delete tag"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"></path> </svg> </button> </div> </td> </tr>`)} </tbody> </table> </div> <!-- Retention policy card --> <div class="card p-5"> <div class="flex items-center justify-between mb-4"> <div> <h3 class="font-bold text-sm text-[#F0F0F0]">Retention policy</h3> <p class="text-xs text-[#F0F0F0]-3 mt-0.5">Auto-delete old tags to manage storage</p> </div> <label class="relative inline-flex items-center cursor-pointer"> <input type="checkbox" checked class="sr-only peer"> <div class="w-10 h-5 bg-[#111111]2 border border-[#2C2C2C] rounded-full peer
                          peer-checked:border-forge-accent peer-checked:bg-forge-accent-muted
                          after:content-[''] after:absolute after:top-[2px] after:left-[2px]
                          after:w-4 after:h-4 after:bg-forge-text-3 after:rounded-full after:transition-all
                          peer-checked:after:translate-x-5 peer-checked:after:bg-forge-accent relative"></div> </label> </div> <div class="grid sm:grid-cols-3 gap-4"> ${[
    { label: "Keep last N tags", value: "10", desc: "per branch prefix" },
    { label: "Delete untagged after", value: "7 days", desc: "dangling layers" },
    { label: "Delete old branches after", value: "30 days", desc: "merged PRs" }
  ].map(({ label, value, desc }) => renderTemplate`<div class="p-3 rounded-none border border-[#2C2C2C] bg-[#111111]2/40"> <div class="text-2xs text-[#F0F0F0]-3 mb-1">${label}</div> <div class="text-sm font-mono font-bold text-[#F0F0F0]">${value}</div> <div class="text-2xs text-[#F0F0F0]-3 mt-0.5">${desc}</div> </div>`)} </div> </div> </div> </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/registry.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/registry.astro";
const $$url = "/dashboard/registry";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Registry,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

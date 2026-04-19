import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute, F as Fragment } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../chunks/DashboardLayout_CthgR4Bm.mjs';
export { renderers } from '../../renderers.mjs';

const $$Sandboxes = createComponent(($$result, $$props, $$slots) => {
  const sandboxes = [
    {
      id: "sb-a1b2",
      name: "api-service debug",
      project: "api-service",
      branch: "fix/null-pointer",
      buildId: "1845",
      status: "running",
      createdAt: "14 min ago",
      expiresIn: "46 min",
      cpu: 42,
      mem: 58,
      ports: [{ port: 3e3, label: "HTTP API", public: true }, { port: 5432, label: "Postgres", public: false }],
      image: "acme-corp/api-service:c1b2f3d",
      creator: "PN",
      url: "https://sb-a1b2.sandbox.forge-ci.dev"
    },
    {
      id: "sb-c3d4",
      name: "web-app preview",
      project: "web-app",
      branch: "feat/dashboard-v2",
      buildId: "1846",
      status: "running",
      createdAt: "3 hours ago",
      expiresIn: "21 hours",
      cpu: 18,
      mem: 31,
      ports: [{ port: 3e3, label: "Next.js", public: true }],
      image: "acme-corp/web-app:3d8e1a0",
      creator: "AL",
      url: "https://sb-c3d4.sandbox.forge-ci.dev"
    },
    {
      id: "sb-e5f6",
      name: "ml-runner test",
      project: "ml-runner",
      branch: "feat/cuda12",
      buildId: "1812",
      status: "stopped",
      createdAt: "1 day ago",
      expiresIn: "Expired",
      cpu: 0,
      mem: 0,
      ports: [],
      image: "acme-corp/ml-runner:cuda12-py311",
      creator: "OA",
      url: null
    }
  ];
  const statusStyle = {
    running: { dot: "bg-forge-success animate-pulse", badge: "badge-success border border-[rgba(0,255,136,0.3)]", label: "Running" },
    stopped: { dot: "bg-forge-text-3", badge: "badge-neutral border border-[#2C2C2C]", label: "Stopped" },
    building: { dot: "bg-forge-cyan animate-pulse", badge: "running", label: "Starting" }
  };
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Sandboxes", "activeNav": "sandboxes", "breadcrumbs": [{ label: "Sandboxes" }] }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-4 sm:p-6 lg:p-8 max-w-[1200px] mx-auto space-y-5"> <!-- Header --> <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4"> <div> <div class="flex items-center gap-2 mb-1"> <h1 class="font-bold text-2xl text-[#F0F0F0]">Sandboxes</h1> <span class="badge-accent border border-[rgba(255,238,0,0.3)] text-xs">Beta</span> </div> <p class="text-[#F0F0F0]-3 text-sm">Ephemeral dev environments — spun up from any build in seconds.</p> </div> <div class="flex gap-2"> <button id="new-sandbox-btn" class="btn-primary btn-sm"> <svg class="w-3.5 h-3.5" fill="currentColor" viewBox="0 0 20 20"> <path fill-rule="evenodd" d="M10 3a1 1 0 011 1v5h5a1 1 0 110 2h-5v5a1 1 0 11-2 0v-5H4a1 1 0 110-2h5V4a1 1 0 011-1z" clip-rule="evenodd"></path> </svg>
New sandbox
</button> </div> </div> <!-- What are sandboxes explanation --> <div class="card p-5 border-forge-accent/15 bg-forge-accent-muted/10"> <div class="flex flex-col sm:flex-row gap-5"> <div class="flex-1 space-y-3"> <h2 class="font-bold text-base text-[#F0F0F0]">Instant ephemeral environments</h2> <p class="text-sm text-[#F0F0F0]-2 leading-relaxed">
Spin up a fully isolated environment from any build — with your exact container image, env vars, and database snapshot — in under 30 seconds. Port-forward to your browser or connect via VS Code. Sandboxes auto-expire after 24 hours.
</p> <div class="flex flex-wrap gap-4 text-xs text-[#F0F0F0]-3"> ${["Isolated network", "Port forwarding", "VS Code connect", "Live terminal", "Auto-expire"].map((f) => renderTemplate`<div class="flex items-center gap-1.5"> <svg class="w-3.5 h-3.5 text-[#00FF88]" fill="currentColor" viewBox="0 0 20 20"> <path fill-rule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clip-rule="evenodd"></path> </svg> ${f} </div>`)} </div> </div> <div class="flex flex-col gap-2 flex-shrink-0 sm:text-right"> <div> <div class="font-mono font-bold text-2xl text-[#FFEE00]">2</div> <div class="text-xs text-[#F0F0F0]-3">Active sandboxes</div> </div> <div> <div class="font-mono font-bold text-2xl text-[#F0F0F0]">5</div> <div class="text-xs text-[#F0F0F0]-3">Max concurrent (Team)</div> </div> </div> </div> </div> <!-- Sandbox cards --> <div class="space-y-4"> ${sandboxes.map((sb) => {
    const s = statusStyle[sb.status];
    return renderTemplate`<div${addAttribute(`card overflow-hidden ${sb.status === "stopped" ? "opacity-60" : ""}`, "class")}> <div class="p-5"> <div class="flex flex-col lg:flex-row gap-4"> <!-- Left: info --> <div class="flex-1 min-w-0"> <div class="flex items-center gap-2.5 mb-3 flex-wrap"> <div${addAttribute(`w-2 h-2 rounded-full flex-shrink-0 ${s.dot}`, "class")}></div> <h2 class="font-bold text-base text-[#F0F0F0]">${sb.name}</h2> <span${addAttribute(`badge text-2xs ${s.badge}`, "class")}>${s.label}</span> <span class="text-2xs text-[#F0F0F0]-3 font-mono">${sb.id}</span> </div> <!-- Meta row --> <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-[#F0F0F0]-3 mb-4"> <span class="flex items-center gap-1"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"></path></svg> ${sb.project} </span> <span class="font-mono">${sb.branch}</span> <a${addAttribute(`/dashboard/builds/${sb.buildId}`, "href")} class="text-[#FFEE00] hover:text-[#FFEE00]-dim font-mono">#${sb.buildId}</a> <span>Created ${sb.createdAt}</span> <span${addAttribute(sb.expiresIn === "Expired" ? "text-[#FF3333]" : "text-[#FF7700]", "class")}>
Expires: ${sb.expiresIn} </span> <div class="avatar-sm bg-[#111111]3 border border-[#2C2C2C] text-[#FFEE00] font-display text-2xs">${sb.creator}</div> </div> <!-- Image --> <div class="flex items-center gap-2 mb-4"> <code class="text-2xs font-mono text-[#F0F0F0]-3 bg-[#111111]2 border border-[#2C2C2C] rounded px-2 py-1">
🐳 ${sb.image} </code> </div> <!-- Ports --> ${sb.ports.length > 0 && renderTemplate`<div class="flex flex-wrap gap-2"> ${sb.ports.map((p) => renderTemplate`<div class="flex items-center gap-2 px-3 py-1.5 rounded-none border border-[#2C2C2C] bg-[#111111]2/50 text-xs"> <div${addAttribute(`w-1.5 h-1.5 rounded-full ${p.public ? "bg-forge-success" : "bg-forge-text-3"}`, "class")}></div> <span class="font-mono text-[#FFEE00]">:${p.port}</span> <span class="text-[#F0F0F0]-2">${p.label}</span> <span class="text-2xs text-[#F0F0F0]-3">${p.public ? "public" : "internal"}</span> ${p.public && sb.url && renderTemplate`<a${addAttribute(sb.url, "href")} target="_blank" rel="noopener noreferrer" class="text-forge-cyan hover:underline text-2xs font-mono" onclick="event.stopPropagation()">
Open ↗
</a>`} </div>`)} </div>`} </div> <!-- Right: resource meters + actions --> <div class="flex flex-col gap-3 lg:w-56 flex-shrink-0"> ${sb.status === "running" && renderTemplate`<div class="p-4 rounded-none bg-[#111111]2/40 border border-[#2C2C2C] space-y-3"> ${[
      { label: "CPU", value: sb.cpu, color: sb.cpu > 80 ? "bg-forge-danger" : "bg-forge-accent" },
      { label: "Memory", value: sb.mem, color: sb.mem > 80 ? "bg-forge-danger" : "bg-forge-cyan" }
    ].map(({ label, value, color }) => renderTemplate`<div> <div class="flex justify-between text-2xs mb-1"> <span class="text-[#F0F0F0]-3">${label}</span> <span class="font-mono text-[#F0F0F0]-2">${value}%</span> </div> <div class="h-1.5 bg-[#111111]3 rounded-full overflow-hidden"> <div${addAttribute(`h-full rounded-full ${color} transition-all`, "class")}${addAttribute(`width:${value}%`, "style")}></div> </div> </div>`)} </div>`} <!-- Action buttons --> <div class="flex flex-col gap-2"> ${sb.status === "running" ? renderTemplate`${renderComponent($$result2, "Fragment", Fragment, {}, { "default": ($$result3) => renderTemplate` <button${addAttribute(`openTerminal('${sb.id}')`, "onclick")} class="btn-primary btn-sm justify-center"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"></path> </svg>
Open terminal
</button> <button class="btn-secondary btn-sm justify-center text-xs"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4"></path> </svg>
Open in VS Code
</button> <button class="btn-secondary btn-sm justify-center text-xs">
Extend by 1h
</button> <button class="btn-danger btn-sm justify-center text-xs">
Terminate
</button> ` })}` : renderTemplate`<button class="btn-secondary btn-sm justify-center">
Start again
</button>`} </div> </div> </div> </div> <!-- Terminal panel (hidden by default) --> <div class="terminal-panel hidden border-t border-[#2C2C2C]"${addAttribute(`terminal-${sb.id}`, "id")}> <div class="flex items-center justify-between px-4 py-2.5 bg-[#111111]2/60 border-b border-[#2C2C2C]"> <div class="flex items-center gap-2"> <div class="terminal-dot bg-[#FF5F57]"></div> <div class="terminal-dot bg-[#FFBD2E]"></div> <div class="terminal-dot bg-[#28C840]"></div> <span class="text-xs font-mono text-[#F0F0F0]-3 ml-1">Terminal — ${sb.id}</span> </div> <button class="text-[#F0F0F0]-3 hover:text-[#F0F0F0] transition-colors text-xs"${addAttribute(`closeTerminal('${sb.id}')`, "onclick")}>✕</button> </div> <div class="bg-[#030508] p-4 font-mono text-xs h-48 overflow-y-auto text-[#F0F0F0]-2 space-y-0.5"> <div><span class="text-[#F0F0F0]-3">root@${sb.id}:~#</span> <span class="text-[#F0F0F0]">cat /etc/hostname</span></div> <div>${sb.id}</div> <div class="mt-1"><span class="text-[#F0F0F0]-3">root@${sb.id}:~#</span> <span class="text-[#F0F0F0]">ps aux | grep node</span></div> <div>node     1  0.4  2.1  /usr/local/bin/node server.js</div> <div class="mt-1"><span class="text-[#F0F0F0]-3">root@${sb.id}:~#</span> <span class="cursor-blink text-[#F0F0F0]"></span></div> </div> </div> </div>`;
  })} </div> <!-- Empty state if no sandboxes --> <div class="hidden text-center py-16"> <div class="text-4xl mb-4">📦</div> <h3 class="font-bold text-xl text-[#F0F0F0] mb-2">No sandboxes yet</h3> <p class="text-[#F0F0F0]-2 text-sm mb-6">Create a sandbox from any build to get a live environment in seconds.</p> <button class="btn-primary btn-md">Create your first sandbox</button> </div> </div>  <div id="sandbox-modal" class="fixed inset-0 z-50 hidden" role="dialog"> <div class="absolute inset-0 bg-black/70 backdrop-blur-sm" id="sandbox-backdrop"></div> <div class="relative flex items-center justify-center min-h-screen p-4"> <div class="w-full max-w-md bg-[#111111] border border-[#2C2C2C] rounded-none shadow-modal animate-scale-in overflow-hidden"> <div class="flex items-center justify-between px-6 py-5 border-b border-[#2C2C2C]"> <h2 class="font-bold text-lg text-[#F0F0F0]">New sandbox</h2> <button id="sandbox-close" class="text-[#F0F0F0]-3 hover:text-[#F0F0F0]"> <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path></svg> </button> </div> <div class="px-6 py-5 space-y-4"> <div> <label class="input-label">Source build</label> <select class="input"> <option>#1847 — api-service · main</option> <option>#1846 — web-app · feat/dashboard-v2</option> <option>#1845 — api-service · fix/null-pointer</option> <option>#1844 — infra · main</option> </select> </div> <div> <label class="input-label">Name (optional)</label> <input type="text" class="input" placeholder="e.g. Debug session for PR #291"> </div> <div> <label class="input-label">Expiry</label> <select class="input"> <option>1 hour</option> <option selected>4 hours</option> <option>8 hours</option> <option>24 hours</option> </select> </div> <div> <label class="input-label">Runner size</label> <select class="input"> <option>small — 2 vCPU · 4 GB</option> <option selected>medium — 4 vCPU · 8 GB</option> <option>large — 8 vCPU · 16 GB</option> </select> </div> <div class="space-y-2"> <label class="input-label">Options</label> ${[
    { label: "Public URL (HTTPS)", checked: true },
    { label: "Mount build artifacts", checked: false },
    { label: "Seed from database snapshot", checked: false }
  ].map(({ label, checked }) => renderTemplate`<label class="flex items-center gap-3 cursor-pointer py-1"> <input type="checkbox"${addAttribute(checked, "checked")} class="rounded border-[#2C2C2C]"> <span class="text-sm text-[#F0F0F0]-2">${label}</span> </label>`)} </div> </div> <div class="flex gap-3 px-6 py-4 border-t border-[#2C2C2C] bg-[#111111]2/30"> <button id="sandbox-cancel" class="btn-secondary btn-md flex-1 justify-center">Cancel</button> <button class="btn-primary btn-md flex-1 justify-center"> <svg class="w-4 h-4" fill="currentColor" viewBox="0 0 20 20"> <path fill-rule="evenodd" d="M10 3a1 1 0 011 1v5h5a1 1 0 110 2h-5v5a1 1 0 11-2 0v-5H4a1 1 0 110-2h5V4a1 1 0 011-1z" clip-rule="evenodd"></path> </svg>
Create sandbox
</button> </div> </div> </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/sandboxes.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/sandboxes.astro";
const $$url = "/dashboard/sandboxes";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Sandboxes,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

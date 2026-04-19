import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../../chunks/DashboardLayout_CthgR4Bm.mjs';
import { i as MOCK_INTEGRATIONS } from '../../../chunks/mock_JVAQtub_.mjs';
export { renderers } from '../../../renderers.mjs';

const $$Integrations = createComponent(($$result, $$props, $$slots) => {
  const connected = MOCK_INTEGRATIONS.filter((i) => i.installed);
  const available = MOCK_INTEGRATIONS.filter((i) => !i.installed);
  const extraAvail = [
    { id: "gitlab", name: "GitLab", category: "Version Control", icon: "\u{1F98A}", description: "GitLab SaaS and self-hosted.", official: true, installed: false },
    { id: "teams", name: "Microsoft Teams", category: "Notifications", icon: "\u{1F4BC}", description: "Build cards in Teams channels.", official: true, installed: false },
    { id: "vault", name: "HashiCorp Vault", category: "Security", icon: "\u{1F511}", description: "Dynamic secrets injection.", official: true, installed: false },
    { id: "snyk", name: "Snyk Security", category: "Security", icon: "\u{1F510}", description: "Vulnerability scanning.", official: true, installed: false }
  ];
  const allAvail = [...available, ...extraAvail];
  const cats = ["All", ...new Set(allAvail.map((i) => i.category))];
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Integrations", "activeNav": "integrations", "breadcrumbs": [{ label: "Settings", href: "/dashboard/settings" }, { label: "Integrations" }] }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-4 sm:p-6 lg:p-8 max-w-[1000px] mx-auto space-y-6"> <div><h1 class="font-bold text-2xl text-[#F0F0F0]">Integrations</h1><p class="text-[#666666] text-sm mt-0.5">${connected.length} connected</p></div> <!-- Connected --> <div class="space-y-3"> <h2 class="font-bold text-base text-[#F0F0F0]">Connected (${connected.length})</h2> ${connected.map((i) => renderTemplate`<div class="card p-5"> <div class="flex items-start gap-4"> <div class="w-10 h-10 rounded-none bg-[#1A1A1A] border border-[#2C2C2C] flex items-center justify-center text-xl flex-shrink-0">${i.icon}</div> <div class="flex-1 min-w-0"> <div class="flex items-center gap-2 flex-wrap mb-1"><span class="text-sm font-medium text-[#F0F0F0]">${i.name}</span><span class="badge-success border border-[rgba(0,255,136,0.2)] text-xs">Connected</span><span class="badge-neutral border border-[#2C2C2C] text-xs">${i.category}</span></div> ${i.connected_as && renderTemplate`<div class="text-xs text-[#666666] mb-2 font-mono">${i.connected_as} · ${i.connected_at}</div>`} <p class="text-xs text-[#AAAAAA]">${i.description}</p> </div> <div class="flex flex-col gap-2 flex-shrink-0"> <button class="btn-secondary btn-sm text-xs">Configure</button> <button class="btn-ghost btn-sm text-xs text-[#FF3333]">Disconnect</button> </div> </div> </div>`)} </div> <!-- Available --> <div class="space-y-4"> <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3"> <h2 class="font-bold text-base text-[#F0F0F0]">Available integrations</h2> <div class="flex gap-2"> <input type="search" id="intg-search" placeholder="Search…" class="input !py-1.5 !text-xs w-40"> <select id="intg-cat" class="input !py-1.5 !text-xs w-36"><option value="">All categories</option>${cats.slice(1).map((c) => renderTemplate`<option${addAttribute(c, "value")}>${c}</option>`)}</select> </div> </div> <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-3" id="intg-grid"> ${allAvail.map((i) => renderTemplate`<div class="card p-4 flex items-start gap-3 hover:border-[#3C3C3C] transition-colors avail-intg"${addAttribute(i.name.toLowerCase(), "data-name")}${addAttribute(i.category, "data-cat")}> <div class="w-9 h-9 rounded-none bg-[#1A1A1A] border border-[#2C2C2C] flex items-center justify-center text-base flex-shrink-0">${i.icon}</div> <div class="flex-1 min-w-0"> <div class="flex items-center gap-1.5 mb-0.5 flex-wrap"> <span class="text-xs font-medium text-[#F0F0F0]">${i.name}</span> ${i.official && renderTemplate`<span class="badge-success border border-[rgba(0,255,136,0.15)] text-xs">Official</span>`} </div> <p class="text-xs text-[#666666] leading-relaxed mb-2">${i.description}</p> <button class="text-xs border border-[rgba(255,238,0,0.3)] bg-[rgba(255,238,0,0.08)] text-[#FFEE00] rounded-md px-2.5 py-1 hover:bg-[#FFEE00] hover:text-[#0A0A0A] transition-all" onclick="installIntg(this)">Install</button> </div> </div>`)} </div> <div id="intg-empty" class="hidden text-center py-8 text-[#666666]">No integrations match.</div> </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/settings/integrations.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/settings/integrations.astro";
const $$url = "/dashboard/settings/integrations";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Integrations,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

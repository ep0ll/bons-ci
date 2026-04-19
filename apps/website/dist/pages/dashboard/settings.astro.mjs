import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../chunks/DashboardLayout_CthgR4Bm.mjs';
export { renderers } from '../../renderers.mjs';

const $$Index = createComponent(($$result, $$props, $$slots) => {
  const invoices = [
    { id: "INV-2025-04", date: "Apr 1, 2025", amount: 1580, status: "paid", period: "Mar 2025" },
    { id: "INV-2025-03", date: "Mar 1, 2025", amount: 1580, status: "paid", period: "Feb 2025" },
    { id: "INV-2025-02", date: "Feb 1, 2025", amount: 1740, status: "paid", period: "Jan 2025" },
    { id: "INV-2025-01", date: "Jan 1, 2025", amount: 1580, status: "paid", period: "Dec 2024" },
    { id: "INV-2024-12", date: "Dec 1, 2024", amount: 1420, status: "paid", period: "Nov 2024" }
  ];
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Settings", "activeNav": "settings", "breadcrumbs": [{ label: "Settings" }] }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-4 sm:p-6 lg:p-8 max-w-[900px] mx-auto space-y-6"> <div> <h1 class="font-bold text-2xl text-[#F0F0F0]">Organization settings</h1> <p class="text-[#F0F0F0]-3 text-sm mt-0.5">Manage acme-corp settings, billing, and plan.</p> </div> <!-- Settings nav tabs --> <div class="flex gap-1 border-b border-[#2C2C2C] overflow-x-auto no-scrollbar -mb-2"> ${["General", "Billing", "Plan", "Danger zone"].map((tab, i) => renderTemplate`<button${addAttribute(`settings-tab px-4 py-2.5 text-sm font-medium whitespace-nowrap border-b-2 transition-all -mb-px ${i === 0 ? "border-forge-accent text-[#FFEE00]" : "border-transparent text-[#F0F0F0]-3 hover:text-[#F0F0F0]"}`, "class")}${addAttribute(tab.toLowerCase().replace(" ", "-"), "data-tab")} onclick="switchSettingsTab(this)"> ${tab} </button>`)} </div> <!-- ── General tab ─────────────────────────── --> <div id="tab-general" class="space-y-5"> <div class="card p-6 space-y-5"> <h2 class="font-bold text-base text-[#F0F0F0] border-b border-[#2C2C2C] pb-4">Organization profile</h2> <!-- Org avatar --> <div class="flex items-center gap-5"> <div class="w-16 h-16 rounded-none bg-forge-accent flex items-center justify-center text-2xl font-bold text-forge-bg font-display flex-shrink-0">
A
</div> <div> <div class="text-sm font-medium text-[#F0F0F0] mb-1">Organization avatar</div> <div class="text-xs text-[#F0F0F0]-3 mb-3">Square PNG or JPG, 256×256px minimum</div> <button class="btn-secondary btn-sm text-xs">Upload image</button> </div> </div> <!-- Fields --> <div class="grid sm:grid-cols-2 gap-4"> <div> <label class="input-label" for="org-name">Organization name</label> <input type="text" id="org-name" class="input" value="Acme Corp"> </div> <div> <label class="input-label" for="org-slug">URL slug</label> <div class="flex"> <span class="px-3 py-2.5 bg-[#111111]2 border border-[#2C2C2C] border-r-0 rounded-l-lg text-[#F0F0F0]-3 text-sm font-mono flex-shrink-0">forge-ci.dev/</span> <input type="text" id="org-slug" class="input rounded-l-none" value="acme-corp"> </div> </div> <div class="sm:col-span-2"> <label class="input-label" for="org-desc">Description</label> <textarea id="org-desc"${addAttribute(2, "rows")} class="input resize-none">B2B SaaS platform for enterprise workflow automation.</textarea> </div> <div> <label class="input-label" for="org-website">Website</label> <input type="url" id="org-website" class="input" value="https://acme-corp.io"> </div> <div> <label class="input-label" for="org-email">Billing email</label> <input type="email" id="org-email" class="input" value="billing@acme-corp.io"> </div> </div> <div class="flex justify-end pt-2"> <button class="btn-primary btn-md">Save changes</button> </div> </div> <!-- Default build settings --> <div class="card p-6 space-y-4"> <h2 class="font-bold text-base text-[#F0F0F0] border-b border-[#2C2C2C] pb-4">Default build settings</h2> <div class="grid sm:grid-cols-2 gap-4"> <div> <label class="input-label">Default runner size</label> <select class="input"> <option>linux-x64-small (2 vCPU · 4 GB)</option> <option selected>linux-x64-medium (4 vCPU · 8 GB)</option> <option>linux-x64-large (8 vCPU · 16 GB)</option> <option>linux-x64-xlarge (16 vCPU · 32 GB)</option> </select> </div> <div> <label class="input-label">Default build timeout</label> <select class="input"> <option>30 minutes</option> <option selected>60 minutes</option> <option>120 minutes</option> <option>No timeout</option> </select> </div> <div> <label class="input-label">Default parallelism</label> <input type="number" class="input" value="10" min="1" max="50"> </div> <div> <label class="input-label">Build log retention</label> <select class="input"> <option>30 days</option> <option selected>90 days</option> <option>365 days</option> <option>Unlimited</option> </select> </div> </div> <!-- Toggles --> <div class="space-y-3 pt-2"> ${[
    { label: "Auto-cancel redundant builds", desc: "Cancel older builds on the same branch when a newer commit is pushed", checked: true },
    { label: "Build on pull request events", desc: "Trigger builds automatically when a PR is opened, updated, or merged", checked: true },
    { label: "Require build to pass before merge", desc: "Block PR merging until all required pipeline steps pass", checked: false },
    { label: "Enable build notifications by default", desc: "Send email and Slack notifications for all new projects", checked: true }
  ].map(({ label, desc, checked }) => renderTemplate`<div class="flex items-start justify-between gap-4 py-3 border-t border-[#2C2C2C]/50"> <div> <div class="text-sm font-medium text-[#F0F0F0]">${label}</div> <div class="text-xs text-[#F0F0F0]-3 mt-0.5">${desc}</div> </div> <label class="relative inline-flex items-center cursor-pointer flex-shrink-0 mt-0.5"> <input type="checkbox"${addAttribute(checked, "checked")} class="sr-only peer"> <div class="w-10 h-5 bg-[#111111]2 border border-[#2C2C2C] rounded-full peer
                            peer-checked:border-forge-accent peer-checked:bg-forge-accent-muted
                            after:content-[''] after:absolute after:top-[2px] after:left-[2px]
                            after:w-4 after:h-4 after:bg-forge-text-3 after:rounded-full after:transition-all
                            peer-checked:after:translate-x-5 peer-checked:after:bg-forge-accent relative"></div> </label> </div>`)} </div> <div class="flex justify-end"> <button class="btn-primary btn-md">Save defaults</button> </div> </div> </div> <!-- ── Billing tab ─────────────────────────── --> <div id="tab-billing" class="space-y-5 hidden"> <!-- Current plan summary --> <div class="card p-6" style="background: linear-gradient(135deg, rgba(255,238,0,0.04), rgba(0,238,255,0.04)); border-color: rgba(255,238,0,0.2);"> <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4"> <div> <div class="flex items-center gap-2 mb-2"> <span class="badge-accent text-sm px-3 py-1 font-bold">Team Plan</span> <span class="text-2xs text-[#F0F0F0]-3">Billed monthly</span> </div> <div class="text-3xl font-bold text-[#F0F0F0] mb-1">
$1,580<span class="text-base font-normal text-[#F0F0F0]-3"> / month</span> </div> <div class="text-sm text-[#F0F0F0]-3">20 seats × $79/user · Next invoice May 1, 2025</div> </div> <div class="flex flex-col gap-2"> <button class="btn-primary btn-md">Upgrade to Enterprise</button> <button class="btn-ghost btn-sm text-[#F0F0F0]-3">Switch to annual (save 20%)</button> </div> </div> </div> <!-- Payment method --> <div class="card p-6"> <div class="flex items-center justify-between mb-5"> <h2 class="font-bold text-base text-[#F0F0F0]">Payment method</h2> <button class="btn-secondary btn-sm text-xs">Update card</button> </div> <div class="flex items-center gap-4 p-4 rounded-none border border-[#2C2C2C] bg-[#111111]2/40"> <div class="w-12 h-8 bg-[#111111]3 border border-[#2C2C2C] rounded-md flex items-center justify-center"> <span class="text-xs font-bold text-[#F0F0F0]-2">VISA</span> </div> <div> <div class="text-sm font-medium text-[#F0F0F0]">Visa ending in 4242</div> <div class="text-xs text-[#F0F0F0]-3">Expires 12/2027 · billing@acme-corp.io</div> </div> <div class="ml-auto badge-success border border-[rgba(0,255,136,0.3)] text-2xs">Active</div> </div> </div> <!-- Usage breakdown --> <div class="card p-6"> <h2 class="font-bold text-base text-[#F0F0F0] mb-5">Usage this billing period</h2> <div class="space-y-3 mb-4"> ${[
    { label: "20 \xD7 Team seats", amount: 1580 },
    { label: "Build minute overages", amount: 0 },
    { label: "Extra cache storage (0 GB)", amount: 0 },
    { label: "Additional egress (0 GB)", amount: 0 }
  ].map(({ label, amount }) => renderTemplate`<div class="flex justify-between items-center py-2 border-b border-[#2C2C2C]/50"> <span class="text-sm text-[#F0F0F0]-2">${label}</span> <span class="text-sm font-mono font-medium text-[#F0F0F0]">$${amount.toLocaleString()}</span> </div>`)} </div> <div class="flex justify-between items-center py-3 border-t-2 border-[#2C2C2C]"> <span class="font-bold text-[#F0F0F0]">Total due May 1</span> <span class="font-bold text-xl text-[#F0F0F0]">$1,580</span> </div> </div> <!-- Invoices --> <div class="card overflow-hidden"> <div class="px-5 py-4 border-b border-[#2C2C2C] flex items-center justify-between"> <h2 class="font-bold text-base text-[#F0F0F0]">Invoice history</h2> <button class="btn-ghost btn-sm text-xs text-[#F0F0F0]-3">Download all</button> </div> <table class="table-forge"> <thead> <tr> <th>Invoice</th> <th>Period</th> <th>Date</th> <th>Amount</th> <th>Status</th> <th></th> </tr> </thead> <tbody> ${invoices.map((inv) => renderTemplate`<tr> <td><code class="text-xs font-mono text-[#F0F0F0]-2">${inv.id}</code></td> <td><span class="text-xs text-[#F0F0F0]-2">${inv.period}</span></td> <td><span class="text-xs text-[#F0F0F0]-3">${inv.date}</span></td> <td><span class="text-xs font-mono text-[#F0F0F0]">$${inv.amount.toLocaleString()}</span></td> <td> <span${addAttribute(`badge border text-2xs ${inv.status === "paid" ? "badge-success border-[rgba(0,255,136,0.3)]" : "badge-warning border-[rgba(255,119,0,0.3)]"}`, "class")}> ${inv.status} </span> </td> <td> <button class="btn-ghost btn-sm !p-1.5 text-[#F0F0F0]-3 hover:text-[#F0F0F0]"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"></path> </svg> </button> </td> </tr>`)} </tbody> </table> </div> </div> <!-- ── Plan tab ────────────────────────────── --> <div id="tab-plan" class="space-y-5 hidden"> <div class="grid sm:grid-cols-3 gap-4"> ${[
    {
      name: "Pro",
      price: 29,
      per: "user/mo",
      cta: "Downgrade",
      ctaCls: "btn-secondary",
      current: false,
      features: ["20,000 build mins", "10 concurrent", "macOS runners", "10 GB cache"]
    },
    {
      name: "Team",
      price: 79,
      per: "user/mo",
      cta: "Current plan",
      ctaCls: "btn-secondary",
      current: true,
      features: ["100,000 build mins", "50 concurrent", "BYOC runners", "Sherlock AI", "100 GB cache", "SSO", "Audit logs 90d"]
    },
    {
      name: "Enterprise",
      price: null,
      per: "custom",
      cta: "Talk to sales",
      ctaCls: "btn-primary",
      current: false,
      features: ["Unlimited mins", "Unlimited concurrent", "Dedicated runners", "SAML + SCIM", "Audit logs unlimited", "99.99% SLA", "Dedicated CSM"]
    }
  ].map((plan) => renderTemplate`<div${addAttribute(`card p-6 flex flex-col ${plan.current ? "border-forge-accent/40" : ""}`, "class")}> ${plan.current && renderTemplate`<div class="badge-accent border border-[rgba(255,238,0,0.3)] text-2xs self-start mb-3">Current plan</div>`} <h3 class="font-bold text-xl text-[#F0F0F0] mb-1">${plan.name}</h3> <div class="text-3xl font-mono font-bold text-[#F0F0F0] mb-0.5"> ${plan.price ? `$${plan.price}` : "Custom"} </div> <div class="text-xs text-[#F0F0F0]-3 mb-5">per ${plan.per}</div> <ul class="space-y-2 flex-1 mb-5"> ${plan.features.map((f) => renderTemplate`<li class="flex items-center gap-2 text-xs text-[#F0F0F0]-2"> <svg class="w-3.5 h-3.5 text-[#00FF88] flex-shrink-0" fill="currentColor" viewBox="0 0 20 20"> <path fill-rule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clip-rule="evenodd"></path> </svg> ${f} </li>`)} </ul> <button${addAttribute(`${plan.ctaCls} btn-md w-full justify-center`, "class")}${addAttribute(plan.current, "disabled")}> ${plan.cta} </button> </div>`)} </div> </div> <!-- ── Danger zone ─────────────────────────── --> <div id="tab-danger-zone" class="hidden space-y-4"> ${[
    { title: "Transfer organization", desc: "Transfer ownership of this organization to another account. You will lose owner access.", btn: "Transfer", btnCls: "btn-secondary" },
    { title: "Delete all build history", desc: "Permanently delete all build logs, artifacts, and metrics. This cannot be undone.", btn: "Delete history", btnCls: "btn-danger" },
    { title: "Delete organization", desc: "Permanently delete the acme-corp organization and all its data. This cannot be undone.", btn: "Delete organization", btnCls: "btn-danger" }
  ].map(({ title, desc, btn, btnCls }) => renderTemplate`<div class="card p-5 border-[rgba(255,51,51,0.3)] flex flex-col sm:flex-row sm:items-center justify-between gap-4"> <div> <h3 class="font-medium text-[#F0F0F0] mb-1">${title}</h3> <p class="text-xs text-[#F0F0F0]-3 leading-relaxed">${desc}</p> </div> <button${addAttribute(`${btnCls} btn-md flex-shrink-0`, "class")}>${btn}</button> </div>`)} </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/settings/index.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/settings/index.astro";
const $$url = "/dashboard/settings";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Index,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

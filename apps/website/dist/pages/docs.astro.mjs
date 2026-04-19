import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../chunks/MarketingLayout_tAfEjAWH.mjs';
import { D as DOCS_CATEGORIES, g as getDocsByCategory, a as DOCS } from '../chunks/docs_TuGd9NEz.mjs';
export { renderers } from '../renderers.mjs';

const $$Docs = createComponent(($$result, $$props, $$slots) => {
  const byCategory = DOCS_CATEGORIES.map((cat) => ({
    cat,
    pages: getDocsByCategory(cat)
  }));
  const catIcons = {
    "Getting Started": "\u{1F680}",
    "Pipelines": "\u26A1",
    "Caching": "\u25C8",
    "Secrets": "\u25C6",
    "Observability": "\u25CE",
    "Runners": "\u25A3",
    "API Reference": "\u25A4"
  };
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": "Documentation \u2014 Forge CI", "description": "Everything you need to set up, configure, and run Forge CI \u2014 quick start, pipeline YAML, caching, secrets, OTel, and API reference." }, { "default": ($$result2) => renderTemplate`  ${maybeRenderHead()}<section class="relative pt-32 pb-12 overflow-hidden"> <div class="absolute inset-0 bg-grid-brut opacity-20 pointer-events-none"></div> <div class="container-forge relative z-10 max-w-4xl"> <div class="section-label mb-5">Documentation</div> <h1 class="section-title mb-4">Build with confidence.</h1> <p class="section-subtitle mb-8">Everything from quick start to API reference, OTel traces, and secrets.</p> <!-- Search --> <div class="max-w-xl"> <div class="input-group"> <div class="input-addon"> <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="square" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path> </svg> </div> <input type="text" class="input" placeholder="Search docs…" id="doc-search"> </div> <p class="text-xs font-mono mt-2" style="color:#3C3C3C">Or press ⌘K from anywhere</p> </div> </div> </section>  <div class="container-forge max-w-4xl"> <a href="/docs/getting-started" class="card-accent flex items-center gap-5 p-6 mb-10 group"> <span class="text-4xl">🚀</span> <div class="flex-1"> <div class="text-base font-bold mb-1 group-hover:text-[#FFEE00] transition-colors" style="font-family:'Space Grotesk';color:#F0F0F0">Quick Start</div> <div class="text-sm" style="color:#AAAAAA">Connect your repo and run your first build in under 2 minutes.</div> </div> <svg class="w-5 h-5 flex-shrink-0 group-hover:translate-x-1 transition-transform" fill="none" viewBox="0 0 24 24" stroke="currentColor" style="color:#FFEE00"> <path stroke-linecap="square" stroke-width="2" d="M13 7l5 5m0 0l-5 5m5-5H6"></path> </svg> </a> <!-- Category grid --> <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-0 border-l-2 border-t-2 mb-16" style="border-color:#2C2C2C"> ${byCategory.map(({ cat, pages }) => renderTemplate`<div class="border-r-2 border-b-2 p-6" style="border-color:#2C2C2C"> <div class="flex items-center gap-3 mb-4"> <span class="text-2xl">${catIcons[cat] ?? "\u25CC"}</span> <div class="text-sm font-bold" style="font-family:'Space Grotesk';color:#F0F0F0">${cat}</div> </div> <ul class="space-y-2"> ${pages.map((p) => renderTemplate`<li> <a${addAttribute(`/docs/${p.slug}`, "href")} class="flex items-center justify-between text-xs font-mono group" style="color:#666666"> <span class="group-hover:text-[#FFEE00] transition-colors truncate">${p.title}</span> <svg class="w-3 h-3 flex-shrink-0 ml-2 opacity-0 group-hover:opacity-100 transition-opacity" fill="none" viewBox="0 0 24 24" stroke="currentColor" style="color:#FFEE00"> <path stroke-linecap="square" stroke-width="2.5" d="M9 5l7 7-7 7"></path> </svg> </a> </li>`)} </ul> </div>`)} </div> <!-- Popular articles --> <div class="mb-20"> <div class="section-label mb-6">Popular articles</div> <div class="space-y-0 border-t-2 border-l-2" style="border-color:#2C2C2C"> ${DOCS.slice(0, 6).map((doc) => renderTemplate`<a${addAttribute(`/docs/${doc.slug}`, "href")} class="flex items-center gap-5 p-4 border-r-2 border-b-2 group" style="border-color:#2C2C2C"> <span class="text-lg flex-shrink-0" style="color:#FFEE00">${catIcons[doc.category] ?? "\u25CC"}</span> <div class="flex-1 min-w-0"> <div class="text-sm font-semibold group-hover:text-[#FFEE00] transition-colors" style="color:#F0F0F0">${doc.title}</div> <div class="text-xs font-mono mt-0.5 truncate" style="color:#666666">${doc.excerpt}</div> </div> <span class="badge-neutral flex-shrink-0">${doc.category}</span> </a>`)} </div> </div> <!-- Help section --> <div class="grid sm:grid-cols-3 gap-0 border-l-2 border-t-2 mb-16" style="border-color:#2C2C2C"> ${[
    { icon: "\u{1F4AC}", t: "Community forum", d: "Ask questions, share pipelines, get help from 50K+ builders.", href: "#" },
    { icon: "\u{1F3AE}", t: "Interactive demo", d: "Try Forge CI in-browser \u2014 no signup required.", href: "#" },
    { icon: "\u{1F4DE}", t: "Talk to sales", d: "Need help planning a migration or enterprise contract?", href: "/contact/sales" }
  ].map((item) => renderTemplate`<a${addAttribute(item.href, "href")} class="border-r-2 border-b-2 p-6 group" style="border-color:#2C2C2C"> <div class="text-3xl mb-3">${item.icon}</div> <div class="text-sm font-bold mb-1 group-hover:text-[#FFEE00] transition-colors" style="font-family:'Space Grotesk';color:#F0F0F0">${item.t}</div> <div class="text-xs leading-relaxed" style="color:#666666">${item.d}</div> </a>`)} </div> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/docs.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/docs.astro";
const $$url = "/docs";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Docs,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

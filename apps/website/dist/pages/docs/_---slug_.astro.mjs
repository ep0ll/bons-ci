import { c as createAstro, a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute, F as Fragment } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../../chunks/MarketingLayout_tAfEjAWH.mjs';
import { g as getDocsByCategory, a as DOCS, D as DOCS_CATEGORIES } from '../../chunks/docs_TuGd9NEz.mjs';
export { renderers } from '../../renderers.mjs';

const $$Astro = createAstro("https://forge-ci.dev");
function getStaticPaths() {
  return DOCS.map((doc) => ({ params: { slug: doc.slug }, props: { doc } }));
}
const $$ = createComponent(($$result, $$props, $$slots) => {
  const Astro2 = $$result.createAstro($$Astro, $$props, $$slots);
  Astro2.self = $$;
  const { doc } = Astro2.props;
  const catPages = getDocsByCategory(doc.category);
  const curIdx = catPages.findIndex((p) => p.slug === doc.slug);
  const prev = curIdx > 0 ? catPages[curIdx - 1] : null;
  const next = curIdx < catPages.length - 1 ? catPages[curIdx + 1] : null;
  const catIcons = {
    "Getting Started": "\u{1F680}",
    "Pipelines": "\u26A1",
    "Caching": "\u25C8",
    "Secrets": "\u25C6",
    "Observability": "\u25CE",
    "Runners": "\u25A3",
    "API Reference": "\u25A4"
  };
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": `${doc.title} \u2014 Forge CI Docs`, "description": doc.excerpt }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="flex pt-16 min-h-screen"> <!-- Left sidebar --> <aside class="hidden lg:flex flex-col flex-shrink-0 w-60 border-r-2 pt-8 sticky top-16 h-[calc(100vh-4rem)] overflow-y-auto" style="border-color:#2C2C2C;background:#060606"> <a href="/docs" class="flex items-center gap-2 px-5 pb-5 mb-2 border-b-2 text-xs font-mono" style="border-color:#2C2C2C;color:#666666"> <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="square" stroke-width="2" d="M15 19l-7-7 7-7"></path> </svg>
All docs
</a> ${DOCS_CATEGORIES.map((cat) => {
    const pages = getDocsByCategory(cat);
    return renderTemplate`<div class="mb-1"> <div class="px-5 py-2 text-xs font-mono uppercase tracking-widest flex items-center gap-2" style="color:#3C3C3C"> <span>${catIcons[cat] ?? "\u25CC"}</span> ${cat} </div> ${pages.map((p) => {
      const isActive = p.slug === doc.slug;
      return renderTemplate`<a${addAttribute(`/docs/${p.slug}`, "href")} class="flex items-center gap-2 px-5 py-1.5 text-xs font-medium transition-all border-l-2"${addAttribute(isActive ? "border-left-color:#FFEE00;color:#FFEE00;background:rgba(255,238,0,0.06)" : "border-left-color:transparent;color:#666666", "style")}> ${p.title} </a>`;
    })} </div>`;
  })} </aside> <!-- Main content --> <div class="flex-1 min-w-0 px-6 sm:px-10 lg:px-16 py-12 max-w-3xl"> <!-- Breadcrumb --> <nav class="flex items-center gap-1.5 text-xs font-mono mb-8" style="color:#3C3C3C"> <a href="/docs" style="color:#666666" class="hover:text-[#FFEE00] transition-colors">Docs</a> <span>›</span> <a href="/docs" style="color:#666666" class="hover:text-[#FFEE00] transition-colors">${doc.category}</a> <span>›</span> <span style="color:#AAAAAA">${doc.title}</span> </nav> <!-- Category badge --> <div class="section-label mb-5">${catIcons[doc.category] ?? ""} ${doc.category}</div> <!-- Title --> <h1 class="section-title mb-4" style="font-size:clamp(1.75rem,4vw,2.5rem)">${doc.title}</h1> <p class="section-subtitle mb-10">${doc.excerpt}</p> <!-- Table of contents (inline) --> ${doc.toc.length > 0 && renderTemplate`<div class="card p-5 mb-10"> <div class="text-xs font-mono uppercase tracking-widest mb-3" style="color:#3C3C3C">On this page</div> <ul class="space-y-1.5"> ${doc.toc.map((item) => renderTemplate`<li${addAttribute(`padding-left:${(item.depth - 2) * 12}px`, "style")}> <a${addAttribute(`#${item.id}`, "href")} class="text-xs font-mono hover:text-[#FFEE00] transition-colors" style="color:#666666">${item.label}</a> </li>`)} </ul> </div>`} <!-- Content: rendered as markdown-like HTML --> <!-- In production wire to MDX; here we render the mock content as preformatted --> <div class="prose-brut space-y-0 text-sm"> ${doc.content.split("\n\n").map((block, bi) => {
    const isH2 = block.startsWith("## ");
    const isH3 = block.startsWith("### ");
    const isCode = block.startsWith("```");
    const isTable = block.trim().startsWith("|");
    const isList = block.trim().startsWith("-") || block.trim().startsWith("*");
    if (isH2) return renderTemplate`<h2${addAttribute(block.slice(3).trim().toLowerCase().replace(/[^a-z0-9]+/g, "-"), "id")} class="text-xl font-bold mt-10 mb-4 pt-8 border-t-2" style="font-family:'Space Grotesk';color:#F0F0F0;border-color:#2C2C2C"> ${block.slice(3)} </h2>`;
    if (isH3) return renderTemplate`<h3${addAttribute(block.slice(4).trim().toLowerCase().replace(/[^a-z0-9]+/g, "-"), "id")} class="text-base font-bold mt-6 mb-3" style="font-family:'Space Grotesk';color:#F0F0F0"> ${block.slice(4)} </h3>`;
    if (isCode) return renderTemplate`<div class="terminal my-5"> <div class="terminal-header"> <div class="terminal-dot" style="background:#FF5F57"></div> <div class="terminal-dot" style="background:#FFBD2E"></div> <div class="terminal-dot" style="background:#28C840"></div> </div> <pre class="p-5 text-xs font-mono overflow-x-auto leading-relaxed" style="color:#AAAAAA">                <code>${block.replace(/^```[a-z]*\n?/, "").replace(/```$/, "").trim()}</code>
              </pre> </div>`;
    if (isTable) return renderTemplate`<div class="overflow-x-auto my-5"> <table class="table-brut"> ${block.split("\n").filter((l) => !l.match(/^[\|\ \-]+$/)).map((row, ri) => {
      const cells = row.split("|").filter((c) => c.trim());
      return ri === 0 ? renderTemplate`<thead><tr>${cells.map((c) => renderTemplate`<th>${c.trim()}</th>`)}</tr></thead>` : renderTemplate`<tbody><tr>${cells.map((c) => renderTemplate`<td>${c.trim()}</td>`)}</tr></tbody>`;
    })} </table> </div>`;
    if (isList) return renderTemplate`<ul class="space-y-1.5 my-3 ml-4"> ${block.split("\n").filter((l) => l.trim()).map((l) => renderTemplate`<li class="flex items-start gap-2 text-sm" style="color:#AAAAAA"> <span style="color:#FFEE00;flex-shrink:0">→</span> ${l.replace(/^[-*]\s+/, "")} </li>`)} </ul>`;
    return renderTemplate`<p class="leading-relaxed my-3" style="color:#AAAAAA">${block}</p>`;
  })} </div> <!-- Prev / Next navigation --> <div class="grid sm:grid-cols-2 gap-4 mt-16 pt-8 border-t-2" style="border-color:#2C2C2C"> ${prev ? renderTemplate`<a${addAttribute(`/docs/${prev.slug}`, "href")} class="card-hover p-5 group"> <div class="text-xs font-mono mb-2" style="color:#3C3C3C">← Previous</div> <div class="text-sm font-semibold group-hover:text-[#FFEE00] transition-colors" style="color:#F0F0F0">${prev.title}</div> </a>` : renderTemplate`<div></div>`} ${next && renderTemplate`<a${addAttribute(`/docs/${next.slug}`, "href")} class="card-hover p-5 group text-right"> <div class="text-xs font-mono mb-2" style="color:#3C3C3C">Next →</div> <div class="text-sm font-semibold group-hover:text-[#FFEE00] transition-colors" style="color:#F0F0F0">${next.title}</div> </a>`} </div> </div> <!-- Right TOC sidebar (desktop) --> <aside class="hidden xl:flex flex-col w-52 flex-shrink-0 pt-12 px-6 sticky top-16 h-[calc(100vh-4rem)] overflow-y-auto"> ${doc.toc.length > 0 && renderTemplate`${renderComponent($$result2, "Fragment", Fragment, {}, { "default": ($$result3) => renderTemplate` <div class="text-xs font-mono uppercase tracking-widest mb-4" style="color:#3C3C3C">On this page</div> <ul class="space-y-2"> ${doc.toc.map((item) => renderTemplate`<li${addAttribute(`padding-left:${(item.depth - 2) * 10}px`, "style")}> <a${addAttribute(`#${item.id}`, "href")} class="text-xs font-mono hover:text-[#FFEE00] transition-colors block" style="color:#3C3C3C">${item.label}</a> </li>`)} </ul> ` })}`} <div class="mt-auto pt-6 border-t-2 mt-8" style="border-color:#2C2C2C"> <a href="/auth/signup" class="btn-primary btn-sm w-full justify-center mb-2">Start free</a> <a href="/docs" class="btn-secondary btn-sm w-full justify-center">All docs</a> </div> </aside> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/docs/[...slug].astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/docs/[...slug].astro";
const $$url = "/docs/[...slug]";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$,
  file: $$file,
  getStaticPaths,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

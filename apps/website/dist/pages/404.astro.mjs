import { c as createAstro, a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../chunks/MarketingLayout_tAfEjAWH.mjs';
export { renderers } from '../renderers.mjs';

const $$Astro = createAstro("https://forge-ci.dev");
const $$404 = createComponent(($$result, $$props, $$slots) => {
  const Astro2 = $$result.createAstro($$Astro, $$props, $$slots);
  Astro2.self = $$404;
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": "404 \u2014 Page not found \u2014 Forge CI", "noindex": true }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="min-h-[90svh] flex items-center justify-center px-4 relative overflow-hidden"> <div class="absolute inset-0 bg-grid opacity-20 pointer-events-none"></div> <div class="relative z-10 text-center max-w-xl"> <div class="terminal max-w-sm mx-auto mb-10"> <div class="terminal-header"><div class="terminal-dot bg-[#FF5F57]"></div><div class="terminal-dot bg-[#FFBD2E]"></div><div class="terminal-dot bg-[#28C840]"></div></div> <div class="p-5 font-mono text-sm space-y-1"> <div><span class="text-[#666666]">$</span> <span class="text-[#F0F0F0]">forge get ${Astro2.url.pathname}</span></div> <div class="text-[#FF3333]">Error: 404 Not Found</div> <div class="text-[#666666]">This page doesn't exist.</div> <div class="cursor-blink text-[#FFEE00] mt-2"></div> </div> </div> <h1 class="font-bold text-5xl text-[#F0F0F0] mb-3">404</h1> <p class="text-[#AAAAAA] mb-8">This page doesn't exist. Maybe it moved, or you followed a stale link.</p> <div class="flex flex-col sm:flex-row gap-3 justify-center mb-10"> <a href="/" class="btn-primary btn-lg">Go to homepage</a> <a href="/dashboard" class="btn-secondary btn-lg">Open dashboard</a> <a href="/docs" class="btn-ghost btn-lg text-[#666666]">Read docs</a> </div> <div class="grid grid-cols-4 gap-2 text-sm"> ${["Features", "Pricing", "Blog", "Status", "Docs", "Changelog", "Contact", "Sign in"].map((l) => renderTemplate`<a${addAttribute(`/${l.toLowerCase().replace(" ", "-")}`, "href")} class="card p-3 text-center text-[#666666] hover:text-[#FFEE00] hover:border-[rgba(255,238,0,0.3)] transition-all">${l}</a>`)} </div> </div> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/404.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/404.astro";
const $$url = "/404";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$404,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

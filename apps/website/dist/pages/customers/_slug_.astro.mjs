import { c as createAstro, a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../../chunks/MarketingLayout_tAfEjAWH.mjs';
import { a as MOCK_CASE_STUDIES } from '../../chunks/mock_JVAQtub_.mjs';
export { renderers } from '../../renderers.mjs';

const $$Astro = createAstro("https://forge-ci.dev");
function getStaticPaths() {
  return MOCK_CASE_STUDIES.map((cs) => ({ params: { slug: cs.slug }, props: { cs } }));
}
const $$slug = createComponent(($$result, $$props, $$slots) => {
  const Astro2 = $$result.createAstro($$Astro, $$props, $$slots);
  Astro2.self = $$slug;
  const { cs } = Astro2.props;
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": `${cs.company} Case Study \u2014 Forge CI`, "description": cs.headline }, { "default": ($$result2) => renderTemplate`  ${maybeRenderHead()}<section class="relative pt-32 pb-12 overflow-hidden"> <div class="absolute inset-0 bg-grid-brut opacity-20 pointer-events-none"></div> <div class="container-forge relative z-10 max-w-4xl"> <a href="/customers" class="inline-flex items-center gap-2 text-xs font-mono mb-8 hover:text-[#FFEE00] transition-colors" style="color:#666666"> <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="square" stroke-width="2" d="M15 19l-7-7 7-7"></path> </svg>
All case studies
</a> <div class="flex items-center gap-5 mb-6"> <span class="text-6xl">${cs.logo_emoji}</span> <div> <h1 class="text-3xl font-bold" style="font-family:'Space Grotesk';color:#F0F0F0">${cs.company}</h1> <div class="text-sm font-mono mt-1" style="color:#666666">${cs.industry} · ${cs.team_size}</div> </div> </div> <h2 class="section-title mb-6 max-w-3xl" style="font-size:clamp(1.5rem,3vw,2.5rem)">${cs.headline}</h2> </div> </section> <div class="container-forge max-w-4xl pb-24"> <div class="grid lg:grid-cols-[1fr_280px] gap-12"> <!-- Story body --> <article> <!-- Results --> <div class="mb-10"> <div class="section-label mb-5">The Numbers</div> <div class="space-y-2"> ${cs.results.map((r) => renderTemplate`<div class="grid grid-cols-[1fr_auto] items-center gap-4 p-4 border-2 group hover:border-[#FFEE00] transition-all" style="border-color:#2C2C2C;background:#111111"> <span class="text-sm font-medium" style="color:#AAAAAA">${r.metric}</span> <div class="flex items-center gap-4 font-mono text-sm"> <span style="color:#3C3C3C;text-decoration:line-through">${r.before}</span> <span style="color:#FFEE00">→</span> <strong class="text-lg" style="color:#FFEE00">${r.after}</strong> </div> </div>`)} </div> </div> <!-- Quote --> <div class="mb-10"> <blockquote class="p-6 border-l-4 border-[#FFEE00]" style="background:#111111"> <p class="text-lg italic leading-relaxed mb-4" style="color:#F0F0F0">"${cs.quote}"</p> <footer class="text-sm font-mono" style="color:#666666">— ${cs.quote_author}, ${cs.company}</footer> </blockquote> </div> <!-- Challenge / Solution / Outcome (expanded story) --> <div class="space-y-8"> <div> <h3 class="text-lg font-bold mb-3" style="font-family:'Space Grotesk';color:#F0F0F0">The Challenge</h3> <p class="text-sm leading-relaxed" style="color:#AAAAAA">
Like many teams at scale, ${cs.company}'s engineering organisation had accumulated significant CI/CD debt.
              Build times had grown beyond 10 minutes on average, cache hit rates hovered around 40%, and on-call engineers
              were routinely paged for CI failures at 3 AM. The cost of slow feedback loops was measurable in both engineering
              time and developer morale.
</p> </div> <div> <h3 class="text-lg font-bold mb-3" style="font-family:'Space Grotesk';color:#F0F0F0">The Solution</h3> <p class="text-sm leading-relaxed mb-4" style="color:#AAAAAA">
After evaluating several CI/CD platforms, ${cs.company}'s platform engineering team selected Forge CI for
              its content-addressed caching layer, Sherlock AI diagnostics, and native OTel trace export. The migration
              took a single weekend — Forge CI's automated migration tooling converted all pipelines in batch.
</p> <div class="terminal"> <div class="terminal-header"> <div class="terminal-dot" style="background:#FF5F57"></div> <div class="terminal-dot" style="background:#FFBD2E"></div> <div class="terminal-dot" style="background:#28C840"></div> <span class="text-xs font-mono ml-auto" style="color:#666666">forge migrate --from github-actions</span> </div> <div class="p-5 font-mono text-xs space-y-1" style="color:#AAAAAA"> <div>→ Scanning .github/workflows/ — found 47 workflow files</div> <div>→ Converted 47 workflows to .forge/pipeline.yml</div> <div>→ Detected Node.js — suggesting built-in node cache</div> <div>→ Detected Docker — adding BuildKit layer caching</div> <div style="color:#00FF88">✓ Migration complete · Review at /dashboard/projects</div> </div> </div> </div> <div> <h3 class="text-lg font-bold mb-3" style="font-family:'Space Grotesk';color:#F0F0F0">The Outcome</h3> <p class="text-sm leading-relaxed" style="color:#AAAAAA">
Within two weeks of migration, ${cs.company} saw an average build time drop of over 80%. The Sherlock AI agent
              reduced mean time to resolution for CI failures from 45 minutes to under 12 minutes. Engineers stopped being
              paged for CI issues — Sherlock now auto-creates fix PRs in 97% of cases.
</p> </div> </div> <!-- CTA --> <div class="border-t-2 mt-12 pt-8 flex gap-3" style="border-color:#2C2C2C"> <a href="/auth/signup" class="btn-primary btn-lg">Start building free →</a> <a href="/customers" class="btn-secondary btn-lg">More case studies</a> </div> </article> <!-- Sidebar --> <aside> <div class="sticky top-24 space-y-5"> <!-- Company info --> <div class="card p-5"> <div class="text-xs font-mono uppercase tracking-widest mb-4" style="color:#3C3C3C">Company</div> <div class="space-y-3 text-sm"> ${[
    { l: "Industry", v: cs.industry },
    { l: "Team size", v: cs.team_size }
  ].map(({ l, v }) => renderTemplate`<div class="flex justify-between"> <span style="color:#666666">${l}</span> <span class="font-semibold" style="color:#F0F0F0">${v}</span> </div>`)} </div> </div> <!-- Features used --> <div class="card p-5"> <div class="text-xs font-mono uppercase tracking-widest mb-4" style="color:#3C3C3C">Features Used</div> <ul class="space-y-2"> ${["Sherlock AI", "Content-addressed cache", "OTel trace export", "BYOC runners", "Ory identity stack"].map((f) => renderTemplate`<li class="flex items-center gap-2 text-xs font-mono" style="color:#AAAAAA"> <span style="color:#00FF88">✓</span> ${f} </li>`)} </ul> </div> <!-- Other stories --> <div class="card p-5"> <div class="text-xs font-mono uppercase tracking-widest mb-4" style="color:#3C3C3C">More stories</div> <div class="space-y-3"> ${MOCK_CASE_STUDIES.filter((c) => c.slug !== cs.slug).map((other) => renderTemplate`<a${addAttribute(`/customers/${other.slug}`, "href")} class="block group"> <div class="flex items-center gap-2"> <span class="text-lg">${other.logo_emoji}</span> <span class="text-xs font-semibold group-hover:text-[#FFEE00] transition-colors" style="color:#F0F0F0">${other.company}</span> </div> <div class="text-xs mt-0.5 truncate" style="color:#666666">${other.results[0].metric}: ${other.results[0].before} → ${other.results[0].after}</div> </a>`)} </div> </div> </div> </aside> </div> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/customers/[slug].astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/customers/[slug].astro";
const $$url = "/customers/[slug]";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$slug,
  file: $$file,
  getStaticPaths,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

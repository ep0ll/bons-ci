import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../chunks/MarketingLayout_tAfEjAWH.mjs';
import { M as MOCK_BLOG_POSTS } from '../chunks/mock_JVAQtub_.mjs';
export { renderers } from '../renderers.mjs';

const $$Blog = createComponent(($$result, $$props, $$slots) => {
  const posts = MOCK_BLOG_POSTS;
  const featured = posts.find((p) => p.featured);
  const rest = posts.filter((p) => !p.featured);
  const cats = ["All", ...new Set(posts.map((p) => p.category))];
  const catCls = { Product: "badge-accent", Engineering: "badge-info", Tutorial: "badge-success", Security: "badge-warning", Company: "badge-neutral" };
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": "Blog \u2014 Forge CI", "description": "Engineering deep-dives, product updates, tutorials, and company news from the Forge CI team." }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<section class="relative pt-32 pb-12 overflow-hidden"> <div class="absolute inset-0 bg-grid opacity-25 pointer-events-none"></div> <div class="container-forge relative z-10"> <div class="section-label mb-5">Blog</div> <div class="flex flex-col sm:flex-row sm:items-end justify-between gap-4 mb-8"> <h1 class="section-title max-w-lg">From the <span class="text-gradient">Forge CI team.</span></h1> <a href="/rss/blog.xml" class="btn-ghost btn-sm text-[#AAAAAA] flex items-center gap-1.5"> <svg class="w-3.5 h-3.5 text-[#FF7700]" fill="currentColor" viewBox="0 0 20 20"><path d="M5 3a1 1 0 000 2c5.523 0 10 4.477 10 10a1 1 0 102 0C17 8.373 11.627 3 5 3zm.001 5.924a1 1 0 10-.002 2 5.076 5.076 0 015.077 5.077 1 1 0 102 0 7.077 7.077 0 00-7.075-7.077zM4 15a2 2 0 114 0 2 2 0 01-4 0z"></path></svg>
RSS
</a> </div> <!-- Category filter --> <div class="flex flex-wrap gap-2"> ${cats.map((cat, i) => renderTemplate`<button${addAttribute(["cat-pill text-xs font-medium px-3 py-1.5 rounded-full border transition-all", i === 0 ? "border-[rgba(255,238,0,0.4)] bg-[rgba(255,238,0,0.08)] text-[#FFEE00]" : "border-[#2C2C2C] text-[#666666] hover:border-[#3C3C3C] hover:text-[#F0F0F0]"], "class:list")}${addAttribute(cat, "data-cat")} onclick="filterBlog(this)">${cat}</button>`)} </div> </div> </section> <div class="container-forge pb-20 space-y-8"> <!-- Featured post --> <a${addAttribute(`/blog/${featured.slug}`, "href")} class="blog-card block card-hover overflow-hidden group"${addAttribute(featured.category, "data-cat")}> <div class="grid md:grid-cols-[1fr_320px]"> <div class="p-8 sm:p-10"> <div class="flex items-center gap-2 mb-4"> <span${addAttribute(`badge border text-xs ${catCls[featured.category] ?? "badge-neutral"}`, "class")}>${featured.category}</span> <span class="badge-accent text-xs">Featured</span> </div> <h2 class="font-bold text-2xl sm:text-3xl text-[#F0F0F0] group-hover:text-[#FFEE00] transition-colors mb-4 leading-tight">${featured.title}</h2> <p class="text-[#AAAAAA] leading-relaxed mb-6">${featured.excerpt}</p> <div class="flex items-center gap-4 text-sm text-[#666666]"> <div class="avatar-sm avatar-base">${featured.author.initials}</div> <span class="text-[#AAAAAA]">${featured.author.name}</span> <span>·</span><span>${featured.published_at}</span> <span>·</span><span>${featured.read_time_min} min read</span> </div> </div> <div class="bg-[#1A1A1A] flex items-center justify-center text-7xl border-l border-[#2C2C2C] min-h-[180px]"> ${featured.image_emoji} </div> </div> </a> <!-- Grid --> <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-5" id="blog-grid"> ${rest.map((p) => renderTemplate`<a${addAttribute(`/blog/${p.slug}`, "href")} class="blog-card card-hover flex flex-col group overflow-hidden"${addAttribute(p.category, "data-cat")}> <div class="bg-[#1A1A1A] py-8 flex items-center justify-center text-5xl border-b border-[#2C2C2C]">${p.image_emoji}</div> <div class="p-5 flex flex-col flex-1"> <div class="flex items-center gap-2 mb-3"> <span${addAttribute(`badge border text-xs ${catCls[p.category] ?? "badge-neutral"}`, "class")}>${p.category}</span> <span class="text-xs text-[#666666]">${p.read_time_min} min</span> </div> <h2 class="font-bold text-base text-[#F0F0F0] group-hover:text-[#FFEE00] transition-colors mb-2 leading-snug flex-1">${p.title}</h2> <p class="text-xs text-[#666666] line-clamp-2 leading-relaxed mb-4">${p.excerpt}</p> <div class="flex items-center gap-2 pt-4 border-t border-[#2C2C2C]"> <div class="avatar-sm avatar-base text-xs">${p.author.initials}</div> <div><div class="text-xs text-[#AAAAAA]">${p.author.name}</div><div class="text-xs text-[#666666]">${p.published_at}</div></div> </div> </div> </a>`)} </div> <div id="blog-empty" class="hidden text-center py-10 text-[#666666]">No posts in this category yet.</div> <!-- Newsletter --> <div class="card p-8 text-center max-w-lg mx-auto"> <div class="text-3xl mb-3">📬</div> <h3 class="font-bold text-xl text-[#F0F0F0] mb-2">Never miss a post</h3> <p class="text-sm text-[#AAAAAA] mb-5">Engineering deep-dives and product updates. Roughly twice a month.</p> <div class="flex gap-2"><input type="email" placeholder="you@company.com" class="input flex-1"><button class="btn-primary btn-md flex-shrink-0">Subscribe</button></div> </div> </div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/blog.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/blog.astro";
const $$url = "/blog";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Blog,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

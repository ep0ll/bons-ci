import { c as createAstro, a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, e as renderSlot, d as addAttribute } from './astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$BaseLayout } from './BaseLayout_DtuG-pEw.mjs';

const $$Astro = createAstro("https://forge-ci.dev");
const $$AuthLayout = createComponent(($$result, $$props, $$slots) => {
  const Astro2 = $$result.createAstro($$Astro, $$props, $$slots);
  Astro2.self = $$AuthLayout;
  const { ...rest } = Astro2.props;
  return renderTemplate`${renderComponent($$result, "BaseLayout", $$BaseLayout, { ...rest, "noindex": true }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="min-h-screen flex" style="background:#0A0A0A"> <!-- Left: form --> <div class="flex-1 flex flex-col justify-center px-6 py-12 lg:px-12 xl:px-16 max-w-xl"> <div class="w-full max-w-sm mx-auto" style="animation:slideUp 0.3s ease-out"> ${renderSlot($$result2, $$slots["default"])} </div> </div> <!-- Right: live panel --> <div class="hidden lg:flex flex-1 flex-col relative overflow-hidden border-l-2" style="border-color:#2C2C2C;background:#060606"> <div class="absolute inset-0 bg-grid-brut opacity-30 pointer-events-none"></div> <div class="absolute inset-0 pointer-events-none" style="background:radial-gradient(ellipse 500px 600px at 60% 30%,rgba(255,238,0,0.04),transparent 60%)"></div> <div class="relative z-10 flex flex-col justify-center h-full px-10 xl:px-14"> <div class="section-label mb-8 self-start">LIVE BUILD STREAM</div> <div class="space-y-2 mb-10"> ${[
    { repo: "acme/api-service", branch: "main", status: "success", dur: "1m 24s", sha: "c1b2f3d" },
    { repo: "acme/web-app", branch: "feat/dashboard", status: "running", dur: "0m 38s", sha: "a4b5c6e" },
    { repo: "acme/infra", branch: "main", status: "success", dur: "3m 44s", sha: "f7a8b9c" },
    { repo: "acme/mobile", branch: "fix/crash-ios", status: "failed", dur: "7m 44s", sha: "d0e1f2a" },
    { repo: "acme/docs", branch: "main", status: "success", dur: "0m 44s", sha: "b3c4d5e" },
    { repo: "acme/sdk", branch: "v3.0.0-rc1", status: "success", dur: "2m 12s", sha: "e6f7a8b" }
  ].map((b) => {
    const col = b.status === "success" ? "#00FF88" : b.status === "running" ? "#00EEFF" : "#FF3333";
    return renderTemplate`<div class="flex items-center gap-3 px-3 py-2.5 border" style="border-color:#2C2C2C;background:rgba(26,26,26,0.5)"> <div class="w-2 h-2 rounded-full flex-shrink-0"${addAttribute(`background:${col}${b.status === "running" ? ";animation:pulse 1s ease-in-out infinite" : ""}`, "style")}></div> <div class="flex-1 min-w-0"> <div class="text-xs font-mono font-semibold truncate" style="color:#F0F0F0">${b.repo}</div> <div class="text-xs font-mono truncate" style="color:#666666">${b.branch} · ${b.sha}</div> </div> <div class="text-right flex-shrink-0"> <div class="text-xs font-mono font-bold"${addAttribute(`color:${col}`, "style")}>${b.status}</div> <div class="text-xs font-mono" style="color:#666666">${b.dur}</div> </div> </div>`;
  })} </div> <div class="grid grid-cols-3 gap-4 pt-6 mb-10 border-t-2" style="border-color:#2C2C2C"> ${[{ v: "10\xD7", l: "Faster builds", s: "vs GitHub Actions" }, { v: "99.99%", l: "Uptime SLA", s: "last 12 months" }, { v: "50K+", l: "Teams", s: "building daily" }].map((s) => renderTemplate`<div class="text-center"> <div class="font-bold text-2xl" style="font-family:'Space Grotesk';color:#FFEE00;letter-spacing:-0.04em">${s.v}</div> <div class="text-xs font-semibold mt-0.5" style="color:#F0F0F0">${s.l}</div> <div class="text-xs font-mono" style="color:#666666">${s.s}</div> </div>`)} </div> <div class="card p-5 card-accent"> <p class="text-sm italic leading-relaxed mb-3" style="color:#AAAAAA">"Forge CI pays for itself in engineer-hours every week. Migration from Jenkins took two days."</p> <div class="flex items-center gap-3"> <div class="avatar-sm">TR</div> <div><div class="text-xs font-semibold" style="color:#F0F0F0">Tomás Reyes</div><div class="text-xs font-mono" style="color:#666666">Platform Lead · Stackline</div></div> </div> </div> <div class="mt-8 text-xs font-mono" style="color:#3C3C3C">forge-ci v2.4.0 · ory kratos v1.2 · keto v0.11</div> </div> </div> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/layouts/AuthLayout.astro", void 0);

export { $$AuthLayout as $ };

import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../chunks/MarketingLayout_tAfEjAWH.mjs';
export { renderers } from '../renderers.mjs';

const $$Security = createComponent(($$result, $$props, $$slots) => {
  const controls = [
    { title: "Encryption at rest", desc: "All data encrypted with AES-256-GCM. Keys managed via AWS KMS with automatic 90-day rotation." },
    { title: "Encryption in transit", desc: "TLS 1.3 enforced everywhere. HSTS preloading. Certificate pinning for runner-to-control-plane auth." },
    { title: "Ephemeral runners", desc: "Every build runs in a freshly provisioned VM destroyed immediately after. No state persists between jobs." },
    { title: "Network isolation", desc: "Each build gets an isolated network namespace. Egress firewall rules enforced per-org with default-deny." },
    { title: "Secret isolation", desc: "Secrets injected via tmpfs memory mounts. Never written to disk. Automatically masked in all log output." },
    { title: "OIDC token exchange", desc: "Short-lived cloud credentials per job via OIDC. AWS, GCP, Azure. No long-lived keys stored anywhere." },
    { title: "Zero-trust architecture", desc: "mTLS between all internal services. Every API call authenticated, authorized, and rate-limited independently." },
    { title: "Vulnerability disclosure", desc: "Responsible disclosure program on HackerOne. 90-day coordinated timeline. Bug bounty for qualifying reports." }
  ];
  const certs = [
    { name: "SOC 2 Type II", icon: "\u{1F6E1}", status: "Current", cls: "badge-success border border-[rgba(0,255,136,0.2)]" },
    { name: "ISO 27001", icon: "\u{1F4CB}", status: "Current", cls: "badge-success border border-[rgba(0,255,136,0.2)]" },
    { name: "GDPR", icon: "\u{1F1EA}\u{1F1FA}", status: "Compliant", cls: "badge-success border border-[rgba(0,255,136,0.2)]" },
    { name: "HIPAA Ready", icon: "\u{1F3E5}", status: "Ready", cls: "badge-info border border-[rgba(51,170,255,0.2)]" },
    { name: "Pen-tested", icon: "\u{1F50D}", status: "Annual", cls: "badge-success border border-[rgba(0,255,136,0.2)]" },
    { name: "FedRAMP", icon: "\u{1F3DB}", status: "In progress", cls: "badge-warning border border-[rgba(245,158,11,0.2)]" }
  ];
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": "Security \u2014 Forge CI", "description": "Forge CI security architecture, certifications, and responsible disclosure program." }, { "default": ($$result2) => renderTemplate`  ${maybeRenderHead()}<section class="relative pt-32 pb-20 overflow-hidden"> <div class="absolute inset-0 bg-grid opacity-25 pointer-events-none"></div> <div class="absolute inset-0 pointer-events-none" style="background:radial-gradient(ellipse 800px 400px at 50% 0,rgba(255,238,0,0.06),transparent 60%)"></div> <div class="container-forge relative z-10 text-center max-w-3xl mx-auto"> <div class="section-label mb-6">Security</div> <h1 class="section-title mb-6">Security isn't a feature.<br><span class="text-gradient">It's the foundation.</span></h1> <p class="section-subtitle mx-auto text-center mb-10">
Every build runs in an ephemeral, isolated environment. Secrets never touch disk.
        Every action is audited. Here's exactly how we protect your code and your team.
</p> <div class="flex flex-col sm:flex-row gap-3 justify-center"> <a href="/docs/security" class="btn-primary btn-lg">Read security docs</a> <a href="mailto:security@forge-ci.dev" class="btn-secondary btn-lg">Report a vulnerability</a> </div> </div> </section>  <section class="section border-t border-[#2C2C2C] bg-[rgba(12,16,24,0.4)]"> <div class="container-forge"> <h2 class="font-bold text-3xl text-[#F0F0F0] text-center mb-10">Certifications & compliance</h2> <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-4 max-w-3xl mx-auto"> ${certs.map((c) => renderTemplate`<div class="card p-5 flex items-start gap-3"> <span class="text-2xl flex-shrink-0">${c.icon}</span> <div> <div class="flex items-center gap-2 mb-1 flex-wrap"> <span class="text-sm font-medium text-[#F0F0F0]">${c.name}</span> <span${addAttribute(`badge text-xs ${c.cls}`, "class")}>${c.status}</span> </div> <p class="text-xs text-[#666666] leading-relaxed"> ${c.name === "SOC 2 Type II" && "Annual third-party audit. Report available under NDA within 24h."} ${c.name === "ISO 27001" && "ISMS certification. Audited by BSI Group annually."} ${c.name === "GDPR" && "DPA available. EU-US Data Privacy Framework certified."} ${c.name === "HIPAA Ready" && "BAA available. Architecture reviewed by healthcare counsel."} ${c.name === "Pen-tested" && "External pentest by NCC Group. Last completed Feb 2025."} ${c.name === "FedRAMP" && "Targeting Moderate authorization in H2 2025."} </p> </div> </div>`)} </div> </div> </section>  <section class="section border-t border-[#2C2C2C]"> <div class="container-forge"> <div class="text-center mb-12"> <h2 class="font-bold text-3xl text-[#F0F0F0]">Security controls</h2> <p class="text-[#AAAAAA] mt-3">How we protect every build, every secret, every byte.</p> </div> <div class="grid sm:grid-cols-2 gap-4 max-w-4xl mx-auto"> ${controls.map((c) => renderTemplate`<div class="card p-5 flex gap-4 hover:border-[rgba(255,238,0,0.2)] transition-colors"> <div class="w-8 h-8 rounded-full bg-[rgba(0,255,136,0.1)] border border-[rgba(0,255,136,0.3)] flex items-center justify-center flex-shrink-0 mt-0.5"> <svg class="w-4 h-4 text-[#00FF88]" fill="currentColor" viewBox="0 0 20 20"><path fill-rule="evenodd" d="M2.166 4.999A11.954 11.954 0 0010 1.944 11.954 11.954 0 0017.834 5c.11.65.166 1.32.166 2.001 0 5.225-3.34 9.67-8 11.317C5.34 16.67 2 12.225 2 7c0-.682.057-1.35.166-2.001zm11.541 3.708a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z" clip-rule="evenodd"></path></svg> </div> <div> <h3 class="text-sm font-medium text-[#F0F0F0] mb-1">${c.title}</h3> <p class="text-xs text-[#AAAAAA] leading-relaxed">${c.desc}</p> </div> </div>`)} </div> </div> </section>  <section class="section border-t border-[#2C2C2C] bg-[rgba(12,16,24,0.4)]"> <div class="container-forge max-w-3xl"> <h2 class="font-bold text-3xl text-[#F0F0F0] text-center mb-10">Build isolation model</h2> <div class="card p-6 font-mono text-sm space-y-2"> <div class="text-[#666666]">┌─ Build Job #${`{id}`} ────────────────────────────────────────────┐</div> <div class="text-[#666666]">│</div> <div class="pl-4 text-[#AAAAAA]">│ <span class="text-[#FFEE00]">VM: freshly provisioned</span> · ephemeral · destroyed after job</div> <div class="pl-4 text-[#AAAAAA]">│ <span class="text-[#00EEFF]">Network: isolated namespace</span> · egress firewall applied</div> <div class="pl-4 text-[#AAAAAA]">│ <span class="text-[#00FF88]">Secrets: tmpfs memory mount</span> · never on disk · auto-masked</div> <div class="pl-4 text-[#AAAAAA]">│ <span class="text-[#FF7700]">Cloud creds: OIDC tokens</span> · scoped per job · auto-expired</div> <div class="text-[#666666]">│</div> <div class="text-[#666666]">└──────────────────────────────────────────────────────────────┘</div> <div class="text-xs text-[#666666] pt-2">→ After job: VM destroyed · secrets purged · network torn down · OIDC tokens expire</div> </div> </div> </section>  <section class="section border-t border-[#2C2C2C]"> <div class="container-forge max-w-2xl text-center"> <div class="text-5xl mb-6">🔍</div> <h2 class="font-bold text-3xl text-[#F0F0F0] mb-4">Found a vulnerability?</h2> <p class="text-[#AAAAAA] mb-8 leading-relaxed">
We run a responsible disclosure program on HackerOne. We target a 90-day coordinated disclosure timeline
        and offer bug bounties for qualifying reports. We will never pursue legal action against researchers acting in good faith.
</p> <div class="flex flex-col sm:flex-row gap-3 justify-center mb-8"> <a href="https://hackerone.com/forge-ci" class="btn-primary btn-lg" target="_blank" rel="noopener noreferrer">Report on HackerOne</a> <a href="mailto:security@forge-ci.dev" class="btn-secondary btn-lg">Email security team</a> </div> <p class="text-xs text-[#666666]">
PGP key: <code class="font-mono text-[#FFEE00]">forge-ci.dev/.well-known/pgp-key.asc</code> </p> </div> </section> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/security.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/security.astro";
const $$url = "/security";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Security,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

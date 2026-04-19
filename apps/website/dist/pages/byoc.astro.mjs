import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead } from '../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../chunks/MarketingLayout_tAfEjAWH.mjs';
export { renderers } from '../renderers.mjs';

const $$Byoc = createComponent(($$result, $$props, $$slots) => {
  const clouds = [
    { name: "AWS", emoji: "\u2601", color: "#FF9900", desc: "EC2, EKS, Fargate. Graviton3 ARM64. OIDC-based auth \u2014 no long-lived keys stored anywhere." },
    { name: "GCP", emoji: "\u{1F310}", color: "#4285F4", desc: "GCE, GKE, Cloud Run. Workload Identity Federation. Multi-region support." },
    { name: "Azure", emoji: "\u{1F499}", color: "#0078D4", desc: "AKS, Azure VMs. Managed Identity and AD Workload Federation supported out of the box." },
    { name: "On-prem", emoji: "\u{1F5A5}", color: "#00FF88", desc: "Bare metal, VMware, Proxmox, or any Linux x64/ARM64 host. Register via the runner agent." }
  ];
  const steps = [
    { n: 1, title: "Install the runner agent", body: "One-line install script. Works on any Linux (x64 or ARM64) or macOS host. Systemd, Docker, or Kubernetes DaemonSet." },
    { n: 2, title: "Register with your org", body: "Run `forge runner register` with your org token. Authentication uses mTLS \u2014 no inbound firewall rules needed." },
    { n: 3, title: "Label your runners", body: "Tag runners by size, region, GPU, OS, or any custom label. Route jobs with `runs-on:` in your pipeline YAML." },
    { n: 4, title: "Build on your infra", body: "Builds run exclusively on your machines. Source code, artifacts, and secrets never leave your network." }
  ];
  const benefits = [
    { icon: "\u{1F512}", title: "Zero data egress", desc: "Source code, secrets, and build artifacts stay inside your VPC. Forge CI sends only job metadata and log streaming." },
    { icon: "\u{1F4B8}", title: "Use your existing budget", desc: "Runs on your existing AWS, GCP, or Azure committed spend. No surprise Forge CI network egress costs." },
    { icon: "\u26A1", title: "Full Forge CI feature set", desc: "Caching, Sherlock AI, matrix builds, waterfall metrics, and real-time dashboards all work on BYOC runners." },
    { icon: "\u{1F30D}", title: "Any region, any latency", desc: "Run runners co-located with your data \u2014 same region as your RDS instance, Kubernetes cluster, or artifact store." }
  ];
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": "BYOC Runners \u2014 Forge CI", "description": "Bring your own cloud. Run Forge CI builds on AWS, GCP, Azure, or bare metal. Source code never leaves your perimeter." }, { "default": ($$result2) => renderTemplate`  ${maybeRenderHead()}<section class="relative pt-32 pb-20 overflow-hidden"> <div class="absolute inset-0 bg-grid opacity-25 pointer-events-none"></div> <div class="absolute inset-0 pointer-events-none" style="background:radial-gradient(ellipse 1000px 500px at 50% 0,rgba(0,238,255,0.06),transparent 60%)"></div> <div class="container-forge relative z-10 text-center max-w-3xl mx-auto"> <div class="section-label mb-6">BYOC Runners</div> <h1 class="section-title mb-6">Your cloud. <span class="text-gradient">Our brains.</span></h1> <p class="section-subtitle mx-auto text-center mb-10">
Register your own EC2 instances, GKE nodes, or bare-metal machines as Forge CI runners.
        Full CI/CD orchestration, Sherlock AI, and real-time metrics — zero data leaving your perimeter.
</p> <div class="flex flex-col sm:flex-row gap-3 justify-center"> <a href="/docs/byoc" class="btn-primary btn-xl">Read the BYOC docs</a> <a href="/auth/signup?plan=team" class="btn-secondary btn-xl">Start free trial</a> </div> </div> </section>  <section class="section border-t border-[#2C2C2C]"> <div class="container-forge"> <h2 class="font-bold text-3xl text-[#F0F0F0] text-center mb-10">Works with any cloud or host</h2> <div class="grid sm:grid-cols-2 lg:grid-cols-4 gap-4"> ${clouds.map((c) => renderTemplate`<div class="card p-6 hover:border-[rgba(255,238,0,0.2)] transition-colors"> <div class="text-4xl mb-4">${c.emoji}</div> <h3 class="font-bold text-xl text-[#F0F0F0] mb-2">${c.name}</h3> <p class="text-sm text-[#AAAAAA] leading-relaxed">${c.desc}</p> </div>`)} </div> </div> </section>  <section class="section border-t border-[#2C2C2C] bg-[rgba(12,16,24,0.4)]"> <div class="container-forge max-w-3xl"> <h2 class="font-bold text-3xl text-[#F0F0F0] text-center mb-12">Set up in 4 steps</h2> <div class="space-y-6 mb-10"> ${steps.map((s) => renderTemplate`<div class="flex gap-5 items-start appear"> <div class="w-10 h-10 rounded-full bg-[#FFEE00] flex items-center justify-center font-bold text-[#0A0A0A] flex-shrink-0">${s.n}</div> <div> <h3 class="font-bold text-lg text-[#F0F0F0] mb-1">${s.title}</h3> <p class="text-[#AAAAAA] leading-relaxed">${s.body}</p> </div> </div>`)} </div> <!-- CLI snippet --> <div class="terminal"> <div class="terminal-header"> <div class="terminal-dot bg-[#FF5F57]"></div> <div class="terminal-dot bg-[#FFBD2E]"></div> <div class="terminal-dot bg-[#28C840]"></div> <span class="text-xs font-mono text-[#666666] ml-2">Install runner agent</span> </div> <pre class="p-5 font-mono text-xs text-[#AAAAAA] leading-relaxed overflow-x-auto"><code><span class="text-[#666666]"># Install on any Linux host (x64 or ARM64)</span>
<span class="text-[#FFEE00]">curl -sSL https://install.forge-ci.dev/runner | bash</span>

<span class="text-[#666666]"># Register with your organization</span>
<span class="text-[#F0F0F0]">forge runner register \\
  --org acme-corp \\
  --token $FORGE_RUNNER_TOKEN \\
  --labels linux,x64,large,us-west-2 \\
  --concurrency 4</span>

<span class="text-[#666666]"># Runner is now active</span>
<span class="text-[#00FF88]">✓ Runner acme-r8f2 registered · waiting for jobs</span></code></pre> </div> </div> </section>  <section class="section border-t border-[#2C2C2C]"> <div class="container-forge"> <div class="grid sm:grid-cols-2 lg:grid-cols-4 gap-6"> ${benefits.map((b) => renderTemplate`<div class="card p-6 text-center hover:border-[rgba(255,238,0,0.2)] transition-colors"> <div class="text-4xl mb-4">${b.icon}</div> <h3 class="font-bold text-base text-[#F0F0F0] mb-2">${b.title}</h3> <p class="text-sm text-[#AAAAAA] leading-relaxed">${b.desc}</p> </div>`)} </div> </div> </section>  <section class="section border-t border-[#2C2C2C] bg-[rgba(12,16,24,0.4)]"> <div class="container-forge max-w-xl text-center"> <h2 class="font-bold text-3xl text-[#F0F0F0] mb-4">BYOC is available on Team and Enterprise plans.</h2> <p class="text-[#AAAAAA] mb-8">Start a 14-day free trial, or talk to our team about enterprise licensing.</p> <div class="flex flex-col sm:flex-row gap-3 justify-center"> <a href="/auth/signup?plan=team" class="btn-primary btn-xl">Start free trial</a> <a href="/enterprise" class="btn-secondary btn-xl">Talk to enterprise sales</a> </div> </div> </section> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/byoc.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/byoc.astro";
const $$url = "/byoc";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Byoc,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

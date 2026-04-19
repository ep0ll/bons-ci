import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../chunks/MarketingLayout_tAfEjAWH.mjs';
export { renderers } from '../renderers.mjs';

const $$Features = createComponent(($$result, $$props, $$slots) => {
  const chartTypes = [
    { cat: "Time-Series & Metrics", icon: "\u{1F4CA}", charts: ["Line chart (build time trends, success rate)", "Area chart (queue depth, cumulative runs)", "Stacked area (parallel stage usage)", "Bar & stacked bar (per-branch failures)", "Histogram (build duration distribution)", "Heatmap (failures over time/day)", "Sparklines (inline metric trends)", "Percentile charts (P50, P90, P99 latency)", "Box plot / Violin plot (spread distribution)", "Control charts (process stability)"] },
    { cat: "Duration & Performance", icon: "\u23F1", charts: ["Waterfall chart (stage-by-stage duration)", "Gantt chart (task scheduling over time)", "Timeline chart (execution sequence)", "Flame graph (CPU/time per step)", "Critical path visualization", "Burn-down chart (remaining tasks vs time)"] },
    { cat: "Pipeline Structure", icon: "\u{1F501}", charts: ["Directed Acyclic Graph (pipeline DAG)", "Layered pipeline view (stages)", "Collapsible workflow tree", "Node-link graph (tasks + dependencies)", "Sankey diagram (flow across stages)", "State transition diagram"] },
    { cat: "Logs & Events", icon: "\u{1F4DC}", charts: ["Log stream viewer (tail -f style)", "Structured log table (JSON logs)", "Expandable log trees (nested steps)", "Log histogram (events over time)", "Pattern clustering (group similar logs)", "Logs linked to pipeline nodes"] },
    { cat: "Traces & Dependencies", icon: "\u{1F50D}", charts: ["Trace timeline (span-based view)", "Span waterfall (nested execution timing)", "Service dependency graph", "Critical path trace", "Causal graph (what triggered what)"] },
    { cat: "Resource & Infra", icon: "\u{1F4E6}", charts: ["CPU / memory usage charts", "Disk I/O and network throughput", "Container/pod status charts", "Node utilization heatmaps", "Cost per pipeline chart"] },
    { cat: "Queue & Concurrency", icon: "\u{1F504}", charts: ["Queue timeline", "Parallelism graph", "Worker utilization chart", "Job scheduling heatmap", "Wait time distribution"] },
    { cat: "AI & Insights", icon: "\u{1F916}", charts: ["Anomaly detection charts", "Failure clustering maps", "Root cause suggestion (Sherlock)", "Predictive duration charts", "Regression detection graphs"] }
  ];
  const sectionFeatures = [
    {
      id: "speed",
      label: "Speed",
      icon: "\u26A1",
      color: "#FFEE00",
      title: "10\xD7 Faster. Provably.",
      desc: "We obsess over every millisecond. Pre-warmed runners start in under 2 seconds. Content-addressed caching with 89% average hit rate. Intelligent build graph analysis runs only what changed. Real-time waterfall percentile charts let you identify bottlenecks at a glance.",
      features: ["Pre-warmed runner pool \u2014 <2s cold start", "Content-addressed cache \xB7 89% avg hit rate", "Smart parallelism across 50 concurrent workers", "Auto-cancel redundant builds on same branch", "Build minute overages at discounted rate", "Global anycast routing to 4 regions"],
      metric: "89% avg cache hit rate"
    },
    {
      id: "identity",
      label: "Ory Identity",
      icon: "\u25C6",
      color: "#00EEFF",
      title: "Enterprise identity. Open source.",
      desc: "Forge CI runs on the Ory identity stack \u2014 Kratos (authn), Hydra (OAuth2/OIDC), and Keto (authz). Every auth method you'll ever need: password, magic link, passkey/WebAuthn, TOTP, SMS OTP, SAML 2.0, SCIM, and 6 OAuth providers. Zero vendor lock-in.",
      features: ["Ory Kratos: email, password, magic link", "WebAuthn/Passkeys (FIDO2)", "TOTP (Authenticator app) + backup codes", "SAML 2.0 via Okta, Azure AD, Google Workspace", "SCIM 2.0 auto-provisioning", "Ory Keto: fine-grained RBAC (Owner/Admin/Member/Viewer)", "Ory Hydra: OAuth2 + PKCE for API tokens", "MFA enforcement per org or per user"],
      metric: "Zero vendor lock-in"
    },
    {
      id: "observability",
      label: "Observability",
      icon: "\u{1F4CA}",
      color: "#00FF88",
      title: "Full CI/CD observability. All chart types.",
      desc: "The full taxonomy from Grafana-style time-series to OpenTelemetry span waterfalls, flame graphs, DAG visualizations, and heatmaps. Real-time log streaming with regex search, level filtering, and ANSI color support. Built in \u2014 no separate Grafana instance required.",
      features: ["Recharts: line, area, bar, histogram, percentile", "D3: flame graphs, heatmaps, custom visualizations", "@xyflow/react: interactive DAG pipeline graph", "Span waterfall (trace timeline like Jaeger)", "Build duration heatmap (7d \xD7 24h)", "Anomaly detection with ML-powered baselines", "ANSI log rendering via ansi-to-html", "Full-text log search with regex and level filter"],
      metric: "13 chart types built in"
    },
    {
      id: "security",
      label: "Security",
      icon: "\u25C9",
      color: "#FF3333",
      title: "Zero-trust from day one.",
      desc: "Every build runs in an ephemeral VM, destroyed immediately after. Secrets injected via tmpfs memory mounts \u2014 never written to disk. OIDC-based cloud credentials (AWS, GCP, Azure) scoped per job and auto-expired. Network egress firewall with default-deny. SOC 2 Type II certified.",
      features: ["Ephemeral runners \u2014 fresh VM per build", "Secrets on tmpfs \u2014 never on disk, auto-masked", "OIDC short-lived cloud credentials per job", "Network egress firewall (default-deny)", "Ory Keto RBAC \u2014 granular per-resource permissions", "Full audit log (Ory Kratos events)", "SOC 2 Type II \xB7 GDPR \xB7 HIPAA ready"],
      metric: "SOC 2 Type II certified"
    }
  ];
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": "Features \u2014 Forge CI", "description": "All Forge CI features: speed, Ory identity stack, full observability suite, security, runners, and caching." }, { "default": ($$result2) => renderTemplate`  ${maybeRenderHead()}<section class="relative pt-32 pb-16 overflow-hidden"> <div class="absolute inset-0 bg-grid-brut opacity-30 pointer-events-none"></div> <div class="container-forge relative z-10"> <div class="section-label mb-5">Features</div> <h1 class="section-title mb-5 max-w-3xl">Everything your team needs.<br><span class="text-gradient-brut">Zero compromise.</span></h1> <p class="section-subtitle max-w-2xl">From solo devs to 2,000-person platform teams, Forge CI scales with every workflow.</p> </div> </section>  ${sectionFeatures.map((sec, si) => renderTemplate`<section${addAttribute(sec.id, "id")}${addAttribute(["section border-t-2", si % 2 === 1 ? "" : "bg-[#060606]"], "class:list")} style="border-color:#2C2C2C"> <div class="container-forge"> <div class="grid lg:grid-cols-2 gap-14 items-center"> <div${addAttribute([si % 2 === 1 ? "lg:order-2" : ""], "class:list")}> <div class="section-label mb-5"${addAttribute(`color:${sec.color};border-color:${sec.color}40`, "style")}>${sec.label}</div> <h2 class="font-bold tracking-tight mb-5"${addAttribute(`font-family:'Space Grotesk';font-size:clamp(1.75rem,3.5vw,2.5rem);letter-spacing:-0.04em;color:#F0F0F0;line-height:1.05`, "style")}>${sec.title}</h2> <p class="text-lg leading-relaxed mb-8" style="color:#AAAAAA">${sec.desc}</p> <ul class="space-y-2.5 mb-8"> ${sec.features.map((f) => renderTemplate`<li class="flex items-start gap-3 text-sm" style="color:#AAAAAA"> <span class="flex-shrink-0 mt-0.5 font-mono font-bold"${addAttribute(`color:${sec.color}`, "style")}>→</span> ${f} </li>`)} </ul> <div class="flex items-center gap-4"> <div class="text-sm font-mono font-bold"${addAttribute(`color:${sec.color}`, "style")}>◆ ${sec.metric}</div> <a href="/auth/signup" class="btn-primary btn-md">Try it free</a> </div> </div> <div${addAttribute([si % 2 === 1 ? "lg:order-1" : ""], "class:list")}> <div class="card p-6"${addAttribute(`border-color:${sec.color}40;box-shadow:4px 4px 0 ${sec.color}30`, "style")}> <div class="text-4xl mb-4">${sec.icon}</div> <div class="text-xs font-mono uppercase tracking-widest mb-4" style="color:#666666">${sec.label} highlights</div> <div class="space-y-2"> ${sec.features.slice(0, 5).map((f) => renderTemplate`<div class="flex items-center gap-2.5 px-3 py-2.5 border" style="border-color:#2C2C2C;background:#111111"> <div class="w-1.5 h-1.5 flex-shrink-0 rounded-full"${addAttribute(`background:${sec.color}`, "style")}></div> <span class="text-xs" style="color:#AAAAAA">${f}</span> </div>`)} </div> </div> </div> </div> </div> </section>`)} <section class="section border-t-2" style="border-color:#2C2C2C;background:#060606"> <div class="container-forge"> <div class="mb-12 appear"> <div class="section-label mb-5">Observability Suite</div> <h2 class="section-title mb-5">Every chart type.<br><span class="text-gradient-brut">Built in.</span></h2> <p class="section-subtitle">From Grafana-style time-series to OpenTelemetry span waterfalls — no external dashboards required.</p> </div> <div class="grid sm:grid-cols-2 lg:grid-cols-4 gap-0 border-l-2 border-t-2" style="border-color:#2C2C2C"> ${chartTypes.map((cat) => renderTemplate`<div class="border-r-2 border-b-2 p-5 appear" style="border-color:#2C2C2C"> <div class="flex items-center gap-2 mb-4"> <span class="text-lg">${cat.icon}</span> <h3 class="text-xs font-mono font-bold uppercase tracking-widest" style="color:#FFEE00">${cat.cat}</h3> </div> <ul class="space-y-1.5"> ${cat.charts.map((c) => renderTemplate`<li class="flex items-start gap-2 text-xs" style="color:#666666"> <span style="color:#2C2C2C;flex-shrink:0">▸</span>${c} </li>`)} </ul> </div>`)} </div> </div> </section>  <section class="section border-t-2 text-center" style="border-color:#2C2C2C"> <div class="container-forge max-w-2xl"> <h2 class="section-title mb-4">Ready to see it yourself?</h2> <p class="section-subtitle mx-auto mb-8">Start free. No credit card. Set up in 2 minutes.</p> <div class="flex flex-col sm:flex-row gap-3 justify-center"> <a href="/auth/signup" class="btn-primary btn-xl">Start building free →</a> <a href="/pricing" class="btn-secondary btn-xl">View pricing</a> </div> </div> </section> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/features.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/features.astro";
const $$url = "/features";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Features,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

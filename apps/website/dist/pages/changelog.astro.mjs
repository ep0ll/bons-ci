import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$MarketingLayout } from '../chunks/MarketingLayout_tAfEjAWH.mjs';
export { renderers } from '../renderers.mjs';

const CHANGELOG = [
  {
    version: "v2.4.0",
    date: "2025-04-10",
    highlight: true,
    entries: [
      { type: "feature", breaking: false, title: "Sherlock AI is now Generally Available", body: "After 6 months of beta with 2,000+ teams, Sherlock AI exits beta on all Team and Enterprise plans. Root-cause analysis, one-click fix PRs, and flaky-test detection are now production-grade.", tags: ["ai", "sherlock"] },
      { type: "feature", breaking: false, title: "BYOC ARM64 runner support", body: "Register AWS Graviton3 and Apple Silicon hosts as BYOC runners. Graviton3 delivers ~40% better price-performance for most Node.js and Go workloads.", tags: ["byoc", "runners", "arm64"] },
      { type: "feature", breaking: false, title: "OTel span export", body: "Build steps now export OpenTelemetry spans to any OTLP endpoint. Wire Forge CI directly into Grafana Tempo, Honeycomb, or Datadog APM.", tags: ["observability", "otel"] },
      { type: "improvement", breaking: false, title: "Waterfall view in build detail", body: "The build detail page now shows a Gantt-style waterfall chart for all steps, making it easy to spot serial bottlenecks at a glance.", tags: ["ui", "observability"] },
      { type: "fix", breaking: false, title: "Cache invalidation on runner OS upgrade", body: "Fixed a bug where upgrading the runner OS version did not invalidate the node_modules cache, causing intermittent failures.", tags: ["cache", "bugfix"] }
    ]
  },
  {
    version: "v2.3.2",
    date: "2025-03-28",
    highlight: false,
    entries: [
      { type: "fix", breaking: false, title: "OIDC token refresh race condition", body: "Fixed a race condition in the OIDC token refresh path that caused ~0.1% of builds to fail with an invalid_grant error on long-running jobs.", tags: ["security", "oidc"] },
      { type: "fix", breaking: false, title: "Matrix build progress reporting", body: 'Matrix build steps now correctly aggregate individual job statuses in real time, preventing false "passed" states during partially failed matrices.', tags: ["matrix", "bugfix"] },
      { type: "improvement", breaking: false, title: "P99 build duration surface in insights", body: "The Insights page now surfaces P99 build duration per project over the last 30 days with a percentile comparison chart.", tags: ["insights", "metrics"] }
    ]
  },
  {
    version: "v2.3.1",
    date: "2025-03-14",
    highlight: false,
    entries: [
      { type: "fix", breaking: false, title: "Slack notification formatting", body: "Fixed broken block-kit formatting in Slack build notifications when commit messages contain backticks.", tags: ["integrations", "slack"] },
      { type: "improvement", breaking: false, title: "Sandbox idle timeout configurable", body: "Sandbox sessions can now configure an idle timeout (default 30 min, max 8 h) to reduce unnecessary compute costs.", tags: ["sandboxes"] }
    ]
  },
  {
    version: "v2.3.0",
    date: "2025-03-01",
    highlight: false,
    entries: [
      { type: "feature", breaking: false, title: "Heatmap build activity view", body: "A day-of-week × hour-of-day heatmap is now available in Insights, showing where build load clusters to help with runner capacity planning.", tags: ["insights", "observability"] },
      { type: "feature", breaking: false, title: "SCIM auto-provisioning (Enterprise)", body: "Enterprise orgs on Okta, Azure AD, and OneLogin can now auto-provision and deprovision members via SCIM 2.0.", tags: ["enterprise", "sso", "scim"] },
      { type: "improvement", breaking: false, title: "Runner queue fairness", body: "Implemented weighted fair-queuing for shared runners to prevent large orgs from monopolising capacity during peak periods.", tags: ["runners", "performance"] },
      { type: "breaking", breaking: true, title: "Deprecated `forge.yml` in favour of `.forge/pipeline.yml`", body: "Support for the legacy root-level `forge.yml` config file has been removed. Migrate to `.forge/pipeline.yml`. The migration guide is in the docs.", tags: ["pipeline", "config"] }
    ]
  },
  {
    version: "v2.2.0",
    date: "2025-02-12",
    highlight: false,
    entries: [
      { type: "feature", breaking: false, title: "OIDC secret injection (AWS, GCP, Azure)", body: "Configure AWS assume-role, GCP workload identity, or Azure federated credential directly in your pipeline to eliminate all long-lived cloud credentials.", tags: ["security", "oidc", "aws"] },
      { type: "feature", breaking: false, title: "Flame graph in build profiling", body: "Build steps that enable profiling export a flame graph viewable directly in the build detail UI, powered by a D3 icicle chart renderer.", tags: ["observability", "profiling"] },
      { type: "fix", breaking: false, title: "macOS M2 runner SSH agent forwarding", body: "Fixed SSH agent forwarding in macOS M2 runners that prevented private repo clones in submodule steps.", tags: ["runners", "macos", "bugfix"] }
    ]
  },
  {
    version: "v2.1.0",
    date: "2025-01-20",
    highlight: false,
    entries: [
      { type: "feature", breaking: false, title: "Log stream filter by level and regex", body: "The live log viewer now supports filtering by severity level (info / warn / error / debug) and regex search with highlighted matches.", tags: ["ui", "logs"] },
      { type: "feature", breaking: false, title: "Artifact expiry policy", body: "Org admins can now set a global artifact expiry policy (7 / 30 / 90 days, or never), overridable per-project.", tags: ["artifacts"] },
      { type: "improvement", breaking: false, title: "Faster cold-start: pre-warmed runner pool", body: "Linux x64 medium runners now maintain a warm pool of 50 pre-initialised VMs. P99 queue time dropped from 12 s to under 1 s.", tags: ["runners", "performance"] }
    ]
  },
  {
    version: "v2.0.0",
    date: "2024-12-01",
    highlight: true,
    entries: [
      { type: "breaking", breaking: true, title: "Pipeline YAML v2 format", body: "The pipeline config schema has been redesigned for clarity and power. Use `forge migrate` to auto-convert v1 configs. v1 support ends 2025-06-01.", tags: ["pipeline", "breaking"] },
      { type: "feature", breaking: false, title: "Ory identity stack (Kratos, Hydra, Keto)", body: "Authentication, OAuth, and RBAC are now powered by Ory — enabling WebAuthn passkeys, TOTP, hardware keys, and fine-grained permission policies.", tags: ["auth", "ory", "security"] },
      { type: "feature", breaking: false, title: "Image registry built-in", body: "Every org gets a private OCI-compliant container registry with vulnerability scanning, tag retention policies, and pull-through caching for Docker Hub.", tags: ["registry", "containers"] },
      { type: "feature", breaking: false, title: "Sandboxes", body: "Spin up an ephemeral dev environment from any build — port-forwarded, publicly accessible, and auto-expired. Perfect for debugging and demos.", tags: ["sandboxes"] },
      { type: "feature", breaking: false, title: "Marketplace plugin system", body: "Install official and community plugins for Slack, Datadog, Snyk, Jira, Linear, PagerDuty, Vault, and more from the Marketplace tab.", tags: ["marketplace", "plugins"] }
    ]
  },
  {
    version: "v1.9.0",
    date: "2024-10-15",
    highlight: false,
    entries: [
      { type: "feature", breaking: false, title: "Audit log export (CSV/JSON)", body: "Org admins can export the full audit log to CSV or JSON for SIEM ingestion or compliance reporting.", tags: ["audit", "compliance"] },
      { type: "improvement", breaking: false, title: "Build cancellation is now instant", body: "Builds can now be cancelled with under 500 ms latency regardless of how many parallel steps are running, via a new ack-and-drain shutdown protocol in the runner agent.", tags: ["performance"] }
    ]
  },
  {
    version: "v1.8.0",
    date: "2024-09-01",
    highlight: false,
    entries: [
      { type: "feature", breaking: false, title: "BYOC runner registration", body: "Bring Your Own Cloud: register any Docker-capable host as a Forge CI runner with a single `forge agent register` command. Runs securely inside your VPC — no inbound firewall rules required.", tags: ["byoc", "runners"] },
      { type: "fix", breaking: false, title: "Windows runner PATH handling", body: "Fixed cases where Windows runners would not inherit the system PATH correctly, causing tool-not-found errors for `npm`, `cargo`, and `dotnet`.", tags: ["runners", "windows", "bugfix"] }
    ]
  },
  {
    version: "v1.7.0",
    date: "2024-08-01",
    highlight: false,
    entries: [
      { type: "feature", breaking: false, title: "Content-addressed caching v2", body: "The caching layer now uses Blake3 content-addressing with split-key invalidation, lifting average hit rates from ~70% to ~89% across all workloads.", tags: ["cache", "performance"] },
      { type: "improvement", breaking: false, title: "Pipeline editor with YAML validation", body: "The in-app pipeline editor now validates your YAML against the v2 schema in real time, with inline error messages and autocomplete for known step types.", tags: ["ui", "pipeline"] }
    ]
  }
];

const $$Changelog = createComponent(($$result, $$props, $$slots) => {
  const typeConfig = {
    feature: { cls: "badge-accent", icon: "\u26A1", label: "Feature" },
    improvement: { cls: "badge-info", icon: "\u2191", label: "Improvement" },
    fix: { cls: "badge-success", icon: "\u2713", label: "Fix" },
    breaking: { cls: "badge-danger", icon: "\u26A0", label: "Breaking" }
  };
  return renderTemplate`${renderComponent($$result, "MarketingLayout", $$MarketingLayout, { "title": "Changelog \u2014 Forge CI", "description": "Every new feature, improvement, fix, and breaking change across every release of Forge CI." }, { "default": ($$result2) => renderTemplate`  ${maybeRenderHead()}<section class="relative pt-32 pb-12 overflow-hidden"> <div class="absolute inset-0 bg-grid-brut opacity-20 pointer-events-none"></div> <div class="container-forge relative z-10"> <div class="section-label mb-5">Changelog</div> <h1 class="section-title mb-4">Every change, documented.</h1> <p class="section-subtitle max-w-xl">
New features, performance improvements, bug fixes, and breaking changes — indexed for every release.
</p> </div> </section> <div class="container-forge max-w-3xl pb-24"> <!-- Version legend --> <div class="flex flex-wrap gap-3 mb-12 pb-8 border-b-2" style="border-color:#2C2C2C"> ${Object.entries(typeConfig).map(([, cfg]) => renderTemplate`<div class="flex items-center gap-2 text-xs font-mono" style="color:#AAAAAA"> <span${addAttribute(`badge ${cfg.cls}`, "class")}>${cfg.icon} ${cfg.label}</span> </div>`)} </div> <!-- Entries --> <div class="space-y-0"> ${CHANGELOG.map((release, ri) => renderTemplate`<div${addAttribute(release.version, "id")} class="relative"> <!-- Timeline connector --> ${ri < CHANGELOG.length - 1 && renderTemplate`<div class="absolute left-[103px] top-0 bottom-0 w-px" style="background:#2C2C2C;transform:translateX(-50%)"></div>`} <div class="grid grid-cols-[96px_1fr] gap-8 pb-14"> <!-- Date column --> <div class="pt-3 text-right"> <div class="text-xs font-mono leading-relaxed" style="color:#666666"> ${release.date.slice(0, 7)} </div> </div> <!-- Content column --> <div class="relative"> <!-- Version bullet --> <div class="absolute -left-[42px] top-3 w-4 h-4 flex items-center justify-center"${addAttribute(`background:${release.highlight ? "#FFEE00" : "#1A1A1A"};border:2px solid ${release.highlight ? "#FFEE00" : "#2C2C2C"}`, "style")}></div> <!-- Header --> <div class="flex items-center gap-3 mb-5"> <div class="text-xl font-bold" style="font-family:'Space Grotesk';color:#F0F0F0;letter-spacing:-0.03em"> ${release.version} </div> ${release.highlight && renderTemplate`<span class="badge-accent">Highlight</span>`} </div> <!-- Entry cards --> <div class="space-y-3"> ${release.entries.map((entry) => {
    const cfg = typeConfig[entry.type];
    return renderTemplate`<div class="card p-5"${addAttribute(entry.breaking ? "border-color:rgba(255,51,51,0.4)" : "", "style")}> <div class="flex items-start gap-3 mb-2"> <span${addAttribute(`badge ${cfg.cls} flex-shrink-0`, "class")}>${cfg.icon} ${cfg.label}</span> ${entry.breaking && renderTemplate`<span class="badge-danger flex-shrink-0">Breaking</span>`} ${entry.tags.slice(0, 2).map((t) => renderTemplate`<span class="badge-neutral flex-shrink-0">${t}</span>`)} </div> <div class="text-sm font-semibold mb-2" style="color:#F0F0F0;font-family:'Space Grotesk'">${entry.title}</div> <p class="text-xs leading-relaxed" style="color:#AAAAAA">${entry.body}</p> </div>`;
  })} </div> </div> </div> </div>`)} </div> <!-- Subscribe to updates --> <div class="card p-8 text-center mt-8 border-t-2" style="border-color:#2C2C2C"> <div class="section-label mb-4 justify-center">Stay updated</div> <h2 class="text-xl font-bold mb-3" style="font-family:'Space Grotesk';color:#F0F0F0">Get release notifications</h2> <p class="text-sm mb-5" style="color:#666666">Subscribe to the RSS feed or follow us on Twitter.</p> <div class="flex flex-col sm:flex-row gap-3 justify-center"> <a href="/rss/changelog.xml" class="btn-secondary btn-md"> <svg class="w-4 h-4" fill="currentColor" viewBox="0 0 20 20"> <path d="M5 3a1 1 0 000 2c5.523 0 10 4.477 10 10a1 1 0 102 0C17 8.373 11.627 3 5 3zm.001 5.924a1 1 0 10-.002 2 5.076 5.076 0 015.077 5.077 1 1 0 102 0 7.077 7.077 0 00-7.075-7.077zM4 15a2 2 0 114 0 2 2 0 01-4 0z"></path> </svg>
RSS feed
</a> <a href="/auth/signup" class="btn-primary btn-md">Start free →</a> </div> </div> </div> ` })}`;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/changelog.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/changelog.astro";
const $$url = "/changelog";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Changelog,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

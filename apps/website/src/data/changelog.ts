// ============================================================
// FORGE CI — Changelog Mock Data
// ============================================================
import type { ChangelogEntry } from '../types/index.ts';

export const CHANGELOG: ChangelogEntry[] = [
  {
    version: 'v2.4.0',
    date: '2025-04-10',
    highlight: true,
    entries: [
      { type: 'feature', breaking: false, title: 'Sherlock AI is now Generally Available', body: 'After 6 months of beta with 2,000+ teams, Sherlock AI exits beta on all Team and Enterprise plans. Root-cause analysis, one-click fix PRs, and flaky-test detection are now production-grade.', tags: ['ai', 'sherlock'] },
      { type: 'feature', breaking: false, title: 'BYOC ARM64 runner support', body: 'Register AWS Graviton3 and Apple Silicon hosts as BYOC runners. Graviton3 delivers ~40% better price-performance for most Node.js and Go workloads.', tags: ['byoc', 'runners', 'arm64'] },
      { type: 'feature', breaking: false, title: 'OTel span export', body: 'Build steps now export OpenTelemetry spans to any OTLP endpoint. Wire Forge CI directly into Grafana Tempo, Honeycomb, or Datadog APM.', tags: ['observability', 'otel'] },
      { type: 'improvement', breaking: false, title: 'Waterfall view in build detail', body: 'The build detail page now shows a Gantt-style waterfall chart for all steps, making it easy to spot serial bottlenecks at a glance.', tags: ['ui', 'observability'] },
      { type: 'fix', breaking: false, title: 'Cache invalidation on runner OS upgrade', body: 'Fixed a bug where upgrading the runner OS version did not invalidate the node_modules cache, causing intermittent failures.', tags: ['cache', 'bugfix'] },
    ],
  },
  {
    version: 'v2.3.2',
    date: '2025-03-28',
    highlight: false,
    entries: [
      { type: 'fix', breaking: false, title: 'OIDC token refresh race condition', body: 'Fixed a race condition in the OIDC token refresh path that caused ~0.1% of builds to fail with an invalid_grant error on long-running jobs.', tags: ['security', 'oidc'] },
      { type: 'fix', breaking: false, title: 'Matrix build progress reporting', body: 'Matrix build steps now correctly aggregate individual job statuses in real time, preventing false "passed" states during partially failed matrices.', tags: ['matrix', 'bugfix'] },
      { type: 'improvement', breaking: false, title: 'P99 build duration surface in insights', body: 'The Insights page now surfaces P99 build duration per project over the last 30 days with a percentile comparison chart.', tags: ['insights', 'metrics'] },
    ],
  },
  {
    version: 'v2.3.1',
    date: '2025-03-14',
    highlight: false,
    entries: [
      { type: 'fix', breaking: false, title: 'Slack notification formatting', body: 'Fixed broken block-kit formatting in Slack build notifications when commit messages contain backticks.', tags: ['integrations', 'slack'] },
      { type: 'improvement', breaking: false, title: 'Sandbox idle timeout configurable', body: 'Sandbox sessions can now configure an idle timeout (default 30 min, max 8 h) to reduce unnecessary compute costs.', tags: ['sandboxes'] },
    ],
  },
  {
    version: 'v2.3.0',
    date: '2025-03-01',
    highlight: false,
    entries: [
      { type: 'feature', breaking: false, title: 'Heatmap build activity view', body: 'A day-of-week × hour-of-day heatmap is now available in Insights, showing where build load clusters to help with runner capacity planning.', tags: ['insights', 'observability'] },
      { type: 'feature', breaking: false, title: 'SCIM auto-provisioning (Enterprise)', body: 'Enterprise orgs on Okta, Azure AD, and OneLogin can now auto-provision and deprovision members via SCIM 2.0.', tags: ['enterprise', 'sso', 'scim'] },
      { type: 'improvement', breaking: false, title: 'Runner queue fairness', body: 'Implemented weighted fair-queuing for shared runners to prevent large orgs from monopolising capacity during peak periods.', tags: ['runners', 'performance'] },
      { type: 'breaking', breaking: true, title: 'Deprecated `forge.yml` in favour of `.forge/pipeline.yml`', body: 'Support for the legacy root-level `forge.yml` config file has been removed. Migrate to `.forge/pipeline.yml`. The migration guide is in the docs.', tags: ['pipeline', 'config'] },
    ],
  },
  {
    version: 'v2.2.0',
    date: '2025-02-12',
    highlight: false,
    entries: [
      { type: 'feature', breaking: false, title: 'OIDC secret injection (AWS, GCP, Azure)', body: 'Configure AWS assume-role, GCP workload identity, or Azure federated credential directly in your pipeline to eliminate all long-lived cloud credentials.', tags: ['security', 'oidc', 'aws'] },
      { type: 'feature', breaking: false, title: 'Flame graph in build profiling', body: 'Build steps that enable profiling export a flame graph viewable directly in the build detail UI, powered by a D3 icicle chart renderer.', tags: ['observability', 'profiling'] },
      { type: 'fix', breaking: false, title: 'macOS M2 runner SSH agent forwarding', body: 'Fixed SSH agent forwarding in macOS M2 runners that prevented private repo clones in submodule steps.', tags: ['runners', 'macos', 'bugfix'] },
    ],
  },
  {
    version: 'v2.1.0',
    date: '2025-01-20',
    highlight: false,
    entries: [
      { type: 'feature', breaking: false, title: 'Log stream filter by level and regex', body: 'The live log viewer now supports filtering by severity level (info / warn / error / debug) and regex search with highlighted matches.', tags: ['ui', 'logs'] },
      { type: 'feature', breaking: false, title: 'Artifact expiry policy', body: 'Org admins can now set a global artifact expiry policy (7 / 30 / 90 days, or never), overridable per-project.', tags: ['artifacts'] },
      { type: 'improvement', breaking: false, title: 'Faster cold-start: pre-warmed runner pool', body: 'Linux x64 medium runners now maintain a warm pool of 50 pre-initialised VMs. P99 queue time dropped from 12 s to under 1 s.', tags: ['runners', 'performance'] },
    ],
  },
  {
    version: 'v2.0.0',
    date: '2024-12-01',
    highlight: true,
    entries: [
      { type: 'breaking', breaking: true, title: 'Pipeline YAML v2 format', body: 'The pipeline config schema has been redesigned for clarity and power. Use `forge migrate` to auto-convert v1 configs. v1 support ends 2025-06-01.', tags: ['pipeline', 'breaking'] },
      { type: 'feature', breaking: false, title: 'Ory identity stack (Kratos, Hydra, Keto)', body: 'Authentication, OAuth, and RBAC are now powered by Ory — enabling WebAuthn passkeys, TOTP, hardware keys, and fine-grained permission policies.', tags: ['auth', 'ory', 'security'] },
      { type: 'feature', breaking: false, title: 'Image registry built-in', body: 'Every org gets a private OCI-compliant container registry with vulnerability scanning, tag retention policies, and pull-through caching for Docker Hub.', tags: ['registry', 'containers'] },
      { type: 'feature', breaking: false, title: 'Sandboxes', body: 'Spin up an ephemeral dev environment from any build — port-forwarded, publicly accessible, and auto-expired. Perfect for debugging and demos.', tags: ['sandboxes'] },
      { type: 'feature', breaking: false, title: 'Marketplace plugin system', body: 'Install official and community plugins for Slack, Datadog, Snyk, Jira, Linear, PagerDuty, Vault, and more from the Marketplace tab.', tags: ['marketplace', 'plugins'] },
    ],
  },
  {
    version: 'v1.9.0',
    date: '2024-10-15',
    highlight: false,
    entries: [
      { type: 'feature', breaking: false, title: 'Audit log export (CSV/JSON)', body: 'Org admins can export the full audit log to CSV or JSON for SIEM ingestion or compliance reporting.', tags: ['audit', 'compliance'] },
      { type: 'improvement', breaking: false, title: 'Build cancellation is now instant', body: 'Builds can now be cancelled with under 500 ms latency regardless of how many parallel steps are running, via a new ack-and-drain shutdown protocol in the runner agent.', tags: ['performance'] },
    ],
  },
  {
    version: 'v1.8.0',
    date: '2024-09-01',
    highlight: false,
    entries: [
      { type: 'feature', breaking: false, title: 'BYOC runner registration', body: 'Bring Your Own Cloud: register any Docker-capable host as a Forge CI runner with a single `forge agent register` command. Runs securely inside your VPC — no inbound firewall rules required.', tags: ['byoc', 'runners'] },
      { type: 'fix', breaking: false, title: 'Windows runner PATH handling', body: 'Fixed cases where Windows runners would not inherit the system PATH correctly, causing tool-not-found errors for `npm`, `cargo`, and `dotnet`.', tags: ['runners', 'windows', 'bugfix'] },
    ],
  },
  {
    version: 'v1.7.0',
    date: '2024-08-01',
    highlight: false,
    entries: [
      { type: 'feature', breaking: false, title: 'Content-addressed caching v2', body: 'The caching layer now uses Blake3 content-addressing with split-key invalidation, lifting average hit rates from ~70% to ~89% across all workloads.', tags: ['cache', 'performance'] },
      { type: 'improvement', breaking: false, title: 'Pipeline editor with YAML validation', body: 'The in-app pipeline editor now validates your YAML against the v2 schema in real time, with inline error messages and autocomplete for known step types.', tags: ['ui', 'pipeline'] },
    ],
  },
];

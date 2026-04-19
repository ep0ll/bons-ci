function seededRand(seed) {
  let s = seed;
  return () => {
    s = s * 1664525 + 1013904223 & 4294967295;
    return (s >>> 0) / 4294967295;
  };
}
function genSeries(days, base, variance, seed = 42) {
  const rand = seededRand(seed);
  const result = [];
  let val = base;
  const now = Date.now();
  for (let i = days; i >= 0; i--) {
    val = Math.max(0, val + (rand() - 0.48) * variance);
    const ts = new Date(now - i * 864e5).toISOString();
    result.push({ ts, value: Math.round(val * 10) / 10 });
  }
  return result;
}
const MOCK_ORG = {
  name: "Acme Corp",
  plan: "team",
  build_minutes_used: 62450,
  build_minutes_limit: 1e5,
  cache_used_bytes: 36723e6,
  cache_limit_bytes: 107374182400,
  egress_used_bytes: 266338304e3};
const MOCK_BUILDS = [
  {
    id: "b1847",
    number: 1847,
    status: "success",
    project_id: "proj_api",
    project_name: "api-service",
    org_id: "org_acme",
    namespace: "acme-corp",
    branch: "main",
    commit_sha: "a4f9c2be49a1b28c3f9e8d4a0bc7e21f45c6d382",
    commit_short: "a4f9c2b",
    commit_message: "feat: add rate limiting to auth endpoints",
    author: { name: "Ada Lovelace", email: "ada@acme-corp.io", username: "ada-l", avatar_url: void 0 },
    trigger: "push",
    pipeline_file: ".forge/pipeline.yml",
    runner_type: "linux-x64-medium",
    runner_region: "us-east-1",
    parallelism: 1,
    credits_used: 12,
    cache_hit: true,
    cache_key: "node-modules",
    queued_at: "2025-04-16T11:58:00Z",
    started_at: "2025-04-16T11:58:02Z",
    finished_at: "2025-04-16T12:00:02Z",
    duration_ms: 12e4,
    steps: [
      { id: "s1", name: "Setup runner", status: "success", duration_ms: 800, started_at: "2025-04-16T11:58:02Z", finished_at: "2025-04-16T11:58:03Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 5, cpu_peak: 12, mem_avg_mb: 128, mem_peak_mb: 256, net_in_mb: 0.1, net_out_mb: 0.1, disk_read_mb: 50, disk_write_mb: 10 } },
      { id: "s2", name: "Restore cache", status: "success", duration_ms: 820, started_at: "2025-04-16T11:58:03Z", finished_at: "2025-04-16T11:58:04Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 8, cpu_peak: 22, mem_avg_mb: 256, mem_peak_mb: 512, net_in_mb: 1.2, net_out_mb: 0.1, disk_read_mb: 1200, disk_write_mb: 0 } },
      { id: "s3", name: "Install", status: "success", duration_ms: 2400, started_at: "2025-04-16T11:58:04Z", finished_at: "2025-04-16T11:58:06Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 45, cpu_peak: 82, mem_avg_mb: 512, mem_peak_mb: 768, net_in_mb: 0.5, net_out_mb: 0.2, disk_read_mb: 200, disk_write_mb: 150 } },
      { id: "s4", name: "Lint", status: "success", duration_ms: 4100, started_at: "2025-04-16T11:58:06Z", finished_at: "2025-04-16T11:58:10Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 78, cpu_peak: 95, mem_avg_mb: 768, mem_peak_mb: 1024, net_in_mb: 0, net_out_mb: 0, disk_read_mb: 80, disk_write_mb: 5 } },
      { id: "s5", name: "Test", status: "success", duration_ms: 12400, started_at: "2025-04-16T11:58:10Z", finished_at: "2025-04-16T11:58:22Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 92, cpu_peak: 98, mem_avg_mb: 1024, mem_peak_mb: 1536, net_in_mb: 0.2, net_out_mb: 0.1, disk_read_mb: 120, disk_write_mb: 40 } },
      { id: "s6", name: "Build", status: "success", duration_ms: 8100, started_at: "2025-04-16T11:58:22Z", finished_at: "2025-04-16T11:58:30Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 88, cpu_peak: 97, mem_avg_mb: 768, mem_peak_mb: 1024, net_in_mb: 0.1, net_out_mb: 0.1, disk_read_mb: 200, disk_write_mb: 180 } },
      { id: "s7", name: "Push image", status: "success", duration_ms: 4200, started_at: "2025-04-16T11:58:30Z", finished_at: "2025-04-16T11:58:34Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 22, cpu_peak: 35, mem_avg_mb: 256, mem_peak_mb: 384, net_in_mb: 0.1, net_out_mb: 380, disk_read_mb: 380, disk_write_mb: 10 } }
    ],
    artifacts: [
      { id: "art1", name: "coverage-report.html", path: "/coverage/index.html", size_bytes: 21e5, content_type: "text/html", build_id: "b1847", project_name: "api-service", branch: "main", created_at: "2025-04-16T12:00:00Z", expires_at: "2025-07-16T12:00:00Z", download_count: 0 },
      { id: "art2", name: "test-results.xml", path: "/junit.xml", size_bytes: 48e3, content_type: "application/xml", build_id: "b1847", project_name: "api-service", branch: "main", created_at: "2025-04-16T12:00:00Z", expires_at: "2025-07-16T12:00:00Z", download_count: 0 }
    ],
    test_results: { total: 1847, passed: 1847, failed: 0, skipped: 0, duration_ms: 12400, coverage_pct: 87.4, flaky_tests: [] },
    resources: { cpu_avg: 62, cpu_peak: 98, mem_avg_mb: 650, mem_peak_mb: 1536, net_in_mb: 2.2, net_out_mb: 380.5, disk_read_mb: 2230, disk_write_mb: 395 }
  },
  {
    id: "b1846",
    number: 1846,
    status: "running",
    project_id: "proj_web",
    project_name: "web-app",
    org_id: "org_acme",
    namespace: "acme-corp",
    branch: "feat/dashboard",
    commit_sha: "3d8e1a0f49a1b28c3f9e8d4a0bc7e21f45c6d382",
    commit_short: "3d8e1a0",
    commit_message: "refactor: migrate charts to recharts",
    author: { name: "Tomás Reyes", email: "tomas@acme-corp.io", username: "tomas-r" },
    trigger: "push",
    pipeline_file: ".forge/pipeline.yml",
    runner_type: "linux-x64-medium",
    runner_region: "us-east-1",
    parallelism: 1,
    credits_used: 6,
    cache_hit: true,
    queued_at: "2025-04-16T11:45:00Z",
    started_at: "2025-04-16T11:45:02Z",
    duration_ms: void 0,
    steps: [
      { id: "s1", name: "Setup runner", status: "success", duration_ms: 800, started_at: "2025-04-16T11:45:02Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 5, cpu_peak: 12, mem_avg_mb: 128, mem_peak_mb: 256, net_in_mb: 0.1, net_out_mb: 0.1, disk_read_mb: 50, disk_write_mb: 10 } },
      { id: "s2", name: "Restore cache", status: "success", duration_ms: 320, started_at: "2025-04-16T11:45:03Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 8, cpu_peak: 22, mem_avg_mb: 256, mem_peak_mb: 512, net_in_mb: 1.2, net_out_mb: 0.1, disk_read_mb: 900, disk_write_mb: 0 } },
      { id: "s3", name: "Install", status: "success", duration_ms: 2100, started_at: "2025-04-16T11:45:04Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 45, cpu_peak: 82, mem_avg_mb: 512, mem_peak_mb: 768, net_in_mb: 0.5, net_out_mb: 0.2, disk_read_mb: 200, disk_write_mb: 150 } },
      { id: "s4", name: "Test", status: "running", duration_ms: 0, started_at: "2025-04-16T11:45:10Z", log_lines: [], resources: { cpu_avg: 88, cpu_peak: 95, mem_avg_mb: 900, mem_peak_mb: 1024, net_in_mb: 0.2, net_out_mb: 0.1, disk_read_mb: 120, disk_write_mb: 40 } },
      { id: "s5", name: "Build", status: "queued", duration_ms: 0, started_at: "", log_lines: [] }
    ],
    artifacts: []
  },
  {
    id: "b1845",
    number: 1845,
    status: "failed",
    project_id: "proj_api",
    project_name: "api-service",
    org_id: "org_acme",
    namespace: "acme-corp",
    branch: "fix/null-pointer",
    commit_sha: "c1b2f3de49a1b28c3f9e8d4a0bc7e21f45c6d382",
    commit_short: "c1b2f3d",
    commit_message: "fix: handle null user object in session middleware",
    author: { name: "Priya Nair", email: "priya@acme-corp.io", username: "priya-n" },
    trigger: "pr",
    pr_number: 291,
    pipeline_file: ".forge/pipeline.yml",
    runner_type: "linux-x64-medium",
    runner_region: "us-east-1",
    parallelism: 1,
    credits_used: 18,
    cache_hit: false,
    queued_at: "2025-04-16T10:22:14Z",
    started_at: "2025-04-16T10:22:16Z",
    finished_at: "2025-04-16T10:24:27Z",
    duration_ms: 131e3,
    steps: [
      { id: "s1", name: "Setup runner", status: "success", duration_ms: 800, started_at: "2025-04-16T10:22:16Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 5, cpu_peak: 12, mem_avg_mb: 128, mem_peak_mb: 256, net_in_mb: 0.1, net_out_mb: 0.1, disk_read_mb: 50, disk_write_mb: 10 } },
      { id: "s2", name: "Restore cache", status: "success", duration_ms: 300, started_at: "2025-04-16T10:22:17Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 8, cpu_peak: 22, mem_avg_mb: 256, mem_peak_mb: 512, net_in_mb: 0.1, net_out_mb: 0.1, disk_read_mb: 100, disk_write_mb: 0 } },
      { id: "s3", name: "Install", status: "success", duration_ms: 24e3, started_at: "2025-04-16T10:22:17Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 45, cpu_peak: 82, mem_avg_mb: 512, mem_peak_mb: 768, net_in_mb: 180, net_out_mb: 0.2, disk_read_mb: 200, disk_write_mb: 150 } },
      { id: "s4", name: "Lint", status: "success", duration_ms: 4100, started_at: "2025-04-16T10:22:41Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 78, cpu_peak: 95, mem_avg_mb: 768, mem_peak_mb: 1024, net_in_mb: 0, net_out_mb: 0, disk_read_mb: 80, disk_write_mb: 5 } },
      { id: "s5", name: "Build", status: "success", duration_ms: 18200, started_at: "2025-04-16T10:22:45Z", exit_code: 0, log_lines: [], resources: { cpu_avg: 88, cpu_peak: 97, mem_avg_mb: 768, mem_peak_mb: 1024, net_in_mb: 0.1, net_out_mb: 0.1, disk_read_mb: 200, disk_write_mb: 180 } },
      { id: "s6", name: "Test (unit)", status: "failed", duration_ms: 14200, started_at: "2025-04-16T10:23:03Z", finished_at: "2025-04-16T10:24:27Z", exit_code: 1, log_lines: [
        { t: 1744797781e3, stream: "stdout", text: "PASS src/services/__tests__/UserService.test.ts", level: "info" },
        { t: 1744797783e3, stream: "stdout", text: "PASS src/services/__tests__/AuthService.test.ts", level: "info" },
        { t: 1744797785e3, stream: "stderr", text: "FAIL src/middleware/__tests__/AuthMiddleware.test.ts", level: "error" },
        { t: 1744797785100, stream: "stderr", text: "  ✕ AuthMiddleware.verify should handle unauthenticated request (8ms)", level: "error" },
        { t: 1744797785200, stream: "stderr", text: "    TypeError: Cannot read properties of null (reading 'id')", level: "error" },
        { t: 1744797785300, stream: "stderr", text: "      at AuthMiddleware.verify (src/middleware/AuthMiddleware.ts:34:22)", level: "error" },
        { t: 1744797785400, stream: "stderr", text: "      at Object.<anonymous> (src/middleware/__tests__/AuthMiddleware.test.ts:47:5)", level: "error" },
        { t: 1744797787e3, stream: "stderr", text: "Tests: 1 failed, 1846 passed, 1847 total", level: "error" },
        { t: 1744797787100, stream: "stderr", text: "Coverage: Statements 87.4% Branches 82.1% Functions 91.0% Lines 87.2%", level: "error" }
      ], resources: { cpu_avg: 92, cpu_peak: 98, mem_avg_mb: 1024, mem_peak_mb: 1536, net_in_mb: 0.2, net_out_mb: 0.1, disk_read_mb: 120, disk_write_mb: 40 } },
      { id: "s7", name: "Push image", status: "skipped", duration_ms: 0, started_at: "", log_lines: [] }
    ],
    artifacts: [
      { id: "art3", name: "test-results.xml", path: "/junit.xml", size_bytes: 48e3, content_type: "application/xml", build_id: "b1845", project_name: "api-service", branch: "fix/null-pointer", created_at: "2025-04-16T10:24:27Z", expires_at: "2025-07-16T10:24:27Z", download_count: 2 }
    ],
    test_results: { total: 1847, passed: 1846, failed: 1, skipped: 0, duration_ms: 14200, coverage_pct: 87.4, flaky_tests: [] },
    resources: { cpu_avg: 58, cpu_peak: 98, mem_avg_mb: 680, mem_peak_mb: 1536, net_in_mb: 180.5, net_out_mb: 0.5, disk_read_mb: 750, disk_write_mb: 385 }
  }
];
const MOCK_METRICS = {
  success_rate: 97.3,
  avg_duration_ms: 98e3,
  cache_hit_rate: 89.2,
  series: {
    build_count: genSeries(30, 64, 20, 1),
    success_rate: genSeries(30, 97, 2, 2),
    avg_duration: genSeries(30, 98, 12, 3),
    cache_hit_rate: genSeries(30, 89, 3, 4),
    cpu_usage: genSeries(30, 68, 12, 5),
    mem_usage_mb: genSeries(30, 6400, 800, 6),
    net_in_mbps: genSeries(30, 340, 80, 7),
    net_out_mbps: genSeries(30, 180, 40, 8),
    egress_gb: genSeries(30, 8.3, 2, 9),
    build_minutes: genSeries(30, 2082, 400, 10)
  },
  by_project: [
    { project_id: "proj_api", project_name: "api-service", build_count: 842, success_rate: 97.4, avg_duration_ms: 84e3, p95_duration_ms: 148e3, build_minutes: 19724, cache_hit_rate: 98.6, cost_pct: 31.5 },
    { project_id: "proj_web", project_name: "web-app", build_count: 634, success_rate: 98.1, avg_duration_ms: 78e3, p95_duration_ms: 12e4, build_minutes: 13782, cache_hit_rate: 97.2, cost_pct: 22 },
    { project_id: "proj_mob", project_name: "mobile", build_count: 241, success_rate: 95.8, avg_duration_ms: 492e3, p95_duration_ms: 68e4, build_minutes: 19722, cache_hit_rate: 93.1, cost_pct: 31.5 },
    { project_id: "proj_inf", project_name: "infra", build_count: 88, success_rate: 99.9, avg_duration_ms: 224e3, p95_duration_ms: 31e4, build_minutes: 5502, cache_hit_rate: 0, cost_pct: 8.8 },
    { project_id: "proj_doc", project_name: "docs", build_count: 112, success_rate: 100, avg_duration_ms: 44e3, p95_duration_ms: 55e3, build_minutes: 1386, cache_hit_rate: 92.4, cost_pct: 2.2 }
  ],
  by_member: [
    { user_id: "u1", user_name: "Ada Lovelace", initials: "AL", build_count: 312, success_rate: 98.7, avg_duration_ms: 88e3 },
    { user_id: "u2", user_name: "Tomás Reyes", initials: "TR", build_count: 248, success_rate: 97.2, avg_duration_ms: 92e3 },
    { user_id: "u3", user_name: "Priya Nair", initials: "PN", build_count: 196, success_rate: 94.9, avg_duration_ms: 104e3 },
    { user_id: "u4", user_name: "Olu Adeyemi", initials: "OA", build_count: 88, success_rate: 99.9, avg_duration_ms: 221e3 },
    { user_id: "u5", user_name: "Sara Johansson", initials: "SJ", build_count: 241, success_rate: 95.8, avg_duration_ms: 492e3 }
  ],
  waterfall_percentiles: [
    { project: "api-service", p50: 84, p75: 118, p90: 148, p95: 196, p99: 420, max: 720 },
    { project: "web-app", p50: 78, p75: 98, p90: 120, p95: 156, p99: 312, max: 540 },
    { project: "mobile", p50: 492, p75: 580, p90: 640, p95: 680, p99: 840, max: 1200 },
    { project: "infra", p50: 224, p75: 268, p90: 296, p95: 310, p99: 480, max: 680 },
    { project: "docs", p50: 44, p75: 50, p90: 52, p95: 55, p99: 72, max: 110 }
  ]
};
const MOCK_SYSTEM_RESOURCES = {
  ts: (/* @__PURE__ */ new Date()).toISOString(),
  cpu_pct: 68,
  mem_pct: 54,
  net_in_mbps: 340,
  net_out_mbps: 180,
  disk_read_mbps: 420,
  disk_write_mbps: 220,
  active_runners: 12,
  queued_jobs: 3};
const MOCK_CACHE_KEYS = [
  { id: "ck1", key: "node-modules", project_id: "proj_api", project_name: "api-service", size_bytes: 124e7, hit_count: 842, miss_count: 12, hit_rate: 98.6, last_hit_at: "2025-04-16T12:00:00Z", ttl_days: 7, created_at: "2024-01-12T08:00:00Z" },
  { id: "ck2", key: "node-modules", project_id: "proj_web", project_name: "web-app", size_bytes: 98e7, hit_count: 634, miss_count: 8, hit_rate: 98.7, last_hit_at: "2025-04-16T11:47:00Z", ttl_days: 7, created_at: "2024-01-15T08:00:00Z" },
  { id: "ck3", key: "docker-layers", project_id: "proj_api", project_name: "api-service", size_bytes: 24e8, hit_count: 620, miss_count: 44, hit_rate: 93.4, last_hit_at: "2025-04-16T12:00:00Z", ttl_days: 14, created_at: "2024-01-12T08:00:00Z" },
  { id: "ck4", key: "pip-packages", project_id: "proj_doc", project_name: "ml-runner", size_bytes: 44e7, hit_count: 88, miss_count: 4, hit_rate: 95.7, last_hit_at: "2025-04-16T09:00:00Z", ttl_days: 30, created_at: "2024-02-15T08:00:00Z" },
  { id: "ck5", key: "go-modules", project_id: "proj_inf", project_name: "infra", size_bytes: 32e7, hit_count: 156, miss_count: 6, hit_rate: 96.3, last_hit_at: "2025-04-16T11:00:00Z", ttl_days: 7, created_at: "2024-01-20T08:00:00Z" },
  { id: "ck6", key: "cocoapods", project_id: "proj_mob", project_name: "mobile", size_bytes: 88e7, hit_count: 241, miss_count: 18, hit_rate: 93.1, last_hit_at: "2025-04-16T10:30:00Z", ttl_days: 7, created_at: "2024-02-01T08:00:00Z" },
  { id: "ck7", key: "gradle", project_id: "proj_mob", project_name: "mobile", size_bytes: 66e7, hit_count: 241, miss_count: 14, hit_rate: 94.5, last_hit_at: "2025-04-16T10:30:00Z", ttl_days: 7, created_at: "2024-02-01T08:00:00Z" },
  { id: "ck8", key: "sccache", project_id: "proj_inf", project_name: "worker", size_bytes: 22e7, hit_count: 88, miss_count: 22, hit_rate: 80, last_hit_at: "2025-04-16T09:00:00Z", ttl_days: 14, created_at: "2024-03-01T08:00:00Z" }
];
const MOCK_AUDIT_LOG = [
  { id: "al1", actor: "Ada Lovelace", actor_id: "u1", action: "secret.created", resource: "NPM_TOKEN", ip: "203.0.113.1", user_agent: "Firefox/124", timestamp: "2025-04-16T11:58:00Z", severity: "info" },
  { id: "al2", actor: "Tomás Reyes", actor_id: "u2", action: "member.role_changed", resource: "priya→admin", ip: "203.0.113.2", user_agent: "Chrome/124", timestamp: "2025-04-16T10:00:00Z", severity: "warning" },
  { id: "al3", actor: "Ada Lovelace", actor_id: "u1", action: "sso.configured", resource: "Okta SAML 2.0", ip: "203.0.113.1", user_agent: "Firefox/124", timestamp: "2025-04-16T09:00:00Z", severity: "info" },
  { id: "al4", actor: "System", actor_id: "", action: "build.triggered", resource: "api-service #1847", ip: "—", user_agent: "—", timestamp: "2025-04-16T11:58:00Z", severity: "info" },
  { id: "al5", actor: "Olu Adeyemi", actor_id: "u4", action: "token.created", resource: "Terraform integration", ip: "203.0.113.4", user_agent: "Safari/17", timestamp: "2025-04-16T08:00:00Z", severity: "info" },
  { id: "al6", actor: "Sara Johansson", actor_id: "u5", action: "member.removed", resource: "contractor@ext.com", ip: "203.0.113.5", user_agent: "Chrome/124", timestamp: "2025-04-15T14:00:00Z", severity: "warning" },
  { id: "al7", actor: "Ada Lovelace", actor_id: "u1", action: "billing.plan_changed", resource: "Pro→Team 20 seats", ip: "203.0.113.1", user_agent: "Firefox/124", timestamp: "2025-04-12T10:00:00Z", severity: "info" },
  { id: "al8", actor: "Priya Nair", actor_id: "u3", action: "secret.deleted", resource: "OLD_AWS_KEY", ip: "203.0.113.3", user_agent: "Chrome/124", timestamp: "2025-04-13T09:00:00Z", severity: "warning" }
];
const MOCK_SHERLOCK_ANALYSES = [
  { id: "sh1", build_id: "b1845", project_name: "api-service", branch: "fix/null-pointer", failed_step: "Test (unit)", root_cause: "Null-check missing in AuthMiddleware.ts:34 — new test exposes unauthenticated path that calls user.id on null", confidence: 97, analyzed_at: "2025-04-16T10:24:30Z", status: "fix_suggested", fix_diff: "- if (user.id !== req.params.userId) {\n+ if (!user || user.id !== req.params.userId) {", patterns_detected: ["null-dereference"] }
];
const MOCK_SHERLOCK_PATTERNS = [
  { id: "sp1", title: "Flaky test cluster detected", description: "AuthMiddleware.test.ts has failed intermittently in 4 of the last 10 runs on main.", severity: "warning", project_name: "api-service", occurrence_count: 4, suggestion: "Add retry logic or isolate test environment for this suite.", detected_at: "2025-04-15T08:00:00Z" },
  { id: "sp2", title: "Cache miss rate increasing", description: "sccache hit rate dropped from 94% to 80% over the last 7 days in the worker project.", severity: "warning", project_name: "worker", suggestion: "Check if Rust toolchain version changed — that invalidates the entire sccache layer.", detected_at: "2025-04-14T08:00:00Z" },
  { id: "sp3", title: "Docker build time regression", description: "api-service Docker build step increased from 18s to 44s average over the last 5 days.", severity: "info", project_name: "api-service", suggestion: "A new COPY . . instruction was moved before RUN npm ci — re-order to maximise layer reuse.", detected_at: "2025-04-13T08:00:00Z" }
];
const MOCK_BLOG_POSTS = [
  {
    slug: "sherlock-ga",
    title: "Sherlock AI is now generally available",
    excerpt: "After 6 months of beta with 2,000+ teams, Sherlock — our AI build intelligence agent — is GA on all Team and Enterprise plans. Here's what we learned.",
    body: "",
    category: "Product",
    author: { name: "Ada Chen", initials: "AC", role: "Product" },
    published_at: "2025-04-10",
    read_time_min: 6,
    featured: true,
    image_emoji: "🤖",
    tags: ["product", "ai", "sherlock"]
  },
  {
    slug: "byoc-arm64",
    title: "BYOC now supports ARM64 — including AWS Graviton and Apple Silicon",
    excerpt: "You can now register ARM64 hosts as Forge CI BYOC runners. Graviton3 gives you 40% better price-performance for most CI workloads.",
    body: "",
    category: "Engineering",
    author: { name: "Marcus Webb", initials: "MW", role: "Engineering" },
    published_at: "2025-03-28",
    read_time_min: 8,
    featured: false,
    image_emoji: "💪",
    tags: ["engineering", "byoc", "arm64"]
  },
  {
    slug: "cache-deep-dive",
    title: "How Forge CI achieves 89% cache hit rates",
    excerpt: "A deep dive into our content-addressed caching layer, how we handle cache invalidation without the false misses, and what you can do to push hit rates even higher.",
    body: "",
    category: "Engineering",
    author: { name: "Priya Nair", initials: "PN", role: "Infrastructure" },
    published_at: "2025-03-14",
    read_time_min: 12,
    featured: false,
    image_emoji: "⚡",
    tags: ["engineering", "cache", "performance"]
  },
  {
    slug: "gha-migration",
    title: "We migrated 3,000 GitHub Actions workflows to Forge CI in a weekend",
    excerpt: "A step-by-step account of migrating a large monorepo's CI from GitHub Actions to Forge CI — pitfalls, wins, and the migration tool we built along the way.",
    body: "",
    category: "Tutorial",
    author: { name: "Sam Rodriguez", initials: "SR", role: "DevRel" },
    published_at: "2025-02-28",
    read_time_min: 15,
    featured: false,
    image_emoji: "🚀",
    tags: ["tutorial", "migration", "github-actions"]
  },
  {
    slug: "oidc-secrets",
    title: "Why you should stop using long-lived CI credentials today",
    excerpt: "Long-lived AWS keys in your CI are a liability. Here's how Forge CI's OIDC token exchange eliminates them entirely — and how to migrate in 30 minutes.",
    body: "",
    category: "Security",
    author: { name: "Alice Park", initials: "AP", role: "Security" },
    published_at: "2025-02-12",
    read_time_min: 10,
    featured: false,
    image_emoji: "🔒",
    tags: ["security", "oidc", "aws"]
  }
];
const MOCK_TESTIMONIALS = [
  { quote: "Forge CI cut our test suite from 22 minutes to under 90 seconds. Sherlock diagnosed a flaky test issue in 8 seconds that we'd been chasing for two weeks.", author: "Mia Chen", title: "VP Engineering", company: "Lineara", initials: "MC", metric: "15× faster", metric_detail: "22 min → 90 sec" },
  { quote: "We migrated from Jenkins on a Friday afternoon. By Monday every team was running. The BYOC runner support meant we didn't have to touch our compliance setup.", author: "Tomás Reyes", title: "Platform Lead", company: "Stackline", initials: "TR", metric: "2-day migration", metric_detail: "300 pipelines moved" },
  { quote: "The cache hit rate alone paid for the plan. 94% of our builds skip the install step entirely. Our engineers stopped dreading the CI queue.", author: "Priya Nair", title: "Staff SRE", company: "Monobase", initials: "PN", metric: "94% cache hits", metric_detail: "$2,400/mo saved" },
  { quote: "Sherlock is the best on-call engineer who never sleeps. It catches broken deploys, explains the exact line of config that caused it, and opens the PR to fix it.", author: "Olu Adeyemi", title: "CTO", company: "Crux Systems", initials: "OA", metric: "74% less MTTR", metric_detail: "45 min → 12 min" },
  { quote: "We run 50,000 builds a month across 8 repos. Forge's audit logs and SSO provisioning made our SOC 2 audit trivial. The compliance team actually smiled.", author: "Sara Johansson", title: "Head of Security", company: "Meridian Cloud", initials: "SJ", metric: "SOC 2 in 30 days", metric_detail: "vs. 6-month industry avg" },
  { quote: "The matrix build support is extraordinary. We test across 4 Node versions × 3 OS × 2 arch targets in parallel. What used to take 45 min takes 3.", author: "Daniel Park", title: "Senior Engineer", company: "Radiant Labs", initials: "DP", metric: "45 min → 3 min", metric_detail: "24 parallel workers" }
];
const MOCK_CASE_STUDIES = [
  {
    slug: "vercel",
    company: "Vercel",
    industry: "Platform tooling",
    logo_emoji: "▲",
    team_size: "2,000+ engineers",
    headline: "Vercel cut global deploy latency by 60% after migrating to Forge CI",
    results: [
      { metric: "Build time", before: "14 min", after: "82 sec" },
      { metric: "Cache hit rate", before: "42%", after: "96%" },
      { metric: "Deploy frequency", before: "12/day", after: "80/day" }
    ],
    quote: "The distributed cache across all our repos was the unlock. Developers stopped thinking about CI.",
    quote_author: "Malte Ubl, CTO"
  },
  {
    slug: "stripe",
    company: "Stripe",
    industry: "Financial infrastructure",
    logo_emoji: "💳",
    team_size: "800+ engineers",
    headline: "Stripe reduced CI spend by $1.2M/year without sacrificing coverage",
    results: [
      { metric: "Build minutes/month", before: "4.2M", after: "1.8M" },
      { metric: "Flaky test rate", before: "8.4%", after: "0.3%" },
      { metric: "Deployment confidence", before: "78%", after: "99%" }
    ],
    quote: "Sherlock eliminated our entire on-call rotation for CI failures. We reinvested those hours in feature work.",
    quote_author: "Laila Hassan, Platform Engineering"
  }
];
const PRICING_TIERS = [
  {
    id: "hobby",
    name: "Hobby",
    tagline: "Perfect for solo projects",
    monthly_price: 0,
    yearly_price: 0,
    custom: false,
    featured: false,
    build_minutes: "2,000 / month",
    parallelism: "1 concurrent",
    cache_gb: "500 MB",
    runners: "Linux x64 (small)",
    features: ["Unlimited public repos", "GitHub & GitLab", "Basic caching", "30-day log retention", "Community support"],
    missing: ["BYOC runners", "SSO/SAML", "Audit logs", "Sherlock AI"],
    cta: "Start for free",
    cta_href: "/auth/signup"
  },
  {
    id: "pro",
    name: "Pro",
    tagline: "For serious devs & small teams",
    monthly_price: 29,
    yearly_price: 23,
    custom: false,
    featured: false,
    build_minutes: "20,000 / month",
    parallelism: "10 concurrent",
    cache_gb: "10 GB",
    runners: "Linux x64/ARM · macOS",
    features: ["Everything in Hobby", "Private repos", "Advanced caching", "macOS runners", "ARM64 runners", "Priority queue", "Build insights", "Email support"],
    missing: ["SSO/SAML", "Audit logs", "Sherlock AI"],
    cta: "Start free trial",
    cta_href: "/auth/signup?plan=pro"
  },
  {
    id: "team",
    name: "Team",
    tagline: "Built for growing engineering teams",
    monthly_price: 79,
    yearly_price: 63,
    custom: false,
    featured: true,
    badge: "Most popular",
    build_minutes: "100,000 / month",
    parallelism: "50 concurrent",
    cache_gb: "100 GB",
    runners: "All runner types",
    features: ["Everything in Pro", "SSO (Google, GitHub)", "RBAC + team permissions", "BYOC runners", "Sherlock AI agent", "Sandboxes", "Image registry", "Webhook delivery", "Audit logs (90 days)", "Dedicated Slack channel", "99.9% SLA"],
    missing: ["SAML 2.0", "SCIM provisioning"],
    cta: "Start free trial",
    cta_href: "/auth/signup?plan=team"
  },
  {
    id: "enterprise",
    name: "Enterprise",
    tagline: "For scale and compliance",
    custom: true,
    featured: false,
    build_minutes: "Unlimited",
    parallelism: "Unlimited",
    cache_gb: "Custom",
    runners: "All + dedicated fleet",
    features: ["Everything in Team", "SAML 2.0 SSO", "SCIM auto-provisioning", "Tailnet / VPC peering", "Dedicated runners", "Custom regions", "Audit logs (unlimited)", "HIPAA BAA", "SOC 2 report access", "Custom contracts & MSA", "Dedicated CSM", "SLA 99.99%"],
    missing: [],
    cta: "Talk to sales",
    cta_href: "/contact/sales"
  }
];
const MOCK_INTEGRATIONS = [
  { id: "github", name: "GitHub", category: "Version Control", icon: "🐙", description: "Triggers builds on push, PR events, and writes commit status checks.", official: true, installed: true, connected_as: "acme-corp", connected_at: "Jan 12, 2024" },
  { id: "slack", name: "Slack", category: "Notifications", icon: "💬", description: "Posts build notifications to #eng-deploys and #ci-alerts channels.", official: true, installed: true, connected_as: "Acme Corp workspace", connected_at: "Jan 14, 2024" },
  { id: "datadog", name: "Datadog", category: "Observability", icon: "🐶", description: "Streams build metrics and traces. Pre-built ForgeCI dashboard enabled.", official: true, installed: true, connected_as: "acme-corp.datadoghq.com", connected_at: "Feb 3, 2024" },
  { id: "linear", name: "Linear", category: "Project Mgmt", icon: "📐", description: "Auto-closes Linear issues on merge via smart commit syntax.", official: true, installed: true, connected_as: "Acme Corp (linear.app)", connected_at: "Mar 1, 2024" },
  { id: "sentry", name: "Sentry", category: "Observability", icon: "🛡", description: "Creates Sentry releases on every deploy and associates commits.", official: true, installed: true, connected_as: "acme-corp (sentry.io)", connected_at: "Mar 14, 2024" },
  { id: "jira", name: "Jira", category: "Project Mgmt", icon: "📋", description: "Transition Jira issues automatically on deploy.", official: true, installed: false },
  { id: "pagerduty", name: "PagerDuty", category: "Notifications", icon: "🚨", description: "Trigger incidents on production deploy failures.", official: true, installed: false },
  { id: "vault", name: "HashiCorp Vault", category: "Security", icon: "🔑", description: "Dynamic secrets injection from Vault. No long-lived credentials.", official: true, installed: false }
];
function formatBytes(bytes, decimals = 1) {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const dm = decimals < 0 ? 0 : decimals;
  const sizes = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + " " + sizes[i];
}
function formatDuration(ms) {
  if (ms < 1e3) return `${ms}ms`;
  if (ms < 6e4) return `${(ms / 1e3).toFixed(1)}s`;
  const m = Math.floor(ms / 6e4);
  const s = Math.floor(ms % 6e4 / 1e3);
  return s > 0 ? `${m}m ${s}s` : `${m}m`;
}
function relativeTime(iso) {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1e3);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export { MOCK_BLOG_POSTS as M, PRICING_TIERS as P, MOCK_CASE_STUDIES as a, MOCK_SHERLOCK_PATTERNS as b, MOCK_SHERLOCK_ANALYSES as c, MOCK_BUILDS as d, MOCK_CACHE_KEYS as e, formatDuration as f, MOCK_METRICS as g, formatBytes as h, MOCK_INTEGRATIONS as i, MOCK_AUDIT_LOG as j, MOCK_ORG as k, MOCK_SYSTEM_RESOURCES as l, MOCK_TESTIMONIALS as m, relativeTime as r };

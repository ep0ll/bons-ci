// ============================================================
// FORGE CI — Complete Type Definitions
// ============================================================

// ── Auth ──────────────────────────────────────────────────
export interface User {
  id: string;
  name: string;
  email: string;
  avatar_url?: string;
  role: OrgRole;
  org_id: string;
  created_at: string;
  last_active?: string;
  mfa_enabled: boolean;
  sso_provisioned: boolean;
}

export interface Session {
  token: string;
  user_id: string;
  org_id: string;
  expires_at: string;
  device: string;
  ip: string;
}

export interface Org {
  id: string;
  name: string;
  slug: string;
  avatar_url?: string;
  plan: 'hobby' | 'pro' | 'team' | 'enterprise';
  seat_count: number;
  seat_limit: number;
  build_minutes_used: number;
  build_minutes_limit: number;
  cache_used_bytes: number;
  cache_limit_bytes: number;
  egress_used_bytes: number;
  egress_limit_bytes: number;
  created_at: string;
  sso_enabled: boolean;
  sso_provider?: string;
  enforce_sso: boolean;
  enforce_mfa: boolean;
}

export type OrgRole = 'owner' | 'admin' | 'member' | 'viewer';

// ── Build / Pipeline ──────────────────────────────────────
export type BuildStatus = 'running' | 'success' | 'failed' | 'cancelled' | 'queued' | 'skipped';
export type TriggerType = 'push' | 'pr' | 'manual' | 'schedule' | 'api';

export interface BuildStep {
  id: string;
  name: string;
  status: BuildStatus;
  duration_ms: number;
  started_at: string;
  finished_at?: string;
  exit_code?: number;
  log_lines: LogLine[];
  resources?: StepResources;
}

export interface StepResources {
  cpu_avg: number;
  cpu_peak: number;
  mem_avg_mb: number;
  mem_peak_mb: number;
  net_in_mb: number;
  net_out_mb: number;
  disk_read_mb: number;
  disk_write_mb: number;
}

export interface LogLine {
  t: number;        // unix ms
  stream: 'stdout' | 'stderr';
  text: string;
  level?: 'info' | 'warn' | 'error' | 'debug';
}

export interface Build {
  id: string;
  number: number;
  status: BuildStatus;
  project_id: string;
  project_name: string;
  org_id: string;
  namespace: string;
  branch: string;
  commit_sha: string;
  commit_short: string;
  commit_message: string;
  author: BuildAuthor;
  trigger: TriggerType;
  pr_number?: number;
  pipeline_file: string;
  runner_type: string;
  runner_region: string;
  parallelism: number;
  credits_used: number;
  cache_hit: boolean;
  cache_key?: string;
  queued_at: string;
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  steps: BuildStep[];
  artifacts: Artifact[];
  resources?: StepResources;
  test_results?: TestResults;
}

export interface BuildAuthor {
  name: string;
  email: string;
  avatar_url?: string;
  username: string;
}

export interface TestResults {
  total: number;
  passed: number;
  failed: number;
  skipped: number;
  duration_ms: number;
  coverage_pct?: number;
  flaky_tests: string[];
}

// ── Metrics ──────────────────────────────────────────────
export interface MetricDataPoint {
  ts: string;     // ISO timestamp
  value: number;
}

export interface BuildMetrics {
  period_days: number;
  build_count: number;
  success_rate: number;
  avg_duration_ms: number;
  p50_duration_ms: number;
  p95_duration_ms: number;
  p99_duration_ms: number;
  cache_hit_rate: number;
  build_minutes_used: number;
  credits_used: number;
  egress_gb: number;
  series: {
    build_count: MetricDataPoint[];
    success_rate: MetricDataPoint[];
    avg_duration: MetricDataPoint[];
    cache_hit_rate: MetricDataPoint[];
    cpu_usage: MetricDataPoint[];
    mem_usage_mb: MetricDataPoint[];
    net_in_mbps: MetricDataPoint[];
    net_out_mbps: MetricDataPoint[];
    egress_gb: MetricDataPoint[];
    build_minutes: MetricDataPoint[];
  };
  by_project: ProjectMetrics[];
  by_member: MemberMetrics[];
  waterfall_percentiles: PercentileData[];
}

export interface PercentileData {
  project: string;
  p50: number;
  p75: number;
  p90: number;
  p95: number;
  p99: number;
  max: number;
}

export interface ProjectMetrics {
  project_id: string;
  project_name: string;
  build_count: number;
  success_rate: number;
  avg_duration_ms: number;
  p95_duration_ms: number;
  build_minutes: number;
  cache_hit_rate: number;
  cost_pct: number;
}

export interface MemberMetrics {
  user_id: string;
  user_name: string;
  initials: string;
  build_count: number;
  success_rate: number;
  avg_duration_ms: number;
}

// ── Resource Monitoring ──────────────────────────────────
export interface SystemResourceSnapshot {
  ts: string;
  cpu_cores_total: number;
  cpu_cores_used: number;
  cpu_pct: number;
  mem_total_gb: number;
  mem_used_gb: number;
  mem_pct: number;
  net_in_mbps: number;
  net_out_mbps: number;
  disk_read_mbps: number;
  disk_write_mbps: number;
  active_runners: number;
  queued_jobs: number;
  egress_today_gb: number;
}

// ── Project / Namespace ──────────────────────────────────
export interface Project {
  id: string;
  name: string;
  slug: string;
  description: string;
  org_id: string;
  vcs_provider: 'github' | 'gitlab' | 'bitbucket';
  repo_url: string;
  repo_full_name: string;
  default_branch: string;
  language: string;
  last_build?: Partial<Build>;
  created_at: string;
  settings: ProjectSettings;
  tags: string[];
  member_count: number;
  webhook_url?: string;
}

export interface ProjectSettings {
  runner_size: 'small' | 'medium' | 'large' | 'xlarge' | '2xlarge';
  runner_os: 'linux-x64' | 'linux-arm64' | 'macos-m2' | 'windows';
  build_timeout_min: number;
  parallelism: number;
  cache_enabled: boolean;
  cache_ttl_days: number;
  auto_cancel_redundant: boolean;
  build_on_pr: boolean;
  notify_slack: boolean;
}

// ── Registry ─────────────────────────────────────────────
export interface RegistryRepo {
  id: string;
  name: string;
  full_name: string;
  description: string;
  is_public: boolean;
  tag_count: number;
  size_bytes: number;
  pull_count: number;
  push_count: number;
  last_pushed_at: string;
  created_at: string;
  vulnerability_summary: VulnSummary;
}

export interface VulnSummary {
  critical: number;
  high: number;
  medium: number;
  low: number;
  scanned_at: string;
}

export interface ImageTag {
  name: string;
  digest: string;
  size_bytes: number;
  pushed_at: string;
  last_pulled_at?: string;
  platforms: string[];
  vuln: VulnSummary;
  build_id?: string;
}

// ── Secrets & Tokens ─────────────────────────────────────
export interface Secret {
  id: string;
  name: string;
  description: string;
  scope: 'org' | 'project' | 'environment';
  env_name?: string;
  project_id?: string;
  created_at: string;
  updated_at: string;
  created_by: string;
  last_used_at?: string;
  rotation_due_at?: string;
  used_in_pipelines: number;
}

export interface APIToken {
  id: string;
  name: string;
  prefix: string;
  token_hash: string;
  scopes: APIScope[];
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  created_by: string;
  revoked: boolean;
}

export type APIScope =
  | 'builds:read' | 'builds:write'
  | 'artifacts:read' | 'artifacts:write'
  | 'secrets:read'
  | 'metrics:read'
  | 'projects:read' | 'projects:write'
  | 'registry:read' | 'registry:write'
  | 'members:read' | 'members:write'
  | 'tokens:read';

// ── Cache ────────────────────────────────────────────────
export interface CacheKey {
  id: string;
  key: string;
  project_id: string;
  project_name: string;
  size_bytes: number;
  hit_count: number;
  miss_count: number;
  hit_rate: number;
  last_hit_at: string;
  ttl_days: number;
  created_at: string;
}

// ── Members ──────────────────────────────────────────────
export interface Member {
  id: string;
  user_id: string;
  name: string;
  email: string;
  avatar_url?: string;
  initials: string;
  role: OrgRole;
  joined_at: string;
  last_active_at: string;
  mfa_enabled: boolean;
  sso_provisioned: boolean;
  build_count: number;
}

export interface Invitation {
  id: string;
  email: string;
  role: OrgRole;
  invited_by_name: string;
  invited_at: string;
  expires_at: string;
  accepted: boolean;
}

// ── Artifacts ────────────────────────────────────────────
export interface Artifact {
  id: string;
  name: string;
  path: string;
  size_bytes: number;
  content_type: string;
  build_id: string;
  project_name: string;
  branch: string;
  created_at: string;
  expires_at: string;
  download_count: number;
}

// ── Sandbox ──────────────────────────────────────────────
export type SandboxStatus = 'starting' | 'running' | 'stopped' | 'expired';

export interface Sandbox {
  id: string;
  name: string;
  project_name: string;
  branch: string;
  build_id: string;
  status: SandboxStatus;
  image: string;
  runner_size: string;
  created_at: string;
  expires_at: string;
  creator_initials: string;
  public_url?: string;
  ports: SandboxPort[];
  cpu_pct: number;
  mem_pct: number;
}

export interface SandboxPort {
  port: number;
  label: string;
  is_public: boolean;
  url?: string;
}

// ── Audit Log ────────────────────────────────────────────
export type AuditSeverity = 'info' | 'warning' | 'danger';

export interface AuditLogEntry {
  id: string;
  actor: string;
  actor_id: string;
  action: string;
  resource: string;
  ip: string;
  user_agent: string;
  timestamp: string;
  severity: AuditSeverity;
  metadata?: Record<string, string>;
}

// ── Egress Rules ─────────────────────────────────────────
export interface EgressRule {
  id: string;
  type: 'allow' | 'block';
  target: string;
  protocol: 'TCP' | 'UDP' | 'HTTP' | 'HTTPS' | 'Any';
  port?: number;
  description: string;
  enabled: boolean;
  priority: number;
}

// ── Integrations ─────────────────────────────────────────
export interface Integration {
  id: string;
  name: string;
  category: string;
  icon: string;
  description: string;
  official: boolean;
  installed: boolean;
  connected_as?: string;
  connected_at?: string;
  config?: Record<string, string>;
}

// ── Pipeline Template ────────────────────────────────────
export interface PipelineTemplate {
  id: string;
  name: string;
  category: string;
  icon: string;
  description: string;
  tags: string[];
  runtime: string;
  avg_duration_s: number;
  star_count: number;
  official: boolean;
  yaml: string;
}

// ── Plugin / Marketplace ─────────────────────────────────
export interface Plugin {
  id: string;
  name: string;
  author: string;
  category: string;
  icon: string;
  description: string;
  rating: number;
  install_count: number;
  official: boolean;
  installed: boolean;
  price: 'free' | 'team' | 'enterprise';
  version: string;
  scopes: string[];
}

// ── Sherlock AI ──────────────────────────────────────────
export interface SherlockAnalysis {
  id: string;
  build_id: string;
  project_name: string;
  branch: string;
  failed_step: string;
  root_cause: string;
  confidence: number;
  analyzed_at: string;
  status: 'pending' | 'fix_suggested' | 'pr_opened' | 'fixed';
  pr_number?: number;
  fix_diff?: string;
  patterns_detected: string[];
}

export interface SherlockPattern {
  id: string;
  title: string;
  description: string;
  severity: 'info' | 'warning' | 'danger';
  project_name: string;
  occurrence_count?: number;
  suggestion: string;
  detected_at: string;
}

// ── Billing ──────────────────────────────────────────────
export interface Invoice {
  id: string;
  period: string;
  date: string;
  amount_cents: number;
  status: 'paid' | 'pending' | 'failed';
  items: InvoiceItem[];
}

export interface InvoiceItem {
  description: string;
  amount_cents: number;
}

// ── Blog ─────────────────────────────────────────────────
export interface BlogPost {
  slug: string;
  title: string;
  excerpt: string;
  body: string;
  category: string;
  author: BlogAuthor;
  published_at: string;
  read_time_min: number;
  featured: boolean;
  image_emoji: string;
  tags: string[];
}

export interface BlogAuthor {
  name: string;
  initials: string;
  role: string;
  avatar?: string;
}

// ── API Response types ───────────────────────────────────
export interface APIResponse<T> {
  data: T;
  error?: string;
  total?: number;
  page?: number;
  per_page?: number;
}

export interface PaginatedResponse<T> extends APIResponse<T[]> {
  total: number;
  page: number;
  per_page: number;
  has_next: boolean;
  has_prev: boolean;
}

// ── Filter/Search types ──────────────────────────────────
export interface BuildFilters {
  status?: BuildStatus[];
  project?: string;
  branch?: string;
  trigger?: TriggerType;
  author?: string;
  date_from?: string;
  date_to?: string;
  search?: string;
  page?: number;
  per_page?: number;
  sort?: 'started_at' | 'duration_ms' | 'number';
  order?: 'asc' | 'desc';
}

export interface LogFilters {
  build_id: string;
  step_id?: string;
  search?: string;
  level?: Array<'info' | 'warn' | 'error' | 'debug'>;
  stream?: 'stdout' | 'stderr';
  from_line?: number;
  to_line?: number;
}

// ── Pricing ──────────────────────────────────────────────
export interface PricingTier {
  id: 'hobby' | 'pro' | 'team' | 'enterprise';
  name: string;
  tagline: string;
  monthly_price?: number;
  yearly_price?: number;
  custom: boolean;
  featured: boolean;
  badge?: string;
  build_minutes: string;
  parallelism: string;
  cache_gb: string;
  runners: string;
  features: string[];
  missing: string[];
  cta: string;
  cta_href: string;
}

// ── Marketing ─────────────────────────────────────────────
export interface Testimonial {
  quote: string;
  author: string;
  title: string;
  company: string;
  initials: string;
  metric: string;
  metric_detail: string;
}

export interface CaseStudy {
  slug: string;
  company: string;
  industry: string;
  logo_emoji: string;
  team_size: string;
  headline: string;
  results: CaseStudyResult[];
  quote: string;
  quote_author: string;
}

export interface CaseStudyResult {
  metric: string;
  before: string;
  after: string;
}

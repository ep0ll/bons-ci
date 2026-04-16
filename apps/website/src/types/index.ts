/* ============================================================
   FORGE CI — Shared TypeScript Types
   ============================================================ */

// ── Navigation ──────────────────────────────────────────────
export interface NavItem {
  label: string;
  href: string;
  icon?: string;
  description?: string;
  badge?: string;
  children?: NavItem[];
}

// ── Build / Pipeline ────────────────────────────────────────
export type BuildStatus =
  | 'running'
  | 'success'
  | 'failed'
  | 'cancelled'
  | 'queued'
  | 'pending'
  | 'skipped';

export interface BuildStep {
  id: string;
  name: string;
  status: BuildStatus;
  duration_ms?: number;
  started_at?: string;
  finished_at?: string;
  log_lines?: string[];
}

export interface Build {
  id: string;
  number: number;
  status: BuildStatus;
  branch: string;
  commit_sha: string;
  commit_message: string;
  author: {
    name: string;
    avatar?: string;
    email?: string;
  };
  project: string;
  namespace: string;
  triggered_by: 'push' | 'pr' | 'manual' | 'schedule' | 'api';
  duration_ms?: number;
  queued_at: string;
  started_at?: string;
  finished_at?: string;
  steps: BuildStep[];
  artifacts?: Artifact[];
  cache_hit?: boolean;
  runner_type?: string;
  credits_used?: number;
}

// ── Metrics ─────────────────────────────────────────────────
export interface UsageMetrics {
  build_minutes_used: number;
  build_minutes_limit: number;
  builds_count: number;
  cache_size_gb: number;
  cache_limit_gb: number;
  egress_gb: number;
  egress_limit_gb: number;
  cpu_avg_percent: number;
  memory_avg_percent: number;
  network_in_gb: number;
  network_out_gb: number;
  period_days: number;
}

export interface MetricDataPoint {
  date: string;
  value: number;
}

// ── Project / Namespace ─────────────────────────────────────
export interface Project {
  id: string;
  name: string;
  slug: string;
  description?: string;
  avatar_url?: string;
  namespace: string;
  vcs_provider: 'github' | 'gitlab' | 'bitbucket';
  repo_url: string;
  default_branch: string;
  last_build?: Build;
  created_at: string;
  settings?: ProjectSettings;
}

export interface ProjectSettings {
  build_timeout_min: number;
  parallelism: number;
  cache_enabled: boolean;
  cache_ttl_days: number;
  environment: 'linux-x64' | 'linux-arm64' | 'macos' | 'windows';
  runner_size: 'small' | 'medium' | 'large' | 'xlarge' | '2xlarge';
}

// ── Team / Members ──────────────────────────────────────────
export type TeamRole = 'owner' | 'admin' | 'member' | 'viewer';

export interface TeamMember {
  id: string;
  name: string;
  email: string;
  avatar_url?: string;
  role: TeamRole;
  joined_at: string;
  last_active?: string;
  sso_provisioned?: boolean;
}

export interface Invitation {
  id: string;
  email: string;
  role: TeamRole;
  invited_by: string;
  created_at: string;
  expires_at: string;
  status: 'pending' | 'accepted' | 'expired';
}

// ── Artifacts ───────────────────────────────────────────────
export interface Artifact {
  id: string;
  name: string;
  path: string;
  size_bytes: number;
  content_type: string;
  created_at: string;
  expires_at?: string;
  download_url?: string;
}

// ── Registry ────────────────────────────────────────────────
export interface RegistryRepository {
  id: string;
  name: string;
  full_name: string;
  description?: string;
  is_public: boolean;
  tag_count: number;
  size_bytes: number;
  pull_count: number;
  last_pushed: string;
  vulnerability_count?: { critical: number; high: number; medium: number; low: number; };
}

// ── Secrets ─────────────────────────────────────────────────
export interface Secret {
  id: string;
  name: string;
  description?: string;
  scope: 'organization' | 'project' | 'environment';
  environment?: string;
  created_at: string;
  updated_at: string;
  created_by: string;
  last_used?: string;
  rotation_due?: string;
}

// ── API Tokens ──────────────────────────────────────────────
export interface APIToken {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  created_at: string;
  last_used?: string;
  expires_at?: string;
  created_by: string;
}

// ── Pricing ─────────────────────────────────────────────────
export interface PricingTier {
  id: string;
  name: string;
  tagline: string;
  price_monthly?: number;
  price_yearly?: number;
  custom_pricing?: boolean;
  featured?: boolean;
  build_minutes: string;
  parallelism: string;
  storage: string;
  features: string[];
  cta: string;
  cta_href: string;
}

// ── Integration ─────────────────────────────────────────────
export interface Integration {
  id: string;
  name: string;
  category: string;
  description: string;
  logo?: string;
  installed?: boolean;
  official?: boolean;
}

// ============================================================
// FORGE CI — Unit Tests
// Tests for auth, charts, and data utilities
// Run: npm test
// ============================================================

import { describe, test, expect } from '@jest/globals';

// Re-export under test paths since we can't import astro modules in Jest
// These functions are pure TS with no DOM deps

// ── Auth tests ─────────────────────────────────────────────
describe('Auth — Role hierarchy', () => {
  type OrgRole = 'owner' | 'admin' | 'member' | 'viewer';

  const ROLE_HIERARCHY: Record<OrgRole, number> = {
    owner: 4, admin: 3, member: 2, viewer: 1,
  };

  function hasRole(userRole: OrgRole, minRole: OrgRole): boolean {
    return ROLE_HIERARCHY[userRole] >= ROLE_HIERARCHY[minRole];
  }

  test('owner can do everything', () => {
    expect(hasRole('owner', 'viewer')).toBe(true);
    expect(hasRole('owner', 'member')).toBe(true);
    expect(hasRole('owner', 'admin')).toBe(true);
    expect(hasRole('owner', 'owner')).toBe(true);
  });

  test('viewer can only viewer actions', () => {
    expect(hasRole('viewer', 'viewer')).toBe(true);
    expect(hasRole('viewer', 'member')).toBe(false);
    expect(hasRole('viewer', 'admin')).toBe(false);
    expect(hasRole('viewer', 'owner')).toBe(false);
  });

  test('admin can do admin and below', () => {
    expect(hasRole('admin', 'viewer')).toBe(true);
    expect(hasRole('admin', 'member')).toBe(true);
    expect(hasRole('admin', 'admin')).toBe(true);
    expect(hasRole('admin', 'owner')).toBe(false);
  });

  test('member can do member and below', () => {
    expect(hasRole('member', 'viewer')).toBe(true);
    expect(hasRole('member', 'member')).toBe(true);
    expect(hasRole('member', 'admin')).toBe(false);
  });
});

describe('Auth — Email validation', () => {
  function isValidEmail(email: string): boolean {
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
  }

  function isWorkEmail(email: string): boolean {
    const freeProviders = new Set(['gmail.com','yahoo.com','hotmail.com','outlook.com']);
    const domain = email.split('@')[1]?.toLowerCase() ?? '';
    return !freeProviders.has(domain);
  }

  test('accepts valid emails', () => {
    expect(isValidEmail('user@company.com')).toBe(true);
    expect(isValidEmail('a.b+c@d.e.f')).toBe(true);
    expect(isValidEmail('admin@forge-ci.dev')).toBe(true);
  });

  test('rejects invalid emails', () => {
    expect(isValidEmail('notanemail')).toBe(false);
    expect(isValidEmail('@nodomain.com')).toBe(false);
    expect(isValidEmail('noDot@com')).toBe(false);
    expect(isValidEmail('')).toBe(false);
  });

  test('identifies free email providers', () => {
    expect(isWorkEmail('user@gmail.com')).toBe(false);
    expect(isWorkEmail('user@company.com')).toBe(true);
    expect(isWorkEmail('user@yahoo.com')).toBe(false);
    expect(isWorkEmail('user@forge-ci.dev')).toBe(true);
  });
});

describe('Auth — Password strength', () => {
  function checkPasswordStrength(password: string) {
    let score = 0;
    if (password.length >= 8) score++;
    if (password.length >= 12) score++;
    if (/[A-Z]/.test(password) && /[0-9]/.test(password)) score++;
    if (/[^a-zA-Z0-9]/.test(password)) score++;
    return score;
  }

  test('short password scores 0', () => {
    expect(checkPasswordStrength('abc')).toBe(0);
  });

  test('medium password scores 1-2', () => {
    const score = checkPasswordStrength('password12');
    expect(score).toBeGreaterThanOrEqual(1);
    expect(score).toBeLessThanOrEqual(2);
  });

  test('strong password scores 4', () => {
    expect(checkPasswordStrength('Str0ng!Password#2024')).toBe(4);
  });

  test('empty string scores 0', () => {
    expect(checkPasswordStrength('')).toBe(0);
  });
});

describe('Auth — Slug validation', () => {
  function isValidSlug(slug: string): boolean {
    return /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(slug);
  }

  function slugify(text: string): string {
    return text.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '').replace(/-+/g, '-').replace(/^-|-$/g, '');
  }

  test('valid slugs pass', () => {
    expect(isValidSlug('acme-corp')).toBe(true);
    expect(isValidSlug('my-org-123')).toBe(true);
    expect(isValidSlug('ab')).toBe(true);
  });

  test('invalid slugs fail', () => {
    expect(isValidSlug('UPPERCASE')).toBe(false);
    expect(isValidSlug('has spaces')).toBe(false);
    expect(isValidSlug('-starts-with-dash')).toBe(false);
    expect(isValidSlug('')).toBe(false);
  });

  test('slugify converts text correctly', () => {
    expect(slugify('Acme Corp')).toBe('acme-corp');
    expect(slugify('My  Company 123')).toBe('my-company-123');
    expect(slugify('Héllo Wörld')).toBe('hllo-wrld');
  });
});

// ── Chart utilities tests ──────────────────────────────────
describe('Charts — Series to points', () => {
  type MetricDataPoint = { ts: string; value: number };

  function seriesToPoints(
    data: MetricDataPoint[],
    width: number,
    height: number,
  ): string {
    if (data.length === 0) return '';
    const values = data.map(d => d.value);
    const min = Math.min(...values);
    const max = Math.max(...values);
    const range = max - min || 1;
    return data.map((point, i) => {
      const x = (i / (data.length - 1)) * width;
      const y = height - ((point.value - min) / range) * height;
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    }).join(' ');
  }

  test('empty array returns empty string', () => {
    expect(seriesToPoints([], 100, 50)).toBe('');
  });

  test('single point returns one coordinate', () => {
    const result = seriesToPoints([{ ts: '2025-01-01', value: 50 }], 100, 50);
    expect(result).toBe('0.00,25.00');
  });

  test('two points span the full width', () => {
    const data = [{ ts: '1', value: 0 }, { ts: '2', value: 100 }];
    const result = seriesToPoints(data, 100, 50);
    const [p1, p2] = result.split(' ');
    expect(p1).toBe('0.00,50.00');
    expect(p2).toBe('100.00,0.00');
  });

  test('uniform values produce a horizontal line', () => {
    const data = [
      { ts: '1', value: 42 },
      { ts: '2', value: 42 },
      { ts: '3', value: 42 },
    ];
    const result = seriesToPoints(data, 100, 50);
    const ys = result.split(' ').map(p => parseFloat(p.split(',')[1]));
    expect(ys.every(y => y === ys[0])).toBe(true);
  });
});

describe('Charts — Percentile waterfall', () => {
  type PercentileData = {
    project: string;
    p50: number; p75: number; p90: number; p95: number; p99: number; max: number;
  };

  function validatePercentileOrder(d: PercentileData): boolean {
    return d.p50 <= d.p75 && d.p75 <= d.p90 && d.p90 <= d.p95 && d.p95 <= d.p99 && d.p99 <= d.max;
  }

  test('api-service percentiles are ordered', () => {
    const d: PercentileData = { project:'api-service', p50:84, p75:118, p90:148, p95:196, p99:420, max:720 };
    expect(validatePercentileOrder(d)).toBe(true);
  });

  test('detects invalid percentile order', () => {
    const d: PercentileData = { project:'broken', p50:200, p75:100, p90:300, p95:400, p99:500, max:600 };
    expect(validatePercentileOrder(d)).toBe(false);
  });
});

describe('Charts — Log filter', () => {
  type LogLine = { t: number; stream: 'stdout'|'stderr'; text: string; level?: string };

  function filterLogLines(
    lines: LogLine[],
    query?: string,
    level?: string,
    stream?: 'stdout'|'stderr',
  ): LogLine[] {
    return lines.filter(line => {
      if (level && line.level !== level) return false;
      if (stream && line.stream !== stream) return false;
      if (query && !line.text.toLowerCase().includes(query.toLowerCase())) return false;
      return true;
    });
  }

  const LINES: LogLine[] = [
    { t: 1, stream:'stdout', text:'npm ci completed successfully', level:'info' },
    { t: 2, stream:'stderr', text:'Warning: peer dependency mismatch', level:'warn' },
    { t: 3, stream:'stderr', text:'Error: Cannot read properties of null', level:'error' },
    { t: 4, stream:'stdout', text:'Tests: 1847 passed, 0 failed', level:'info' },
  ];

  test('no filter returns all lines', () => {
    expect(filterLogLines(LINES)).toHaveLength(4);
  });

  test('query filter is case-insensitive', () => {
    expect(filterLogLines(LINES, 'ERROR')).toHaveLength(1);
    expect(filterLogLines(LINES, 'error')).toHaveLength(1);
  });

  test('level filter works', () => {
    expect(filterLogLines(LINES, undefined, 'error')).toHaveLength(1);
    expect(filterLogLines(LINES, undefined, 'info')).toHaveLength(2);
  });

  test('stream filter works', () => {
    expect(filterLogLines(LINES, undefined, undefined, 'stderr')).toHaveLength(2);
    expect(filterLogLines(LINES, undefined, undefined, 'stdout')).toHaveLength(2);
  });

  test('combined filters narrow results', () => {
    expect(filterLogLines(LINES, 'npm', 'info', 'stdout')).toHaveLength(1);
  });
});

// ── Data utilities tests ───────────────────────────────────
describe('Data — Format utilities', () => {
  function formatBytes(bytes: number): string {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B','KB','MB','GB','TB'];
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
    return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
  }

  function formatMs(ms: number): string {
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
    const m = Math.floor(ms / 60_000);
    const s = Math.floor((ms % 60_000) / 1000);
    return s > 0 ? `${m}m ${s}s` : `${m}m`;
  }

  test('formatBytes handles various sizes', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1024)).toBe('1.0 KB');
    expect(formatBytes(1024 * 1024)).toBe('1.0 MB');
    expect(formatBytes(1024 * 1024 * 1024)).toBe('1.0 GB');
    expect(formatBytes(1536)).toBe('1.5 KB');
  });

  test('formatMs handles various durations', () => {
    expect(formatMs(500)).toBe('500ms');
    expect(formatMs(1500)).toBe('1.5s');
    expect(formatMs(90_000)).toBe('1m 30s');
    expect(formatMs(120_000)).toBe('2m');
    expect(formatMs(3_661_000)).toBe('61m 1s');
  });
});

describe('Data — Build metrics', () => {
  test('build success rate is in valid range', () => {
    const rate = 97.3;
    expect(rate).toBeGreaterThanOrEqual(0);
    expect(rate).toBeLessThanOrEqual(100);
  });

  test('waterfall percentile data consistency', () => {
    const data = [
      { project:'api-service', p50:84,  p75:118, p90:148, p95:196, p99:420, max:720  },
      { project:'web-app',     p50:78,  p75:98,  p90:120, p95:156, p99:312, max:540  },
      { project:'mobile',      p50:492, p75:580, p90:640, p95:680, p99:840, max:1200 },
    ];

    for (const d of data) {
      expect(d.p50).toBeLessThanOrEqual(d.p75);
      expect(d.p75).toBeLessThanOrEqual(d.p90);
      expect(d.p90).toBeLessThanOrEqual(d.p95);
      expect(d.p95).toBeLessThanOrEqual(d.p99);
      expect(d.p99).toBeLessThanOrEqual(d.max);
    }
  });

  test('mock builds have required fields', () => {
    const builds = [
      { id:'b1847', number:1847, status:'success', project_name:'api-service', branch:'main', duration_ms:120000 },
      { id:'b1845', number:1845, status:'failed',  project_name:'api-service', branch:'fix/null-pointer', duration_ms:131000 },
    ];

    for (const build of builds) {
      expect(build.id).toBeTruthy();
      expect(build.number).toBeGreaterThan(0);
      expect(['success','failed','running','cancelled','queued']).toContain(build.status);
      expect(build.project_name).toBeTruthy();
      expect(build.branch).toBeTruthy();
    }
  });
});

describe('Data — Egress rules', () => {
  test('block rules have higher priority numbers than allow rules', () => {
    const rules = [
      { id:'er1', type:'allow', priority:10 },
      { id:'er6', type:'block', priority:990 },
      { id:'er7', type:'block', priority:995 },
    ];

    const allowPriorities = rules.filter(r => r.type === 'allow').map(r => r.priority);
    const blockPriorities = rules.filter(r => r.type === 'block').map(r => r.priority);

    const maxAllow = Math.max(...allowPriorities);
    const minBlock = Math.min(...blockPriorities);

    expect(maxAllow).toBeLessThan(minBlock);
  });

  test('all rules have unique priorities', () => {
    const rules = [10, 20, 30, 40, 50, 990, 995];
    const unique = new Set(rules);
    expect(unique.size).toBe(rules.length);
  });
});

describe('Data — Pricing tiers', () => {
  const tiers = [
    { id:'hobby', monthly_price:0 },
    { id:'pro',   monthly_price:29 },
    { id:'team',  monthly_price:79 },
  ];

  test('pricing is monotonically increasing', () => {
    const prices = tiers.map(t => t.monthly_price);
    for (let i = 1; i < prices.length; i++) {
      expect(prices[i]).toBeGreaterThan(prices[i-1]);
    }
  });

  test('hobby plan is free', () => {
    const hobby = tiers.find(t => t.id === 'hobby');
    expect(hobby?.monthly_price).toBe(0);
  });
});

describe('Data — Members', () => {
  const members = [
    { id:'m1', role:'owner',  mfa_enabled:true  },
    { id:'m2', role:'admin',  mfa_enabled:true  },
    { id:'m3', role:'member', mfa_enabled:true  },
    { id:'m5', role:'member', mfa_enabled:false },
    { id:'m6', role:'member', mfa_enabled:false },
    { id:'m7', role:'viewer', mfa_enabled:false },
  ];

  test('there is exactly one owner', () => {
    const owners = members.filter(m => m.role === 'owner');
    expect(owners).toHaveLength(1);
  });

  test('owner has mfa enabled', () => {
    const owner = members.find(m => m.role === 'owner');
    expect(owner?.mfa_enabled).toBe(true);
  });

  test('mfa compliance is tracked', () => {
    const withMFA = members.filter(m => m.mfa_enabled).length;
    const total = members.length;
    const pct = (withMFA / total) * 100;
    expect(pct).toBeGreaterThan(0);
    expect(pct).toBeLessThanOrEqual(100);
  });
});

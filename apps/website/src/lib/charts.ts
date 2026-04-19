// ============================================================
// FORGE CI — Chart Utilities
// Pure TypeScript — no DOM deps — SSR safe
// ============================================================

import type { MetricDataPoint, PercentileData } from '../types/index.ts';

// ── SVG Path Generators ────────────────────────────────────

export interface ChartBounds {
  width: number;
  height: number;
  paddingTop?: number;
  paddingBottom?: number;
  paddingLeft?: number;
  paddingRight?: number;
}

export interface DataRange {
  min: number;
  max: number;
}

/**
 * Convert data series to an SVG polyline points string
 */
export function seriesToPoints(
  data: MetricDataPoint[],
  bounds: ChartBounds,
  range?: DataRange,
): string {
  if (data.length === 0) return '';
  const { width, height, paddingTop = 4, paddingBottom = 4, paddingLeft = 0, paddingRight = 0 } = bounds;
  const w = width - paddingLeft - paddingRight;
  const h = height - paddingTop - paddingBottom;

  const values = data.map(d => d.value);
  const min = range?.min ?? Math.min(...values);
  const max = range?.max ?? Math.max(...values);
  const valueRange = max - min || 1;

  return data.map((point, i) => {
    const x = paddingLeft + (i / (data.length - 1)) * w;
    const y = paddingTop + h - ((point.value - min) / valueRange) * h;
    return `${x.toFixed(2)},${y.toFixed(2)}`;
  }).join(' ');
}

/**
 * Convert data series to a closed SVG path for area fill
 */
export function seriesToAreaPath(
  data: MetricDataPoint[],
  bounds: ChartBounds,
  range?: DataRange,
): string {
  if (data.length === 0) return '';
  const { width, height, paddingTop = 4, paddingBottom = 4, paddingLeft = 0, paddingRight = 0 } = bounds;
  const w = width - paddingLeft - paddingRight;
  const h = height - paddingTop - paddingBottom;

  const values = data.map(d => d.value);
  const min = range?.min ?? Math.min(...values);
  const max = range?.max ?? Math.max(...values);
  const valueRange = max - min || 1;

  const lastX = paddingLeft + w;
  const baseline = paddingTop + h;

  const linePoints = data.map((point, i) => {
    const x = paddingLeft + (i / (data.length - 1)) * w;
    const y = paddingTop + h - ((point.value - min) / valueRange) * h;
    return `${x.toFixed(2)},${y.toFixed(2)}`;
  });

  return `M${linePoints.join('L')}L${lastX},${baseline}L${paddingLeft},${baseline}Z`;
}

/**
 * Generate smooth cubic bezier curve path through data points
 */
export function seriesToSmoothPath(
  data: MetricDataPoint[],
  bounds: ChartBounds,
  range?: DataRange,
): string {
  if (data.length < 2) return '';
  const { width, height, paddingTop = 4, paddingBottom = 4, paddingLeft = 0, paddingRight = 0 } = bounds;
  const w = width - paddingLeft - paddingRight;
  const h = height - paddingTop - paddingBottom;

  const values = data.map(d => d.value);
  const min = range?.min ?? Math.min(...values);
  const max = range?.max ?? Math.max(...values);
  const valueRange = max - min || 1;

  const points = data.map((point, i) => ({
    x: paddingLeft + (i / (data.length - 1)) * w,
    y: paddingTop + h - ((point.value - min) / valueRange) * h,
  }));

  let path = `M${points[0].x.toFixed(2)},${points[0].y.toFixed(2)}`;
  for (let i = 1; i < points.length; i++) {
    const prev = points[i - 1];
    const curr = points[i];
    const cpx = (prev.x + curr.x) / 2;
    path += `C${cpx.toFixed(2)},${prev.y.toFixed(2)},${cpx.toFixed(2)},${curr.y.toFixed(2)},${curr.x.toFixed(2)},${curr.y.toFixed(2)}`;
  }
  return path;
}

// ── Axis Tick Generation ───────────────────────────────────

export interface AxisTick {
  value: number;
  label: string;
  position: number; // 0–1 normalized
}

/**
 * Generate nice axis ticks for a value range
 */
export function generateTicks(
  min: number,
  max: number,
  targetCount = 5,
  formatter?: (v: number) => string,
): AxisTick[] {
  const range = max - min;
  if (range === 0) return [{ value: min, label: fmt(min), position: 0.5 }];

  const step = niceStep(range / targetCount);
  const niceMin = Math.floor(min / step) * step;
  const niceMax = Math.ceil(max / step) * step;

  const ticks: AxisTick[] = [];
  let v = niceMin;
  while (v <= niceMax + step * 0.01) {
    ticks.push({
      value: v,
      label: formatter ? formatter(v) : fmt(v),
      position: (v - min) / range,
    });
    v += step;
  }
  return ticks;

  function fmt(n: number): string {
    if (Math.abs(n) >= 1_000_000) return `${(n/1_000_000).toFixed(1)}M`;
    if (Math.abs(n) >= 1_000) return `${(n/1_000).toFixed(1)}k`;
    return n.toFixed(Math.abs(n) < 10 ? 1 : 0);
  }
}

function niceStep(roughStep: number): number {
  const magnitude = Math.pow(10, Math.floor(Math.log10(roughStep)));
  const normalized = roughStep / magnitude;
  if (normalized <= 1) return magnitude;
  if (normalized <= 2) return 2 * magnitude;
  if (normalized <= 5) return 5 * magnitude;
  return 10 * magnitude;
}

// ── Stacked Bar Chart ──────────────────────────────────────

export interface StackedBarSegment {
  label: string;
  color: string;
  values: number[];
}

export interface StackedBarRect {
  x: number;
  y: number;
  width: number;
  height: number;
  color: string;
  label: string;
  value: number;
  barIndex: number;
}

export function generateStackedBars(
  segments: StackedBarSegment[],
  bounds: ChartBounds,
  barCount: number,
  gap = 2,
): StackedBarRect[] {
  const { width, height, paddingTop = 0, paddingBottom = 0, paddingLeft = 0, paddingRight = 0 } = bounds;
  const usableWidth = width - paddingLeft - paddingRight;
  const usableHeight = height - paddingTop - paddingBottom;
  const barWidth = (usableWidth / barCount) - gap;

  // Find max total per bar
  const totals = Array.from({ length: barCount }, (_, i) =>
    segments.reduce((s, seg) => s + (seg.values[i] ?? 0), 0)
  );
  const maxTotal = Math.max(...totals, 1);

  const rects: StackedBarRect[] = [];

  for (let b = 0; b < barCount; b++) {
    let yOffset = 0;
    const x = paddingLeft + b * (barWidth + gap);

    for (const seg of segments) {
      const val = seg.values[b] ?? 0;
      const h = (val / maxTotal) * usableHeight;
      const y = paddingTop + usableHeight - yOffset - h;
      if (h > 0) {
        rects.push({ x, y, width: barWidth, height: h, color: seg.color, label: seg.label, value: val, barIndex: b });
      }
      yOffset += h;
    }
  }
  return rects;
}

// ── Waterfall / Percentile Chart ───────────────────────────

export interface WaterfallBar {
  project: string;
  x: number;
  y_p50: number;
  height_p50_to_p75: number;
  height_p75_to_p90: number;
  height_p90_to_p95: number;
  height_p95_to_p99: number;
  y_max: number;
  width: number;
  label_p50: string;
  label_p95: string;
  label_p99: string;
}

export function generateWaterfallBars(
  data: PercentileData[],
  bounds: ChartBounds,
): WaterfallBar[] {
  const { width, height, paddingTop = 10, paddingBottom = 30, paddingLeft = 60, paddingRight = 10 } = bounds;
  const usableWidth = width - paddingLeft - paddingRight;
  const usableHeight = height - paddingTop - paddingBottom;

  const maxVal = Math.max(...data.map(d => d.max));
  const barWidth = (usableWidth / data.length) * 0.6;
  const barGap = usableWidth / data.length;

  const scale = (v: number) => usableHeight - (v / maxVal) * usableHeight;

  return data.map((d, i) => {
    const x = paddingLeft + i * barGap + (barGap - barWidth) / 2;
    const fmt = (ms: number) => ms >= 60000 ? `${(ms/60000).toFixed(1)}m` : `${(ms/1000).toFixed(0)}s`;

    return {
      project: d.project,
      x,
      y_p50: paddingTop + scale(d.p50),
      height_p50_to_p75: Math.abs(scale(d.p50) - scale(d.p75)),
      height_p75_to_p90: Math.abs(scale(d.p75) - scale(d.p90)),
      height_p90_to_p95: Math.abs(scale(d.p90) - scale(d.p95)),
      height_p95_to_p99: Math.abs(scale(d.p95) - scale(d.p99)),
      y_max: paddingTop + scale(d.max),
      width: barWidth,
      label_p50: fmt(d.p50),
      label_p95: fmt(d.p95),
      label_p99: fmt(d.p99),
    };
  });
}

// ── Heatmap ────────────────────────────────────────────────

export interface HeatCell {
  day: number;   // 0=Mon, 6=Sun
  hour: number;  // 0–23
  value: number; // 0–1 normalized intensity
  raw: number;   // raw build count
}

export function generateHeatmapData(
  days = 7,
  hours = 24,
  seed = 42,
): HeatCell[] {
  const cells: HeatCell[] = [];
  let s = seed;
  const rand = () => { s = (s * 1664525 + 1013904223) & 0xffffffff; return (s >>> 0) / 0xffffffff; };

  for (let day = 0; day < days; day++) {
    const isWeekend = day >= 5;
    for (let hour = 0; hour < hours; hour++) {
      const isPeak = hour >= 9 && hour <= 17 && !isWeekend;
      const base = isPeak ? 30 : isWeekend ? 3 : 8;
      const noise = rand() * base * 0.5;
      const raw = Math.round(base + noise);
      cells.push({ day, hour, raw, value: 0 }); // normalize below
    }
  }

  const maxRaw = Math.max(...cells.map(c => c.raw));
  cells.forEach(c => c.value = c.raw / maxRaw);
  return cells;
}

// ── Resource Timeline ──────────────────────────────────────

export interface ResourcePoint {
  t: number;  // seconds from build start
  cpu: number;
  mem_mb: number;
  net_in_mb: number;
  net_out_mb: number;
}

/**
 * Generate realistic resource timeline for a build step
 */
export function generateResourceTimeline(
  duration_s: number,
  cpu_avg: number,
  mem_avg_mb: number,
  seed = 1,
): ResourcePoint[] {
  const points: ResourcePoint[] = [];
  const steps = Math.min(duration_s, 60);
  const step_s = duration_s / steps;
  let s = seed;
  const rand = () => { s = (s * 1664525 + 1013904223) & 0xffffffff; return (s >>> 0) / 0xffffffff; };

  let cpu = cpu_avg * 0.2;
  let mem = mem_avg_mb * 0.6;

  for (let i = 0; i <= steps; i++) {
    const phase = i / steps;
    // Ramp up, sustain, ramp down
    const envelope = phase < 0.15
      ? phase / 0.15
      : phase > 0.85
      ? (1 - phase) / 0.15
      : 1;

    cpu = Math.min(100, Math.max(0, cpu_avg * envelope + (rand() - 0.5) * 15));
    mem = Math.max(0, mem_avg_mb * (0.7 + phase * 0.3) + (rand() - 0.5) * mem_avg_mb * 0.1);

    points.push({
      t: i * step_s,
      cpu: Math.round(cpu * 10) / 10,
      mem_mb: Math.round(mem),
      net_in_mb: Math.round(rand() * 10 * 10) / 10,
      net_out_mb: Math.round(rand() * 5 * 10) / 10,
    });
  }
  return points;
}

// ── Log search / filter ────────────────────────────────────

import type { LogLine } from '../types/index.ts';

export interface LogSearchOptions {
  query?: string;
  levels?: Array<'info' | 'warn' | 'error' | 'debug'>;
  stream?: 'stdout' | 'stderr';
  caseSensitive?: boolean;
  regex?: boolean;
}

export function filterLogLines(lines: LogLine[], opts: LogSearchOptions): {
  lines: LogLine[];
  matchCount: number;
  matchedIndices: number[];
} {
  const matchedIndices: number[] = [];
  const filtered = lines.filter((line, i) => {
    // Level filter
    if (opts.levels?.length && line.level && !opts.levels.includes(line.level)) return false;
    // Stream filter
    if (opts.stream && line.stream !== opts.stream) return false;

    // Query filter
    if (opts.query) {
      let match = false;
      if (opts.regex) {
        try {
          const re = new RegExp(opts.query, opts.caseSensitive ? '' : 'i');
          match = re.test(line.text);
        } catch { match = false; }
      } else {
        match = opts.caseSensitive
          ? line.text.includes(opts.query)
          : line.text.toLowerCase().includes(opts.query.toLowerCase());
      }
      if (!match) return false;
    }

    matchedIndices.push(i);
    return true;
  });

  return { lines: filtered, matchCount: matchedIndices.length, matchedIndices };
}

/**
 * Highlight search matches in a log line text
 * Returns array of {text, highlight} segments
 */
export function highlightMatches(
  text: string,
  query: string,
  caseSensitive = false,
): Array<{ text: string; highlight: boolean }> {
  if (!query) return [{ text, highlight: false }];

  const flags = caseSensitive ? 'g' : 'gi';
  let re: RegExp;
  try {
    re = new RegExp(query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), flags);
  } catch {
    return [{ text, highlight: false }];
  }

  const segments: Array<{ text: string; highlight: boolean }> = [];
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = re.exec(text)) !== null) {
    if (match.index > lastIndex) {
      segments.push({ text: text.slice(lastIndex, match.index), highlight: false });
    }
    segments.push({ text: match[0], highlight: true });
    lastIndex = match.index + match[0].length;
    if (re.lastIndex === match.index) re.lastIndex++;
  }

  if (lastIndex < text.length) {
    segments.push({ text: text.slice(lastIndex), highlight: false });
  }

  return segments.length ? segments : [{ text, highlight: false }];
}

// ── Format helpers ─────────────────────────────────────────

export function formatMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.floor((ms % 60_000) / 1000);
  return s > 0 ? `${m}m ${s}s` : `${m}m`;
}

export function formatBytes(bytes: number, decimals = 1): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B','KB','MB','GB','TB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
  return `${(bytes / Math.pow(k, i)).toFixed(decimals)} ${sizes[i]}`;
}

export function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return n.toString();
}

export function pct(used: number, total: number): number {
  if (total === 0) return 0;
  return Math.round((used / total) * 100 * 10) / 10;
}

export function clamp(v: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, v));
}

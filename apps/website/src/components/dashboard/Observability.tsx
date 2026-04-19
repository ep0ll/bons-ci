/**
 * Forge CI — React Observability Components
 * Uses: recharts, @xyflow/react, d3
 * Neo-brutalist theme: #0A0A0A bg, #FFEE00 accent, 0px border-radius
 */

// ─── Theme tokens (shared) ────────────────────────────────────
export const BRUT = {
  bg: '#0A0A0A',
  surface: '#111111',
  s2: '#1A1A1A',
  s3: '#222222',
  border: '#2C2C2C',
  border2: '#3C3C3C',
  accent: '#FFEE00',
  green: '#00FF88',
  red: '#FF3333',
  orange: '#FF7700',
  blue: '#33AAFF',
  cyan: '#00EEFF',
  purple: '#CC44FF',
  text: '#F0F0F0',
  t2: '#AAAAAA',
  t3: '#666666',
  t4: '#3C3C3C',
} as const;

// ─── recharts: Build Metrics Line Chart ───────────────────────
import React, { useState } from 'react';
import {
  ResponsiveContainer, LineChart, AreaChart, BarChart,
  Line, Area, Bar, CartesianGrid, XAxis, YAxis, Tooltip, Legend,
  ReferenceLine, ComposedChart, Scatter,
} from 'recharts';

interface MetricPoint { date: string; value: number; label?: string; }

interface BuildMetricsChartProps {
  data: MetricPoint[];
  color?: string;
  label?: string;
  type?: 'line' | 'area' | 'bar';
  height?: number;
  showGrid?: boolean;
  unit?: string;
}

const CustomTooltip = ({ active, payload, label, unit }: any) => {
  if (!active || !payload?.length) return null;
  return (
    <div style={{
      background: BRUT.s2, border: `2px solid ${BRUT.border2}`,
      padding: '8px 12px', borderRadius: 0,
      boxShadow: `3px 3px 0 ${BRUT.border}`,
      fontFamily: '"JetBrains Mono", monospace', fontSize: 11,
    }}>
      <p style={{ color: BRUT.t3, marginBottom: 4 }}>{label}</p>
      {payload.map((p: any, i: number) => (
        <p key={i} style={{ color: p.color, fontWeight: 700 }}>
          {p.name}: {typeof p.value === 'number' ? p.value.toFixed(1) : p.value}{unit ?? ''}
        </p>
      ))}
    </div>
  );
};

export function BuildMetricsChart({ data, color = BRUT.accent, label = 'Value', type = 'area', height = 180, showGrid = true, unit }: BuildMetricsChartProps) {
  const tickStyle = { fill: BRUT.t4, fontSize: 10, fontFamily: '"JetBrains Mono", monospace' };
  const gridStyle = { stroke: BRUT.border, strokeDasharray: '2 4' };

  const commonProps = {
    data, margin: { top: 4, right: 4, left: -16, bottom: 0 },
  };
  const axisProps = {
    style: tickStyle,
    tick: tickStyle,
    axisLine: { stroke: BRUT.border },
    tickLine: false,
  };

  return (
    <ResponsiveContainer width="100%" height={height}>
      {type === 'bar' ? (
        <BarChart {...commonProps}>
          {showGrid && <CartesianGrid {...gridStyle} />}
          <XAxis dataKey="date" {...axisProps} />
          <YAxis {...axisProps} />
          <Tooltip content={<CustomTooltip unit={unit} />} />
          <Bar dataKey="value" name={label} fill={color} radius={0} maxBarSize={20}
            style={{ filter: `drop-shadow(0 0 4px ${color}40)` }} />
        </BarChart>
      ) : type === 'area' ? (
        <AreaChart {...commonProps}>
          <defs>
            <linearGradient id={`grad-${label}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity={0.2} />
              <stop offset="100%" stopColor={color} stopOpacity={0} />
            </linearGradient>
          </defs>
          {showGrid && <CartesianGrid {...gridStyle} />}
          <XAxis dataKey="date" {...axisProps} />
          <YAxis {...axisProps} />
          <Tooltip content={<CustomTooltip unit={unit} />} />
          <Area dataKey="value" name={label} stroke={color} fill={`url(#grad-${label})`}
            strokeWidth={2} dot={false} activeDot={{ r: 3, fill: color, stroke: BRUT.bg, strokeWidth: 2 }} />
        </AreaChart>
      ) : (
        <LineChart {...commonProps}>
          {showGrid && <CartesianGrid {...gridStyle} />}
          <XAxis dataKey="date" {...axisProps} />
          <YAxis {...axisProps} />
          <Tooltip content={<CustomTooltip unit={unit} />} />
          <Line dataKey="value" name={label} stroke={color} strokeWidth={2}
            dot={false} activeDot={{ r: 3, fill: color }} />
        </LineChart>
      )}
    </ResponsiveContainer>
  );
}

// ─── recharts: Multi-Series Build Duration Chart ──────────────
interface DurationSeries { name: string; p50: number; p90: number; p99: number; }

export function BuildDurationChart({ data, height = 200 }: { data: DurationSeries[]; height?: number }) {
  const tickStyle = { fill: BRUT.t4, fontSize: 10, fontFamily: '"JetBrains Mono", monospace' };
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={data} margin={{ top: 4, right: 4, left: -16, bottom: 0 }}>
        <CartesianGrid stroke={BRUT.border} strokeDasharray="2 4" />
        <XAxis dataKey="name" tick={tickStyle} axisLine={{ stroke: BRUT.border }} tickLine={false} />
        <YAxis tick={tickStyle} axisLine={{ stroke: BRUT.border }} tickLine={false}
          tickFormatter={(v: number) => v >= 60000 ? `${(v / 60000).toFixed(0)}m` : `${(v / 1000).toFixed(0)}s`} />
        <Tooltip content={<CustomTooltip unit="ms" />} />
        <Legend wrapperStyle={{ fontFamily: '"JetBrains Mono", monospace', fontSize: 11, color: BRUT.t3 }} />
        <Bar dataKey="p50" name="P50" fill={BRUT.green} radius={0} maxBarSize={14} />
        <Bar dataKey="p90" name="P90" fill={BRUT.orange} radius={0} maxBarSize={14} />
        <Bar dataKey="p99" name="P99" fill={BRUT.red} radius={0} maxBarSize={14} />
      </ComposedChart>
    </ResponsiveContainer>
  );
}

// ─── recharts: Success Rate with Control Bands ────────────────
interface SuccessPoint { date: string; rate: number; baseline: number; lower: number; upper: number; }

export function SuccessRateChart({ data, height = 160 }: { data: SuccessPoint[]; height?: number }) {
  const tickStyle = { fill: BRUT.t4, fontSize: 10, fontFamily: '"JetBrains Mono", monospace' };
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={data} margin={{ top: 4, right: 4, left: -16, bottom: 0 }}>
        <defs>
          <linearGradient id="rateGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={BRUT.green} stopOpacity={0.15} />
            <stop offset="100%" stopColor={BRUT.green} stopOpacity={0} />
          </linearGradient>
          <linearGradient id="controlGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={BRUT.t4} stopOpacity={0.1} />
            <stop offset="100%" stopColor={BRUT.t4} stopOpacity={0} />
          </linearGradient>
        </defs>
        <CartesianGrid stroke={BRUT.border} strokeDasharray="2 4" />
        <XAxis dataKey="date" tick={tickStyle} axisLine={{ stroke: BRUT.border }} tickLine={false} />
        <YAxis tick={tickStyle} axisLine={{ stroke: BRUT.border }} tickLine={false}
          domain={[80, 100]} tickFormatter={(v: number) => `${v}%`} />
        <Tooltip content={<CustomTooltip unit="%" />} />
        <ReferenceLine y={95} stroke={BRUT.orange} strokeDasharray="4 4" label={{ value: 'SLA', fill: BRUT.orange, fontSize: 10 }} />
        <Area dataKey="upper" name="Upper bound" stroke="none" fill={`url(#controlGrad)`} dot={false} />
        <Area dataKey="lower" name="Lower bound" stroke="none" fill={BRUT.bg} dot={false} />
        <Area dataKey="rate" name="Success rate" stroke={BRUT.green} fill={`url(#rateGrad)`}
          strokeWidth={2} dot={false} activeDot={{ r: 3, fill: BRUT.green }} />
        <Line dataKey="baseline" name="Baseline" stroke={BRUT.t4} strokeWidth={1} strokeDasharray="3 3" dot={false} />
      </AreaChart>
    </ResponsiveContainer>
  );
}

// ─── recharts: Stacked Build Volume Bar Chart ─────────────────
interface VolumePoint { date: string; success: number; failed: number; cancelled: number; }

export function BuildVolumeBarChart({ data, height = 180 }: { data: VolumePoint[]; height?: number }) {
  const tickStyle = { fill: BRUT.t4, fontSize: 10, fontFamily: '"JetBrains Mono", monospace' };
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={data} margin={{ top: 4, right: 4, left: -16, bottom: 0 }} barCategoryGap="30%">
        <CartesianGrid stroke={BRUT.border} strokeDasharray="2 4" />
        <XAxis dataKey="date" tick={tickStyle} axisLine={{ stroke: BRUT.border }} tickLine={false} />
        <YAxis tick={tickStyle} axisLine={{ stroke: BRUT.border }} tickLine={false} />
        <Tooltip content={<CustomTooltip />} />
        <Legend wrapperStyle={{ fontFamily: '"JetBrains Mono", monospace', fontSize: 11, color: BRUT.t3 }} />
        <Bar dataKey="success" name="Passed" stackId="a" fill={BRUT.green} radius={0} />
        <Bar dataKey="failed" name="Failed" stackId="a" fill={BRUT.red} radius={0} />
        <Bar dataKey="cancelled" name="Cancelled" stackId="a" fill={BRUT.border2} radius={0} />
      </BarChart>
    </ResponsiveContainer>
  );
}

// ─── @xyflow/react: Pipeline DAG Component ────────────────────
import { ReactFlow, Background, Controls, MiniMap, Handle, Position, type Node, type Edge, type NodeProps } from '@xyflow/react';

interface PipelineStep {
  id: string;
  label: string;
  status: 'success' | 'failed' | 'running' | 'queued' | 'skipped';
  duration?: string;
  deps?: string[];
  parallelGroup?: string;
}

const STATUS_COLOR: Record<string, { border: string; bg: string; text: string; dot: string }> = {
  success: { border: BRUT.green, bg: 'rgba(0,255,136,0.06)', text: BRUT.green, dot: BRUT.green },
  failed: { border: BRUT.red, bg: 'rgba(255,51,51,0.06)', text: BRUT.red, dot: BRUT.red },
  running: { border: BRUT.cyan, bg: 'rgba(0,238,255,0.06)', text: BRUT.cyan, dot: BRUT.cyan },
  queued: { border: BRUT.t4, bg: 'rgba(60,60,60,0.2)', text: BRUT.t3, dot: BRUT.t4 },
  skipped: { border: BRUT.t4, bg: 'rgba(44,44,44,0.1)', text: BRUT.t4, dot: BRUT.t4 },
};

function PipelineNode({ data }: NodeProps) {
  const s = STATUS_COLOR[data.status as string] ?? STATUS_COLOR.queued;
  return (
    <div style={{
      background: s.bg, border: `2px solid ${s.border}`, borderRadius: 0,
      padding: '8px 12px', minWidth: 140, fontFamily: '"Space Grotesk", sans-serif',
      boxShadow: data.status === 'running' ? `0 0 12px ${s.dot}40` : `2px 2px 0 ${BRUT.border}`,
    }}>
      <Handle type="target" position={Position.Left} style={{ background: s.border, border: 'none', borderRadius: 0, width: 6, height: 6 }} />
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 2 }}>
        <div style={{
          width: 7, height: 7, borderRadius: '50%', background: s.dot, flexShrink: 0,
          animation: data.status === 'running' ? 'pulse 1s ease-in-out infinite' : undefined,
        }} />
        <span style={{ fontSize: 11, fontWeight: 700, color: s.text, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
          {data.label as string}
        </span>
      </div>
      {data.duration != null && (
        <div style={{ fontSize: 10, color: BRUT.t3, fontFamily: '"JetBrains Mono", monospace', marginLeft: 13 }}>
          {String(data.duration)}
        </div>
      )}
      <Handle type="source" position={Position.Right} style={{ background: s.border, border: 'none', borderRadius: 0, width: 6, height: 6 }} />
    </div>
  );
}

const nodeTypes = { pipeline: PipelineNode };

function stepsToFlow(steps: PipelineStep[]): { nodes: Node[]; edges: Edge[] } {
  const LEVEL_WIDTH = 200;
  const NODE_HEIGHT = 80;

  // Build dependency graph and compute levels
  const levels: Record<string, number> = {};
  const queue = steps.filter(s => !s.deps?.length);
  queue.forEach(s => { levels[s.id] = 0; });

  for (let iter = 0; iter < 10; iter++) {
    steps.forEach(s => {
      if (s.deps?.length) {
        const maxDepLevel = Math.max(...(s.deps.map(d => levels[d] ?? 0)));
        levels[s.id] = maxDepLevel + 1;
      }
    });
  }

  // Group by level
  const byLevel: Record<number, PipelineStep[]> = {};
  steps.forEach(s => {
    const l = levels[s.id] ?? 0;
    byLevel[l] = [...(byLevel[l] ?? []), s];
  });

  const nodes: Node[] = steps.map(s => {
    const l = levels[s.id] ?? 0;
    const group = byLevel[l];
    const idx = group.indexOf(s);
    return {
      id: s.id,
      type: 'pipeline',
      position: { x: l * LEVEL_WIDTH, y: idx * NODE_HEIGHT },
      data: { label: s.label, status: s.status, duration: s.duration },
    };
  });

  const edges: Edge[] = steps.flatMap(s =>
    (s.deps ?? []).map(dep => ({
      id: `${dep}-${s.id}`,
      source: dep,
      target: s.id,
      style: { stroke: BRUT.border2, strokeWidth: 1.5 },
      markerEnd: { type: 'arrowclosed' as any, color: BRUT.border2, width: 12, height: 12 },
      animated: s.status === 'running',
    }))
  );

  return { nodes, edges };
}

interface PipelineDAGProps {
  steps: PipelineStep[];
  height?: number;
}

export function PipelineDAG({ steps, height = 320 }: PipelineDAGProps) {
  const { nodes, edges } = stepsToFlow(steps);

  return (
    <div style={{ height, background: BRUT.bg, border: `2px solid ${BRUT.border}` }}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        proOptions={{ hideAttribution: true }}
        style={{ background: BRUT.bg }}
      >
        <Background color={BRUT.border} gap={20} size={1} />
        <Controls style={{
          background: BRUT.s2, border: `1px solid ${BRUT.border}`, borderRadius: 0,
          color: BRUT.t2,
        }} />
        <MiniMap
          style={{ background: BRUT.s2, border: `1px solid ${BRUT.border}`, borderRadius: 0 }}
          nodeColor={(n) => STATUS_COLOR[(n.data as any).status ?? 'queued'].border}
          maskColor="rgba(0,0,0,0.6)"
        />
      </ReactFlow>
    </div>
  );
}

// ─── Span Waterfall (pure React + CSS) ───────────────────────
interface Span {
  id: string;
  name: string;
  startMs: number;
  durationMs: number;
  status: 'success' | 'failed' | 'running' | 'skipped';
  depth?: number;
  children?: Span[];
}

export function SpanWaterfall({ spans, totalMs, height = 320 }: { spans: Span[]; totalMs: number; height?: number }) {
  const [selected, setSelected] = useState<string | null>(null);

  function renderSpan(span: Span, depth = 0): React.ReactNode {
    const leftPct = (span.startMs / totalMs) * 100;
    const widthPct = Math.max((span.durationMs / totalMs) * 100, 0.5);
    const col = STATUS_COLOR[span.status];
    const isSelected = selected === span.id;

    return (
      <div key={span.id}>
        <div style={{ display: 'flex', alignItems: 'center', height: 26, borderBottom: `1px solid ${BRUT.border}`, cursor: 'pointer' }}
          onClick={() => setSelected(isSelected ? null : span.id)}>
          <div style={{
            flexShrink: 0, width: 180, paddingLeft: 8 + depth * 16,
            fontFamily: '"JetBrains Mono", monospace', fontSize: 10,
            color: isSelected ? BRUT.accent : BRUT.t2, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
          }}>
            {span.name}
          </div>
          <div style={{ flex: 1, position: 'relative', margin: '0 8px' }}>
            <div style={{
              position: 'absolute', left: `${leftPct}%`, width: `${widthPct}%`,
              height: 14, top: '50%', transform: 'translateY(-50%)',
              background: col.bg, border: `1px solid ${col.border}`,
              display: 'flex', alignItems: 'center', paddingLeft: 4, overflow: 'hidden',
            }}>
              {widthPct > 5 && (
                <span style={{ fontSize: 9, fontFamily: '"JetBrains Mono", monospace', color: col.text, fontWeight: 700, whiteSpace: 'nowrap' }}>
                  {span.durationMs >= 1000 ? `${(span.durationMs / 1000).toFixed(1)}s` : `${span.durationMs}ms`}
                </span>
              )}
            </div>
          </div>
          <div style={{ flexShrink: 0, width: 60, textAlign: 'right', paddingRight: 8, fontFamily: '"JetBrains Mono", monospace', fontSize: 9, color: BRUT.t4 }}>
            {span.durationMs >= 1000 ? `${(span.durationMs / 1000).toFixed(2)}s` : `${span.durationMs}ms`}
          </div>
        </div>
        {span.children?.map(c => renderSpan(c, depth + 1))}
      </div>
    );
  }

  return (
    <div style={{ border: `2px solid ${BRUT.border}`, background: BRUT.bg, height, overflowY: 'auto' }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', height: 28, borderBottom: `2px solid ${BRUT.border2}`, background: BRUT.s2, position: 'sticky', top: 0 }}>
        <div style={{ flexShrink: 0, width: 180, paddingLeft: 8, fontFamily: '"JetBrains Mono", monospace', fontSize: 9, color: BRUT.t4, textTransform: 'uppercase', letterSpacing: '0.1em' }}>Name</div>
        <div style={{ flex: 1, fontFamily: '"JetBrains Mono", monospace', fontSize: 9, color: BRUT.t4, textTransform: 'uppercase', letterSpacing: '0.1em', paddingLeft: 8 }}>Timeline (0 → {totalMs >= 60000 ? `${(totalMs / 60000).toFixed(1)}m` : `${(totalMs / 1000).toFixed(1)}s`})</div>
        <div style={{ flexShrink: 0, width: 60, textAlign: 'right', paddingRight: 8, fontFamily: '"JetBrains Mono", monospace', fontSize: 9, color: BRUT.t4 }}>Duration</div>
      </div>
      {spans.map(s => renderSpan(s))}
    </div>
  );
}

// ─── D3 Flame Graph (SVG-based) ───────────────────────────────
interface FlameNode {
  name: string;
  value: number;   // time in ms
  children?: FlameNode[];
  color?: string;
}

// Color scale for flame depth
const FLAME_COLORS = ['#FF3333', '#FF5500', '#FF7700', '#FF9900', '#FFBB00', '#FFEE00'];

function flattenFlame(node: FlameNode, startX: number, totalWidth: number, y: number, depth: number, result: Array<{ x: number; w: number; y: number; d: number; name: string; value: number }> = []) {
  const w = (node.value / (node.children ? node.children.reduce((s, c) => s + c.value, 0) : node.value)) * totalWidth;
  result.push({ x: startX, w, y, d: depth, name: node.name, value: node.value });
  let cx = startX;
  for (const child of node.children ?? []) {
    const cw = (child.value / node.value) * totalWidth;
    flattenFlame(child, cx, cw, y + 20, depth + 1, result);
    cx += cw;
  }
  return result;
}

export function FlameGraph({ root, width = 560, height = 200 }: { root: FlameNode; width?: number; height?: number }) {
  const [hovered, setHovered] = useState<string | null>(null);
  const cells = flattenFlame(root, 0, width, 0, 0);
  const maxDepth = Math.max(...cells.map(c => c.d));
  const svgH = (maxDepth + 1) * 20 + 4;

  return (
    <div style={{ overflow: 'auto', border: `2px solid ${BRUT.border}`, background: BRUT.bg }}>
      <svg width={width} height={Math.min(svgH, height)} style={{ display: 'block' }}>
        {cells.map((cell, i) => {
          const col = FLAME_COLORS[Math.min(cell.d, FLAME_COLORS.length - 1)];
          const isHov = hovered === `${cell.name}-${i}`;
          return (
            <g key={i} onMouseEnter={() => setHovered(`${cell.name}-${i}`)} onMouseLeave={() => setHovered(null)}>
              <rect
                x={cell.x + 0.5} y={cell.y + 0.5}
                width={Math.max(cell.w - 1, 1)} height={19}
                fill={isHov ? BRUT.accent : col}
                stroke={BRUT.bg} strokeWidth={1}
                style={{ cursor: 'pointer' }}
              />
              {cell.w > 30 && (
                <text x={cell.x + 4} y={cell.y + 13}
                  fill={isHov ? BRUT.bg : BRUT.bg} fontSize={9}
                  fontFamily='"JetBrains Mono", monospace' fontWeight={700}
                  clipPath={`inset(0 ${Math.max(0, cell.x + cell.w - (cell.x + cell.w - 4))}px 0 0)`}>
                  <title>{cell.name} — {cell.value}ms</title>
                  {cell.name.length > cell.w / 8 ? cell.name.slice(0, Math.floor(cell.w / 8)) + '…' : cell.name}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      {hovered && (
        <div style={{ padding: '4px 8px', background: BRUT.s2, borderTop: `1px solid ${BRUT.border}`, fontFamily: '"JetBrains Mono", monospace', fontSize: 10, color: BRUT.t2 }}>
          {cells.find((c, i) => `${c.name}-${i}` === hovered)?.name} — {cells.find((c, i) => `${c.name}-${i}` === hovered)?.value}ms
        </div>
      )}
    </div>
  );
}

// ─── Log Viewer Component ─────────────────────────────────────
interface LogLine {
  t: string;   // timestamp
  text: string;
  level?: 'info' | 'warn' | 'error' | 'debug';
}

const LEVEL_COLORS: Record<string, { text: string; bg: string; border: string }> = {
  error: { text: BRUT.red, bg: 'rgba(255,51,51,0.06)', border: BRUT.red },
  warn: { text: BRUT.orange, bg: 'rgba(255,119,0,0.06)', border: BRUT.orange },
  info: { text: BRUT.t2, bg: 'transparent', border: 'transparent' },
  debug: { text: BRUT.t4, bg: 'transparent', border: 'transparent' },
};

export function LogViewer({ lines, height = 400 }: { lines: LogLine[]; height?: number }) {
  const [query, setQuery] = useState('');
  const [level, setLevel] = useState<string | null>(null);
  const [showTs, setShowTs] = useState(true);

  const filtered = lines.filter(l => {
    if (level && l.level !== level) return false;
    if (query) {
      try { return new RegExp(query, 'i').test(l.text); } catch { return l.text.toLowerCase().includes(query.toLowerCase()); }
    }
    return true;
  });

  return (
    <div style={{ border: `2px solid ${BRUT.border}`, background: BRUT.bg, display: 'flex', flexDirection: 'column' }}>
      {/* Controls */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 12px', background: BRUT.s2, borderBottom: `2px solid ${BRUT.border}` }}>
        <input
          type="text" placeholder="Filter (regex)…" value={query}
          onChange={e => setQuery(e.target.value)}
          style={{ flex: 1, background: BRUT.bg, border: `1px solid ${BRUT.border2}`, color: BRUT.text, padding: '3px 8px', fontSize: 11, fontFamily: '"JetBrains Mono", monospace', borderRadius: 0, outline: 'none' }}
        />
        {(['info', 'warn', 'error', 'debug'] as const).map(lvl => (
          <button key={lvl}
            onClick={() => setLevel(level === lvl ? null : lvl)}
            style={{
              background: level === lvl ? LEVEL_COLORS[lvl].border : 'transparent',
              border: `1px solid ${LEVEL_COLORS[lvl].border}`, color: level === lvl ? BRUT.bg : LEVEL_COLORS[lvl].text,
              padding: '2px 8px', fontSize: 10, fontFamily: '"JetBrains Mono", monospace',
              cursor: 'pointer', borderRadius: 0, textTransform: 'uppercase',
            }}>
            {lvl}
          </button>
        ))}
        <button onClick={() => setShowTs(!showTs)}
          style={{ background: 'transparent', border: `1px solid ${BRUT.border}`, color: BRUT.t4, padding: '2px 8px', fontSize: 10, fontFamily: '"JetBrains Mono", monospace', cursor: 'pointer', borderRadius: 0 }}>
          HH:MM
        </button>
        <span style={{ fontFamily: '"JetBrains Mono", monospace', fontSize: 10, color: BRUT.t4 }}>{filtered.length} lines</span>
      </div>
      {/* Log body */}
      <div style={{ height, overflowY: 'auto', fontFamily: '"JetBrains Mono", monospace', fontSize: 11, lineHeight: '20px' }}>
        {filtered.map((line, i) => {
          const lc = LEVEL_COLORS[line.level ?? 'info'];
          const isMatch = query && (() => { try { return new RegExp(query, 'i').test(line.text); } catch { return false; } })();
          return (
            <div key={i} style={{
              display: 'flex', gap: 12, padding: '1px 12px',
              background: lc.bg, borderLeft: `2px solid ${lc.border}`,
            }}
              onMouseEnter={e => (e.currentTarget.style.background = BRUT.s2)}
              onMouseLeave={e => (e.currentTarget.style.background = lc.bg)}>
              {showTs && <span style={{ color: BRUT.t4, flexShrink: 0, userSelect: 'none', width: 60 }}>{line.t.slice(11, 19)}</span>}
              <span style={{ color: lc.text, wordBreak: 'break-all' }}>
                {isMatch && query ? line.text.split(new RegExp(`(${query})`, 'gi')).map((part, j) =>
                  part.toLowerCase() === query.toLowerCase()
                    ? <mark key={j} style={{ background: BRUT.accent, color: BRUT.bg }}>{part}</mark>
                    : part
                ) : line.text}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

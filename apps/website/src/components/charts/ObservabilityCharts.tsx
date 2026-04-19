/**
 * Forge CI — HeatmapChart (D3-based)
 * Day-of-week × hour-of-day build activity heatmap
 */
import React, { useState } from 'react';
import { BRUT } from '../dashboard/Observability.tsx';

interface HeatCell {
    day: number;   // 0=Sun, 6=Sat
    hour: number;  // 0-23
    count: number;
    successRate: number;
}

interface HeatmapChartProps {
    data?: HeatCell[];
    title?: string;
}

// Generate deterministic mock heatmap data
function generateHeatData(): HeatCell[] {
    function seeded(seed: number) {
        let s = seed;
        return () => { s = (s * 1664525 + 1013904223) & 0xffffffff; return (s >>> 0) / 0xffffffff; };
    }
    const rand = seeded(1337);
    const cells: HeatCell[] = [];
    for (let day = 0; day < 7; day++) {
        for (let hour = 0; hour < 24; hour++) {
            // Weekdays 9-18 peak, Mon-Fri
            const workday = day >= 1 && day <= 5;
            const workhour = hour >= 9 && hour <= 18;
            const base = workday && workhour ? 12 : workday ? 3 : 1;
            const count = Math.floor(base * (0.5 + rand() * 1.5));
            cells.push({ day, hour, count, successRate: 85 + rand() * 14 });
        }
    }
    return cells;
}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const HOURS = Array.from({ length: 24 }, (_, i) => i);

function countToColor(count: number, max: number): string {
    if (count === 0) return BRUT.s2;
    const t = count / max;
    if (t < 0.25) return '#1A2A1A';
    if (t < 0.50) return '#1A4A1A';
    if (t < 0.75) return '#00AA55';
    return '#00FF88';
}

export function HeatmapChart({ data, title }: HeatmapChartProps) {
    const cells = data ?? generateHeatData();
    const max = Math.max(...cells.map(c => c.count), 1);
    const [tooltip, setTooltip] = useState<{ cell: HeatCell; x: number; y: number } | null>(null);

    const cellW = 22;
    const cellH = 18;
    const labelW = 32;
    const labelH = 20;

    const svgW = labelW + 24 * (cellW + 1);
    const svgH = labelH + 7 * (cellH + 1) + 24; // +24 for hour labels

    return (
        <div style={{ position: 'relative', fontFamily: '"JetBrains Mono", monospace' }}>
            {title && (
                <div style={{ fontSize: 10, color: BRUT.t3, textTransform: 'uppercase', letterSpacing: '0.1em', marginBottom: 8 }}>{title}</div>
            )}
            <div style={{ overflowX: 'auto' }}>
                <svg width={svgW} height={svgH}>
                    {/* Day labels */}
                    {DAYS.map((d, di) => (
                        <text
                            key={d}
                            x={labelW - 4}
                            y={labelH + di * (cellH + 1) + cellH / 2 + 4}
                            textAnchor="end"
                            fontSize={9}
                            fill={BRUT.t4}
                        >
                            {d}
                        </text>
                    ))}

                    {/* Hour labels */}
                    {[0, 6, 12, 18, 23].map(h => (
                        <text
                            key={h}
                            x={labelW + h * (cellW + 1) + cellW / 2}
                            y={labelH + 7 * (cellH + 1) + 14}
                            textAnchor="middle"
                            fontSize={9}
                            fill={BRUT.t4}
                        >
                            {String(h).padStart(2, '0')}h
                        </text>
                    ))}

                    {/* Cells */}
                    {cells.map((cell, i) => {
                        const x = labelW + cell.hour * (cellW + 1);
                        const y = labelH + cell.day * (cellH + 1);
                        const fill = countToColor(cell.count, max);
                        return (
                            <rect
                                key={i}
                                x={x} y={y}
                                width={cellW} height={cellH}
                                fill={fill}
                                stroke={BRUT.border}
                                strokeWidth={0.5}
                                style={{ cursor: 'pointer' }}
                                onMouseEnter={e => setTooltip({ cell, x: e.clientX, y: e.clientY })}
                                onMouseLeave={() => setTooltip(null)}
                            />
                        );
                    })}
                </svg>
            </div>

            {/* Legend */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 8, fontSize: 9, color: BRUT.t4 }}>
                <span>Less</span>
                {['#1A1A1A', '#1A2A1A', '#1A4A1A', '#00AA55', '#00FF88'].map(c => (
                    <div key={c} style={{ width: 12, height: 12, background: c, border: `1px solid ${BRUT.border}` }} />
                ))}
                <span>More</span>
            </div>

            {/* Tooltip */}
            {tooltip && (
                <div style={{
                    position: 'fixed',
                    left: tooltip.x + 12,
                    top: tooltip.y - 10,
                    background: BRUT.s2,
                    border: `2px solid ${BRUT.accent}`,
                    padding: '6px 10px',
                    fontSize: 11,
                    zIndex: 999,
                    pointerEvents: 'none',
                    boxShadow: `2px 2px 0 ${BRUT.border}`,
                }}>
                    <div style={{ color: BRUT.text, fontWeight: 700 }}>
                        {DAYS[tooltip.cell.day]} {String(tooltip.cell.hour).padStart(2, '0')}:00
                    </div>
                    <div style={{ color: BRUT.t2 }}>{tooltip.cell.count} builds</div>
                    <div style={{ color: BRUT.green }}>{tooltip.cell.successRate.toFixed(0)}% success</div>
                </div>
            )}
        </div>
    );
}

// ── Donut / Gauge chart ───────────────────────────────────────
interface DonutSegment {
    label: string;
    value: number;
    color: string;
}

interface DonutChartProps {
    segments: DonutSegment[];
    centerLabel?: string;
    centerValue?: string;
    size?: number;
}

export function DonutChart({ segments, centerLabel, centerValue, size = 120 }: DonutChartProps) {
    const [hovered, setHovered] = useState<string | null>(null);
    const total = segments.reduce((s, x) => s + x.value, 0);
    const r = (size / 2) * 0.7;
    const cx = size / 2;
    const cy = size / 2;
    const strokeW = (size / 2) * 0.28;

    let offset = 0;
    const arcs = segments.map(seg => {
        const pct = total > 0 ? seg.value / total : 0;
        const circumference = 2 * Math.PI * r;
        const arc = {
            seg,
            pct,
            dasharray: circumference,
            dashoffset: circumference * (1 - pct),
            rotate: offset * 360 - 90,
        };
        offset += pct;
        return arc;
    });

    return (
        <div style={{ display: 'inline-block', position: 'relative' }}>
            <svg width={size} height={size}>
                {/* Background track */}
                <circle cx={cx} cy={cy} r={r} fill="none" stroke={BRUT.border} strokeWidth={strokeW} />
                {arcs.map(arc => (
                    <circle
                        key={arc.seg.label}
                        cx={cx} cy={cy} r={r}
                        fill="none"
                        stroke={hovered === arc.seg.label ? BRUT.accent : arc.seg.color}
                        strokeWidth={hovered === arc.seg.label ? strokeW + 2 : strokeW}
                        strokeDasharray={arc.dasharray}
                        strokeDashoffset={arc.dashoffset}
                        transform={`rotate(${arc.rotate} ${cx} ${cy})`}
                        style={{ cursor: 'pointer', transition: 'stroke 0.15s, stroke-width 0.15s' }}
                        onMouseEnter={() => setHovered(arc.seg.label)}
                        onMouseLeave={() => setHovered(null)}
                    />
                ))}
                {/* Center text */}
                {centerValue && (
                    <text x={cx} y={cy - 4} textAnchor="middle" fontSize={size * 0.15} fontWeight={700} fill={BRUT.text} fontFamily='"Space Grotesk", sans-serif'>
                        {centerValue}
                    </text>
                )}
                {centerLabel && (
                    <text x={cx} y={cy + size * 0.1} textAnchor="middle" fontSize={size * 0.08} fill={BRUT.t3} fontFamily='"JetBrains Mono", monospace'>
                        {centerLabel}
                    </text>
                )}
            </svg>
            {/* Legend */}
            <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 3 }}>
                {segments.map(s => (
                    <div key={s.label} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 10, fontFamily: '"JetBrains Mono", monospace', color: BRUT.t2 }}>
                        <div style={{ width: 8, height: 8, background: s.color, flexShrink: 0 }} />
                        <span>{s.label}</span>
                        <span style={{ marginLeft: 'auto', color: BRUT.text, fontWeight: 700 }}>{((s.value / total) * 100).toFixed(0)}%</span>
                    </div>
                ))}
            </div>
        </div>
    );
}

// ── Percentile Bar (p50/p75/p90/p95/p99) ─────────────────────
interface PercentileData {
    project: string;
    p50: number;
    p75: number;
    p90: number;
    p95: number;
    p99: number;
    max: number;
}

export function PercentileBarChart({ data }: { data: PercentileData[] }) {
    const globalMax = Math.max(...data.map(d => d.max));
    const pcts: Array<{ key: keyof PercentileData; color: string; label: string }> = [
        { key: 'p50', color: BRUT.green, label: 'P50' },
        { key: 'p75', color: BRUT.accent, label: 'P75' },
        { key: 'p90', color: BRUT.orange, label: 'P90' },
        { key: 'p95', color: '#FF5500', label: 'P95' },
        { key: 'p99', color: BRUT.red, label: 'P99' },
    ];

    const fmt = (ms: number) => ms >= 60000 ? `${(ms / 60000).toFixed(1)}m` : `${(ms / 1000).toFixed(0)}s`;

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            {/* Legend */}
            <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
                {pcts.map(p => (
                    <div key={p.key} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 10, fontFamily: '"JetBrains Mono", monospace', color: BRUT.t2 }}>
                        <div style={{ width: 8, height: 8, background: p.color }} />
                        {p.label}
                    </div>
                ))}
            </div>

            {data.map(row => (
                <div key={row.project} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                    <div style={{ fontSize: 11, fontFamily: '"JetBrains Mono", monospace', color: BRUT.t2 }}>{row.project}</div>
                    <div style={{ position: 'relative', height: 20, background: BRUT.s2, border: `1px solid ${BRUT.border}` }}>
                        {pcts.map((p, pi) => {
                            const val = row[p.key] as number;
                            const w = (val / globalMax) * 100;
                            return (
                                <div
                                    key={p.key}
                                    title={`${p.label}: ${fmt(val)}`}
                                    style={{
                                        position: 'absolute', left: 0, top: 0,
                                        width: `${w}%`, height: '100%',
                                        background: p.color,
                                        opacity: 1 - pi * 0.12,
                                        mixBlendMode: 'screen',
                                    }}
                                />
                            );
                        })}
                        <div style={{ position: 'absolute', right: 6, top: '50%', transform: 'translateY(-50%)', fontSize: 9, fontFamily: '"JetBrains Mono", monospace', color: BRUT.t4 }}>
                            max {fmt(row.max)}
                        </div>
                    </div>
                    <div style={{ display: 'flex', gap: 12 }}>
                        {pcts.map(p => (
                            <div key={p.key} style={{ fontSize: 9, fontFamily: '"JetBrains Mono", monospace', color: p.color }}>
                                {p.label}: {fmt(row[p.key] as number)}
                            </div>
                        ))}
                    </div>
                </div>
            ))}
        </div>
    );
}

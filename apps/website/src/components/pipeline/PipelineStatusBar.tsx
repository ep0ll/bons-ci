/**
 * Forge CI — Pipeline Status Bar
 *
 * Shows build progress, vertex counts, cache efficiency, and elapsed time.
 */
import React, { useEffect, useState } from 'react';
import type { PipelineVertex, VertexStatus } from '../../types/pipeline';
import { VERTEX_STATUS_COLORS } from '../../types/pipeline';

const FONT_MONO = '"JetBrains Mono", "Fira Code", monospace';
const FONT_SANS = '"Space Grotesk", system-ui, sans-serif';
const BRUT = {
    bg: '#0A0A0A', surface: '#111111', border: '#2C2C2C',
    accent: '#FFEE00', green: '#00FF88', red: '#FF3333', cyan: '#00EEFF',
    t1: '#F0F0F0', t2: '#B0B0B0', t3: '#8A8A8A', t4: '#3C3C3C',
};

function formatElapsed(ms: number): string {
    const s = Math.floor(ms / 1000);
    const m = Math.floor(s / 60);
    const sec = s % 60;
    return m > 0 ? `${m}m ${sec}s` : `${sec}s`;
}

// ── SVG Progress Ring ─────────────────────────────────────────
function ProgressRing({ progress, size = 36, color = BRUT.green }: {
    progress: number; size?: number; color?: string;
}) {
    const r = (size - 4) / 2;
    const circumference = 2 * Math.PI * r;
    const offset = circumference - (progress / 100) * circumference;

    return (
        <svg width={size} height={size} style={{ transform: 'rotate(-90deg)' }}>
            <circle
                cx={size / 2} cy={size / 2} r={r}
                fill="none" stroke={BRUT.border} strokeWidth={3}
            />
            <circle
                cx={size / 2} cy={size / 2} r={r}
                fill="none" stroke={color} strokeWidth={3}
                strokeDasharray={circumference}
                strokeDashoffset={offset}
                strokeLinecap="butt"
                style={{ transition: 'stroke-dashoffset 0.4s ease' }}
            />
        </svg>
    );
}

export function PipelineStatusBar({ buildId, status, vertices, startedAt }: {
    buildId: string;
    status: 'connecting' | 'running' | 'succeeded' | 'failed';
    vertices: PipelineVertex[];
    startedAt: number;
}) {
    const [elapsed, setElapsed] = useState(0);

    useEffect(() => {
        if (status === 'running' || status === 'connecting') {
            const id = setInterval(() => setElapsed(Date.now() - startedAt), 500);
            return () => clearInterval(id);
        }
    }, [status, startedAt]);

    const total = vertices.length;
    const byStatus = vertices.reduce((acc, v) => {
        acc[v.status] = (acc[v.status] ?? 0) + 1;
        return acc;
    }, {} as Record<VertexStatus, number>);

    const completed = (byStatus.completed ?? 0) + (byStatus.cached ?? 0);
    const running = byStatus.running ?? 0;
    const failed = byStatus.failed ?? 0;
    const cached = byStatus.cached ?? 0;
    const progress = total > 0 ? Math.round((completed / total) * 100) : 0;

    const statusConfig = {
        connecting: { label: 'Connecting', color: BRUT.t3, bg: 'transparent' },
        running: { label: 'Running', color: BRUT.accent, bg: 'rgba(255,238,0,0.08)' },
        succeeded: { label: 'Succeeded', color: BRUT.green, bg: 'rgba(0,255,136,0.08)' },
        failed: { label: 'Failed', color: BRUT.red, bg: 'rgba(255,51,51,0.08)' },
    }[status];

    return (
        <div
            style={{
                display: 'flex',
                alignItems: 'center',
                gap: 16,
                padding: '8px 16px',
                borderBottom: `2px solid ${BRUT.border}`,
                background: BRUT.surface,
                flexShrink: 0,
                minHeight: 52,
            }}
        >
            {/* Progress ring */}
            <div style={{ position: 'relative', flexShrink: 0 }}>
                <ProgressRing
                    progress={progress}
                    color={failed > 0 ? BRUT.red : status === 'succeeded' ? BRUT.green : BRUT.accent}
                />
                <span
                    style={{
                        position: 'absolute',
                        inset: 0,
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        fontSize: 10,
                        fontFamily: FONT_MONO,
                        fontWeight: 700,
                        color: BRUT.t1,
                    }}
                >
                    {progress}%
                </span>
            </div>

            {/* Build ID + status */}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span style={{ fontSize: 13, fontWeight: 700, color: BRUT.t1, fontFamily: FONT_SANS }}>
                        Pipeline
                    </span>
                    <span
                        style={{
                            fontSize: 10,
                            fontFamily: FONT_MONO,
                            color: statusConfig.color,
                            background: statusConfig.bg,
                            border: `1px solid ${statusConfig.color}30`,
                            padding: '1px 8px',
                            fontWeight: 700,
                            textTransform: 'uppercase',
                            letterSpacing: '0.08em',
                        }}
                    >
                        {statusConfig.label}
                    </span>
                </div>
                <span style={{ fontSize: 10, fontFamily: FONT_MONO, color: BRUT.t4 }}>
                    {buildId} · LLB DAG solver
                </span>
            </div>

            <div style={{ flex: 1 }} />

            {/* Vertex counts */}
            <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                <StatusChip label="Total" count={total} color={BRUT.t2} />
                {running > 0 && <StatusChip label="Running" count={running} color={BRUT.accent} pulse />}
                <StatusChip label="Done" count={completed} color={BRUT.green} />
                {failed > 0 && <StatusChip label="Failed" count={failed} color={BRUT.red} />}
                {cached > 0 && <StatusChip label="Cached" count={cached} color={BRUT.cyan} />}
            </div>

            {/* Cache efficiency */}
            {total > 0 && (
                <div
                    style={{
                        display: 'flex',
                        flexDirection: 'column',
                        alignItems: 'center',
                        gap: 1,
                        padding: '4px 12px',
                        borderLeft: `1px solid ${BRUT.border}`,
                    }}
                >
                    <span style={{ fontSize: 14, fontFamily: FONT_MONO, fontWeight: 700, color: BRUT.cyan }}>
                        {total > 0 ? Math.round((cached / total) * 100) : 0}%
                    </span>
                    <span style={{ fontSize: 9, fontFamily: FONT_MONO, color: BRUT.t4, textTransform: 'uppercase', letterSpacing: '0.1em' }}>
                        cache hit
                    </span>
                </div>
            )}

            {/* Elapsed time */}
            <div
                style={{
                    display: 'flex',
                    flexDirection: 'column',
                    alignItems: 'center',
                    gap: 1,
                    padding: '4px 12px',
                    borderLeft: `1px solid ${BRUT.border}`,
                }}
            >
                <span
                    style={{
                        fontSize: 14,
                        fontFamily: FONT_MONO,
                        fontWeight: 700,
                        color: status === 'running' ? BRUT.accent : BRUT.t1,
                        animation: status === 'running' ? 'pulse 2s ease-in-out infinite' : undefined,
                    }}
                >
                    {formatElapsed(elapsed)}
                </span>
                <span style={{ fontSize: 9, fontFamily: FONT_MONO, color: BRUT.t4, textTransform: 'uppercase', letterSpacing: '0.1em' }}>
                    elapsed
                </span>
            </div>
        </div>
    );
}

function StatusChip({ label, count, color, pulse }: {
    label: string; count: number; color: string; pulse?: boolean;
}) {
    return (
        <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
            <div
                style={{
                    width: 6,
                    height: 6,
                    borderRadius: '50%',
                    background: color,
                    animation: pulse ? 'pulse 1.2s ease-in-out infinite' : undefined,
                }}
            />
            <span style={{ fontSize: 11, fontFamily: FONT_MONO, color: BRUT.t2 }}>
                <span style={{ fontWeight: 700, color }}>{count}</span>{' '}
                <span style={{ color: BRUT.t4, fontSize: 9 }}>{label}</span>
            </span>
        </div>
    );
}

export default PipelineStatusBar;

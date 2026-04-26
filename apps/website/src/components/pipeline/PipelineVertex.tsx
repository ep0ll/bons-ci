/**
 * Forge CI — Custom @xyflow/react Node for LLB Pipeline Vertices
 *
 * Renders each vertex with:
 * - VertexType icon + color
 * - Status ring (running = animated glow)
 * - Duration badge
 * - Cache hit indicator
 * - Progress bar for transfers
 */
import React from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import {
    type PipelineVertex as PV,
    VERTEX_TYPE_ICONS,
    VERTEX_STATUS_COLORS,
} from '../../types/pipeline';

// ── Brut constants ────────────────────────────────────────────
const FONT_MONO = '"JetBrains Mono", "Fira Code", monospace';
const FONT_SANS = '"Space Grotesk", system-ui, sans-serif';

function formatDuration(ms?: number): string {
    if (!ms) return '';
    if (ms < 1000) return `${ms}ms`;
    return `${(ms / 1000).toFixed(1)}s`;
}

export function PipelineVertexNode({ data, selected }: NodeProps) {
    const v = data as unknown as PV;
    const typeInfo = VERTEX_TYPE_ICONS[v.type] ?? VERTEX_TYPE_ICONS.custom;
    const statusColors = VERTEX_STATUS_COLORS[v.status] ?? VERTEX_STATUS_COLORS.queued;
    const isRunning = v.status === 'running';
    const isCached = v.status === 'cached' || v.cached;

    // Truncate long names
    const label = v.name.length > 42 ? v.name.slice(0, 40) + '…' : v.name;

    return (
        <div
            style={{
                background: statusColors.bg,
                border: `2px solid ${statusColors.border}`,
                borderRadius: 0,
                padding: '10px 14px',
                minWidth: 200,
                maxWidth: 320,
                fontFamily: FONT_SANS,
                boxShadow: isRunning
                    ? statusColors.glow
                    : selected
                        ? `0 0 0 2px #FFEE00, 3px 3px 0 #2C2C2C`
                        : `2px 2px 0 #1A1A1A`,
                transition: 'box-shadow 0.3s ease, border-color 0.3s ease',
                cursor: 'pointer',
                position: 'relative',
                overflow: 'hidden',
            }}
        >
            {/* Running shimmer animation */}
            {isRunning && (
                <div
                    style={{
                        position: 'absolute',
                        inset: 0,
                        background: `linear-gradient(90deg, transparent 0%, ${statusColors.border}15 50%, transparent 100%)`,
                        animation: 'shimmer 2s ease-in-out infinite',
                    }}
                />
            )}

            {/* Input handle */}
            <Handle
                type="target"
                position={Position.Left}
                style={{
                    background: statusColors.border,
                    border: 'none',
                    borderRadius: 0,
                    width: 8,
                    height: 8,
                }}
            />

            {/* Header row: icon + type badge + status dot */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6, position: 'relative', zIndex: 1 }}>
                {/* Type icon */}
                <span style={{ fontSize: 14, flexShrink: 0 }}>{typeInfo.icon}</span>

                {/* Type badge */}
                <span
                    style={{
                        fontSize: 9,
                        fontFamily: FONT_MONO,
                        fontWeight: 700,
                        textTransform: 'uppercase',
                        letterSpacing: '0.08em',
                        color: typeInfo.color,
                        background: `${typeInfo.color}15`,
                        border: `1px solid ${typeInfo.color}30`,
                        padding: '1px 6px',
                    }}
                >
                    {typeInfo.label}
                </span>

                {/* Cache hit badge */}
                {isCached && (
                    <span
                        style={{
                            fontSize: 9,
                            fontFamily: FONT_MONO,
                            fontWeight: 700,
                            color: '#00EEFF',
                            background: 'rgba(0,238,255,0.1)',
                            border: '1px solid rgba(0,238,255,0.25)',
                            padding: '1px 5px',
                            display: 'flex',
                            alignItems: 'center',
                            gap: 3,
                        }}
                    >
                        ⚡ CACHED
                    </span>
                )}

                {/* Status dot */}
                <div
                    style={{
                        marginLeft: 'auto',
                        width: 8,
                        height: 8,
                        borderRadius: '50%',
                        background: statusColors.text,
                        flexShrink: 0,
                        animation: isRunning ? 'pulse 1.2s ease-in-out infinite' : undefined,
                    }}
                />
            </div>

            {/* Vertex label */}
            <div
                style={{
                    fontSize: 11,
                    fontFamily: FONT_MONO,
                    fontWeight: 600,
                    color: '#F0F0F0',
                    lineHeight: 1.4,
                    marginBottom: 4,
                    position: 'relative',
                    zIndex: 1,
                    wordBreak: 'break-word',
                }}
                title={v.name}
            >
                {label}
            </div>

            {/* Bottom row: duration + error */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, position: 'relative', zIndex: 1 }}>
                {v.duration_ms != null && v.duration_ms > 0 && (
                    <span
                        style={{
                            fontSize: 10,
                            fontFamily: FONT_MONO,
                            color: '#8A8A8A',
                        }}
                    >
                        {formatDuration(v.duration_ms)}
                    </span>
                )}

                {isRunning && v.started_at && (
                    <RunningTimer startedAt={v.started_at} />
                )}

                {v.error && (
                    <span
                        style={{
                            fontSize: 10,
                            fontFamily: FONT_MONO,
                            color: '#FF3333',
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                            maxWidth: 180,
                        }}
                        title={v.error}
                    >
                        ✗ {v.error}
                    </span>
                )}
            </div>

            {/* Progress bar for transfers */}
            {v.progress != null && v.progress > 0 && v.progress < 100 && (
                <div
                    style={{
                        marginTop: 6,
                        height: 3,
                        background: '#1A1A1A',
                        position: 'relative',
                        zIndex: 1,
                    }}
                >
                    <div
                        style={{
                            height: '100%',
                            width: `${v.progress}%`,
                            background: statusColors.border,
                            transition: 'width 0.3s ease',
                        }}
                    />
                </div>
            )}

            {/* Output handle */}
            <Handle
                type="source"
                position={Position.Right}
                style={{
                    background: statusColors.border,
                    border: 'none',
                    borderRadius: 0,
                    width: 8,
                    height: 8,
                }}
            />
        </div>
    );
}

/** Live elapsed timer for running vertices */
function RunningTimer({ startedAt }: { startedAt: number }) {
    const [elapsed, setElapsed] = React.useState(Date.now() - startedAt);

    React.useEffect(() => {
        const id = setInterval(() => setElapsed(Date.now() - startedAt), 100);
        return () => clearInterval(id);
    }, [startedAt]);

    return (
        <span
            style={{
                fontSize: 10,
                fontFamily: FONT_MONO,
                color: '#FFEE00',
                animation: 'pulse 1.5s ease-in-out infinite',
            }}
        >
            {formatDuration(elapsed)}
        </span>
    );
}

export default PipelineVertexNode;

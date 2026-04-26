/**
 * Forge CI — Live Log Stream with ANSI rendering
 *
 * Features:
 * - Real-time log append from SSE
 * - ANSI color → HTML conversion
 * - Auto-scroll with "scroll lock" toggle
 * - Vertex name prefix on each line
 * - Search within logs
 * - stderr highlighted
 */
import React, { useCallback, useEffect, useRef, useState } from 'react';
import type { LogChunk } from '../../types/pipeline';

const FONT_MONO = '"JetBrains Mono", "Fira Code", monospace';
const BRUT = {
    bg: '#0A0A0A', surface: '#111111', border: '#2C2C2C',
    accent: '#FFEE00', green: '#00FF88', red: '#FF3333', cyan: '#00EEFF',
    t1: '#F0F0F0', t2: '#B0B0B0', t3: '#8A8A8A', t4: '#3C3C3C',
};

// ── Simple ANSI to HTML converter ─────────────────────────────
const ANSI_MAP: Record<string, string> = {
    '0': '', '1': 'font-weight:bold', '2': 'opacity:0.6',
    '31': 'color:#FF3333', '32': 'color:#00FF88', '33': 'color:#FFEE00',
    '34': 'color:#33AAFF', '35': 'color:#FF66CC', '36': 'color:#00EEFF',
    '90': 'color:#8A8A8A', '97': 'color:#F0F0F0',
};

function ansiToHtml(text: string): string {
    return text
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/\x1b\[([0-9;]+)m/g, (_, codes) => {
            const parts = codes.split(';');
            const styles = parts.map((c: string) => ANSI_MAP[c]).filter(Boolean);
            if (styles.length === 0 || (parts.length === 1 && parts[0] === '0')) {
                return '</span>';
            }
            return `<span style="${styles.join(';')}">`;
        });
}

export function LiveLogStream({ logs, selectedVertex, selectedVertexName, onClearFilter }: {
    logs: LogChunk[];
    selectedVertex: string | null;
    selectedVertexName?: string;
    onClearFilter: () => void;
}) {
    const scrollRef = useRef<HTMLDivElement>(null);
    const [autoScroll, setAutoScroll] = useState(true);
    const [search, setSearch] = useState('');

    // Auto-scroll when new logs arrive
    useEffect(() => {
        if (autoScroll && scrollRef.current) {
            scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
        }
    }, [logs.length, autoScroll]);

    // Detect manual scroll
    const handleScroll = useCallback(() => {
        if (!scrollRef.current) return;
        const { scrollTop, scrollHeight, clientHeight } = scrollRef.current;
        const atBottom = scrollHeight - scrollTop - clientHeight < 40;
        setAutoScroll(atBottom);
    }, []);

    const filteredLogs = search
        ? logs.filter(l => l.data.toLowerCase().includes(search.toLowerCase()))
        : logs;

    return (
        <div style={{ display: 'flex', flexDirection: 'column', height: '100%', background: '#050505' }}>
            {/* Log toolbar */}
            <div
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    padding: '6px 12px',
                    borderBottom: `1px solid ${BRUT.border}`,
                    background: BRUT.surface,
                    flexShrink: 0,
                }}
            >
                <span
                    style={{
                        fontSize: 10,
                        fontFamily: FONT_MONO,
                        fontWeight: 700,
                        textTransform: 'uppercase',
                        letterSpacing: '0.1em',
                        color: BRUT.accent,
                    }}
                >
                    Build Logs
                </span>

                {selectedVertex && (
                    <span
                        style={{
                            fontSize: 10,
                            fontFamily: FONT_MONO,
                            color: BRUT.cyan,
                            background: 'rgba(0,238,255,0.1)',
                            border: '1px solid rgba(0,238,255,0.2)',
                            padding: '1px 8px',
                            display: 'flex',
                            alignItems: 'center',
                            gap: 4,
                        }}
                    >
                        ⊞ {selectedVertexName ?? selectedVertex.slice(0, 12)}
                        <button
                            onClick={onClearFilter}
                            style={{ background: 'none', border: 'none', color: BRUT.cyan, cursor: 'pointer', fontSize: 11, padding: '0 2px' }}
                        >
                            ✕
                        </button>
                    </span>
                )}

                <div style={{ flex: 1 }} />

                {/* Search */}
                <div style={{ position: 'relative' }}>
                    <input
                        type="text"
                        placeholder="Search logs…"
                        value={search}
                        onChange={e => setSearch(e.target.value)}
                        style={{
                            fontSize: 11,
                            fontFamily: FONT_MONO,
                            background: BRUT.bg,
                            border: `1px solid ${BRUT.border}`,
                            color: BRUT.t1,
                            padding: '3px 8px 3px 24px',
                            width: 180,
                            outline: 'none',
                            borderRadius: 0,
                        }}
                    />
                    <span style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', fontSize: 11, color: BRUT.t4 }}>
                        ⌕
                    </span>
                </div>

                {/* Line count */}
                <span style={{ fontSize: 10, fontFamily: FONT_MONO, color: BRUT.t4 }}>
                    {filteredLogs.length} lines
                </span>

                {/* Auto-scroll toggle */}
                <button
                    onClick={() => setAutoScroll(!autoScroll)}
                    style={{
                        fontSize: 10,
                        fontFamily: FONT_MONO,
                        background: autoScroll ? 'rgba(0,255,136,0.1)' : 'transparent',
                        border: `1px solid ${autoScroll ? 'rgba(0,255,136,0.3)' : BRUT.border}`,
                        color: autoScroll ? BRUT.green : BRUT.t4,
                        padding: '2px 8px',
                        cursor: 'pointer',
                        borderRadius: 0,
                    }}
                >
                    {autoScroll ? '⬇ AUTO' : '⏸ PAUSED'}
                </button>
            </div>

            {/* Log content */}
            <div
                ref={scrollRef}
                onScroll={handleScroll}
                style={{
                    flex: 1,
                    overflow: 'auto',
                    padding: '8px 0',
                    fontFamily: FONT_MONO,
                    fontSize: 12,
                    lineHeight: 1.6,
                }}
            >
                {filteredLogs.length === 0 && (
                    <div style={{ padding: '40px 16px', textAlign: 'center', color: BRUT.t4, fontSize: 12 }}>
                        {logs.length === 0 ? 'Waiting for logs…' : 'No matching log lines'}
                    </div>
                )}

                {filteredLogs.map((log, i) => (
                    <div
                        key={i}
                        style={{
                            padding: '1px 12px',
                            display: 'flex',
                            gap: 8,
                            alignItems: 'flex-start',
                            background: log.stream === 'stderr' ? 'rgba(255,51,51,0.04)' : 'transparent',
                            borderLeft: log.stream === 'stderr' ? `2px solid ${BRUT.red}30` : '2px solid transparent',
                        }}
                    >
                        {/* Line number */}
                        <span style={{ color: BRUT.t4, width: 36, textAlign: 'right', flexShrink: 0, fontSize: 10, marginTop: 2, userSelect: 'none' }}>
                            {i + 1}
                        </span>

                        {/* Timestamp */}
                        <span style={{ color: BRUT.t4, fontSize: 10, flexShrink: 0, marginTop: 2 }}>
                            {new Date(log.timestamp).toLocaleTimeString('en-US', { hour12: false, fractionalSecondDigits: 1 } as any)}
                        </span>

                        {/* Vertex prefix (when showing all) */}
                        {!selectedVertex && log.vertex_name && (
                            <span
                                style={{
                                    color: BRUT.cyan,
                                    fontSize: 10,
                                    flexShrink: 0,
                                    maxWidth: 160,
                                    overflow: 'hidden',
                                    textOverflow: 'ellipsis',
                                    whiteSpace: 'nowrap',
                                    marginTop: 2,
                                    opacity: 0.7,
                                }}
                                title={log.vertex_name}
                            >
                                [{log.vertex_name.slice(0, 24)}]
                            </span>
                        )}

                        {/* Log text */}
                        <span
                            style={{ color: log.stream === 'stderr' ? BRUT.red : BRUT.t2, flex: 1, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}
                            dangerouslySetInnerHTML={{ __html: ansiToHtml(log.data) }}
                        />
                    </div>
                ))}
            </div>
        </div>
    );
}

export default LiveLogStream;

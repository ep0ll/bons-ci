/**
 * Forge CI — Live Pipeline DAG Viewer
 *
 * Connects to SSE server, maintains vertex state via reducer,
 * renders interactive @xyflow/react graph with dagre auto-layout.
 *
 * Usage: <LivePipelineDAG buildId="demo-001" client:only="react" />
 */
import React, { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import {
    ReactFlow,
    Background,
    Controls,
    MiniMap,
    useReactFlow,
    ReactFlowProvider,
    type Node,
    type Edge,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import dagre from 'dagre';
import { PipelineVertexNode } from './PipelineVertex';
import { LiveLogStream } from './LiveLogStream';
import { PipelineStatusBar } from './PipelineStatusBar';
import type {
    PipelineVertex,
    PipelineEvent,
    LogChunk,
    PipelineBuild,
    VertexStatus,
} from '../../types/pipeline';
import { VERTEX_STATUS_COLORS } from '../../types/pipeline';

// ── Constants ─────────────────────────────────────────────────
const SSE_BASE = 'http://localhost:3001';
const NODE_WIDTH = 280;
const NODE_HEIGHT = 90;

const BRUT = {
    bg: '#0A0A0A', surface: '#111111', border: '#2C2C2C', border2: '#1A1A1A',
    accent: '#FFEE00', green: '#00FF88', red: '#FF3333', cyan: '#00EEFF',
    t1: '#F0F0F0', t2: '#B0B0B0', t3: '#8A8A8A', t4: '#3C3C3C',
};

// ── Node types for ReactFlow ──────────────────────────────────
const nodeTypes = { 'pipeline-vertex': PipelineVertexNode };

// ── Dagre layout ──────────────────────────────────────────────
function layoutDAG(vertices: PipelineVertex[]): { nodes: Node[]; edges: Edge[] } {
    const g = new dagre.graphlib.Graph();
    g.setGraph({ rankdir: 'LR', ranksep: 80, nodesep: 30, marginx: 40, marginy: 40 });
    g.setDefaultEdgeLabel(() => ({}));

    for (const v of vertices) {
        g.setNode(v.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
    }

    const edgeList: Edge[] = [];
    for (const v of vertices) {
        for (const inp of v.inputs) {
            const edgeId = `${inp}→${v.id}`;
            g.setEdge(inp, v.id);
            const isActive = v.status === 'running';
            const isFailed = v.status === 'failed';
            const isCached = v.status === 'cached';
            const isDone = v.status === 'completed';
            const edgeColor = isFailed
                ? BRUT.red
                : isActive
                    ? BRUT.accent
                    : isCached
                        ? BRUT.cyan
                        : isDone
                            ? BRUT.green
                            : '#555555';
            edgeList.push({
                id: edgeId,
                source: inp,
                target: v.id,
                type: 'smoothstep',
                animated: isActive,
                style: {
                    stroke: edgeColor,
                    strokeWidth: isActive ? 2.5 : isDone || isCached ? 1.8 : 1.2,
                    opacity: v.status === 'queued' ? 0.35 : 0.85,
                },
                markerEnd: {
                    type: 'arrowclosed' as any,
                    color: edgeColor,
                    width: 12,
                    height: 12,
                },
            });
        }
    }

    dagre.layout(g);

    const nodes: Node[] = vertices.map(v => {
        const pos = g.node(v.id);
        return {
            id: v.id,
            type: 'pipeline-vertex',
            position: { x: (pos?.x ?? 0) - NODE_WIDTH / 2, y: (pos?.y ?? 0) - NODE_HEIGHT / 2 },
            data: v as unknown as Record<string, unknown>,
        };
    });

    return { nodes, edges: edgeList };
}

// ── Reducer for pipeline state ────────────────────────────────
interface PipelineState {
    vertices: Map<string, PipelineVertex>;
    logs: LogChunk[];
    buildStatus: 'connecting' | 'running' | 'succeeded' | 'failed';
    startedAt: number;
}

type PipelineAction =
    | { type: 'VERTEX_EVENT'; event: PipelineEvent }
    | { type: 'LOG_CHUNK'; chunk: LogChunk }
    | { type: 'DAG_COMPLETE' }
    | { type: 'RESET' };

function pipelineReducer(state: PipelineState, action: PipelineAction): PipelineState {
    switch (action.type) {
        case 'VERTEX_EVENT': {
            const { event } = action;
            const vertices = new Map(state.vertices);

            if (event.vertex) {
                vertices.set(event.vertex_id, event.vertex);
            } else {
                const existing = vertices.get(event.vertex_id);
                if (existing) {
                    const updated = { ...existing };
                    switch (event.kind) {
                        case 'vertex.started':
                            updated.status = 'running';
                            updated.started_at = event.timestamp;
                            break;
                        case 'vertex.completed':
                            updated.status = 'completed';
                            updated.completed_at = event.timestamp;
                            updated.duration_ms = updated.started_at ? event.timestamp - updated.started_at : 0;
                            break;
                        case 'vertex.failed':
                            updated.status = 'failed';
                            updated.error = event.error;
                            updated.completed_at = event.timestamp;
                            break;
                        case 'vertex.cached':
                            updated.status = 'cached';
                            updated.cached = true;
                            break;
                        case 'vertex.progress':
                            updated.progress = event.progress;
                            break;
                    }
                    vertices.set(event.vertex_id, updated);
                }
            }

            const buildStatus = state.buildStatus === 'connecting' ? 'running' : state.buildStatus;
            return { ...state, vertices, buildStatus };
        }

        case 'LOG_CHUNK':
            return { ...state, logs: [...state.logs, action.chunk] };

        case 'DAG_COMPLETE': {
            const hasFailed = [...state.vertices.values()].some(v => v.status === 'failed');
            return { ...state, buildStatus: hasFailed ? 'failed' : 'succeeded' };
        }

        case 'RESET':
            return { vertices: new Map(), logs: [], buildStatus: 'connecting', startedAt: Date.now() };

        default:
            return state;
    }
}

// ── SSE Hook ──────────────────────────────────────────────────
function useSSE(buildId: string, dispatch: React.Dispatch<PipelineAction>) {
    useEffect(() => {
        dispatch({ type: 'RESET' });

        const eventSource = new EventSource(`${SSE_BASE}/api/builds/${buildId}/events`);
        const logSource = new EventSource(`${SSE_BASE}/api/builds/${buildId}/logs`);

        eventSource.addEventListener('pipeline', (e) => {
            try {
                const event: PipelineEvent = JSON.parse(e.data);
                if (event.kind === 'dag.complete') {
                    dispatch({ type: 'DAG_COMPLETE' });
                } else {
                    dispatch({ type: 'VERTEX_EVENT', event });
                }
            } catch { }
        });

        logSource.addEventListener('log', (e) => {
            try {
                const chunk: LogChunk = JSON.parse(e.data);
                dispatch({ type: 'LOG_CHUNK', chunk });
            } catch { }
        });

        eventSource.onerror = () => { /* reconnect is automatic */ };
        logSource.onerror = () => { };

        return () => {
            eventSource.close();
            logSource.close();
        };
    }, [buildId, dispatch]);
}

// ── Inner Flow component (needs ReactFlowProvider) ────────────
function PipelineFlow({ vertices, selectedVertex, onSelectVertex }: {
    vertices: PipelineVertex[];
    selectedVertex: string | null;
    onSelectVertex: (id: string | null) => void;
}) {
    const { fitView, setCenter } = useReactFlow();
    const prevCountRef = useRef(0);

    // Track user-dragged positions so dagre doesn't override them
    const positionOverrides = useRef<Map<string, { x: number; y: number }>>(new Map());
    const layoutedIdsRef = useRef<Set<string>>(new Set());

    // Controlled state for nodes and edges
    const [flowNodes, setFlowNodes] = useState<Node[]>([]);
    const [flowEdges, setFlowEdges] = useState<Edge[]>([]);

    // Compute layout — only position NEW nodes via dagre, preserve dragged positions
    useEffect(() => {
        if (vertices.length === 0) { setFlowNodes([]); setFlowEdges([]); return; }

        const { nodes: dagreNodes, edges } = layoutDAG(vertices);
        const newNodes = dagreNodes.map(n => {
            const override = positionOverrides.current.get(n.id);
            if (override) {
                return { ...n, position: override };
            }
            return n;
        });

        setFlowNodes(newNodes);
        setFlowEdges(prevEdges => {
            // Highlight edges connected to selected vertex
            if (selectedVertex) {
                return edges.map(e => ({
                    ...e,
                    style: {
                        ...e.style,
                        opacity: (e.source === selectedVertex || e.target === selectedVertex)
                            ? 1
                            : 0.15,
                        strokeWidth: (e.source === selectedVertex || e.target === selectedVertex)
                            ? ((e.style?.strokeWidth as number) ?? 1.5) + 0.5
                            : ((e.style?.strokeWidth as number) ?? 1.5),
                    },
                }));
            }
            return edges;
        });

        // Auto-fit when new vertices appear
        const hasNew = vertices.some(v => !layoutedIdsRef.current.has(v.id));
        if (hasNew) {
            vertices.forEach(v => layoutedIdsRef.current.add(v.id));
            if (vertices.length > prevCountRef.current) {
                setTimeout(() => fitView({ padding: 0.15, duration: 400 }), 100);
                prevCountRef.current = vertices.length;
            }
        }
    }, [vertices, selectedVertex, fitView]);

    // Handle node position changes (drag, selection, etc.)
    const onNodesChange = useCallback((changes: any[]) => {
        setFlowNodes(nds => {
            const updated = [...nds];
            for (const change of changes) {
                if (change.type === 'position' && change.position) {
                    const idx = updated.findIndex(n => n.id === change.id);
                    if (idx !== -1) {
                        updated[idx] = {
                            ...updated[idx],
                            position: change.position,
                            ...(change.dragging !== undefined ? { dragging: change.dragging } : {}),
                        };
                    }
                }
                if (change.type === 'select') {
                    const idx = updated.findIndex(n => n.id === change.id);
                    if (idx !== -1) {
                        updated[idx] = { ...updated[idx], selected: change.selected };
                    }
                }
            }
            return updated;
        });
    }, []);

    // Save dragged position permanently
    const onNodeDragStop = useCallback((_: any, node: Node) => {
        positionOverrides.current.set(node.id, { ...node.position });
    }, []);

    const onNodeClick = useCallback((_: any, node: Node) => {
        onSelectVertex(node.id === selectedVertex ? null : node.id);
    }, [selectedVertex, onSelectVertex]);

    // Double-click to zoom-and-center on a node
    const onNodeDoubleClick = useCallback((_: any, node: Node) => {
        setCenter(
            node.position.x + NODE_WIDTH / 2,
            node.position.y + NODE_HEIGHT / 2,
            { zoom: 1.5, duration: 500 }
        );
    }, [setCenter]);

    // Reset layout (re-apply dagre to all nodes)
    const resetLayout = useCallback(() => {
        positionOverrides.current.clear();
        layoutedIdsRef.current.clear();
        prevCountRef.current = 0;
        const { nodes, edges } = layoutDAG(vertices);
        setFlowNodes(nodes);
        setFlowEdges(edges);
        setTimeout(() => fitView({ padding: 0.15, duration: 400 }), 50);
    }, [vertices, fitView]);

    return (
        <ReactFlow
            nodes={flowNodes}
            edges={flowEdges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onNodeClick={onNodeClick}
            onNodeDragStop={onNodeDragStop}
            onNodeDoubleClick={onNodeDoubleClick}
            onPaneClick={() => onSelectVertex(null)}
            nodesDraggable={true}
            fitView
            fitViewOptions={{ padding: 0.15 }}
            proOptions={{ hideAttribution: true }}
            minZoom={0.2}
            maxZoom={2.5}
            snapToGrid={true}
            snapGrid={[12, 12]}
            style={{ background: BRUT.bg }}
            defaultEdgeOptions={{
                type: 'smoothstep',
            }}
        >
            <Background color={BRUT.border} gap={24} size={1} />
            <Controls
                style={{
                    background: BRUT.surface,
                    border: `1px solid ${BRUT.border}`,
                    borderRadius: 0,
                    color: BRUT.t2,
                }}
            />
            <MiniMap
                style={{
                    background: BRUT.surface,
                    border: `1px solid ${BRUT.border}`,
                    borderRadius: 0,
                }}
                nodeColor={(n) => {
                    const status = (n.data as any)?.status as VertexStatus ?? 'queued';
                    return VERTEX_STATUS_COLORS[status]?.border ?? BRUT.t4;
                }}
                maskColor="rgba(0,0,0,0.7)"
                pannable
                zoomable
            />
            {/* Reset layout button */}
            <div style={{
                position: 'absolute',
                top: 12,
                left: 12,
                zIndex: 5,
                display: 'flex',
                gap: 6,
            }}>
                <button
                    onClick={resetLayout}
                    title="Reset layout (re-apply auto dagre layout)"
                    style={{
                        background: BRUT.surface,
                        border: `1px solid ${BRUT.border}`,
                        color: BRUT.t2,
                        padding: '6px 10px',
                        fontSize: 10,
                        fontFamily: '"JetBrains Mono", monospace',
                        fontWeight: 600,
                        cursor: 'pointer',
                        textTransform: 'uppercase',
                        letterSpacing: '0.05em',
                        transition: 'all 0.15s',
                    }}
                    onMouseOver={e => { (e.target as HTMLElement).style.borderColor = BRUT.accent; (e.target as HTMLElement).style.color = BRUT.accent; }}
                    onMouseOut={e => { (e.target as HTMLElement).style.borderColor = BRUT.border; (e.target as HTMLElement).style.color = BRUT.t2; }}
                >
                    ⟲ Reset Layout
                </button>
            </div>
        </ReactFlow>
    );
}

// ── Main exported component ───────────────────────────────────
export function LivePipelineDAG({ buildId = 'demo-001' }: { buildId?: string }) {
    const [state, dispatch] = useReducer(pipelineReducer, {
        vertices: new Map(),
        logs: [],
        buildStatus: 'connecting',
        startedAt: Date.now(),
    });

    const [selectedVertex, setSelectedVertex] = useState<string | null>(null);
    const [splitRatio, setSplitRatio] = useState(0.55); // DAG takes 55% height
    const [isResizing, setIsResizing] = useState(false);
    const containerRef = useRef<HTMLDivElement>(null);

    useSSE(buildId, dispatch);

    const vertices = useMemo(() => [...state.vertices.values()], [state.vertices]);
    const selectedVertexData = selectedVertex ? state.vertices.get(selectedVertex) : null;
    const filteredLogs = selectedVertex
        ? state.logs.filter(l => l.vertex_id === selectedVertex)
        : state.logs;

    // Resize handler for split view
    const handleMouseMove = useCallback((e: MouseEvent) => {
        if (!isResizing || !containerRef.current) return;
        const rect = containerRef.current.getBoundingClientRect();
        const ratio = (e.clientY - rect.top) / rect.height;
        setSplitRatio(Math.max(0.25, Math.min(0.8, ratio)));
    }, [isResizing]);

    const handleMouseUp = useCallback(() => setIsResizing(false), []);

    useEffect(() => {
        if (isResizing) {
            document.addEventListener('mousemove', handleMouseMove);
            document.addEventListener('mouseup', handleMouseUp);
            return () => {
                document.removeEventListener('mousemove', handleMouseMove);
                document.removeEventListener('mouseup', handleMouseUp);
            };
        }
    }, [isResizing, handleMouseMove, handleMouseUp]);

    return (
        <div
            ref={containerRef}
            style={{
                display: 'flex',
                flexDirection: 'column',
                height: '100%',
                background: BRUT.bg,
                fontFamily: '"Space Grotesk", system-ui, sans-serif',
            }}
        >
            {/* Status Bar */}
            <PipelineStatusBar
                buildId={buildId}
                status={state.buildStatus}
                vertices={vertices}
                startedAt={state.startedAt}
            />

            {/* DAG Graph */}
            <div style={{ flex: `0 0 ${splitRatio * 100}%`, position: 'relative', overflow: 'hidden' }}>
                <ReactFlowProvider>
                    <PipelineFlow
                        vertices={vertices}
                        selectedVertex={selectedVertex}
                        onSelectVertex={setSelectedVertex}
                    />
                </ReactFlowProvider>

                {/* Selected vertex info overlay */}
                {selectedVertexData && (
                    <div
                        style={{
                            position: 'absolute',
                            top: 12,
                            right: 12,
                            background: BRUT.surface,
                            border: `2px solid ${BRUT.border}`,
                            padding: '12px 16px',
                            maxWidth: 320,
                            zIndex: 10,
                            boxShadow: `4px 4px 0 ${BRUT.border2}`,
                        }}
                    >
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                            <span style={{ fontSize: 10, fontFamily: '"JetBrains Mono", monospace', color: BRUT.accent, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.1em' }}>
                                Selected Vertex
                            </span>
                            <button
                                onClick={() => setSelectedVertex(null)}
                                style={{ background: 'none', border: 'none', color: BRUT.t3, cursor: 'pointer', fontSize: 16, padding: 0 }}
                            >
                                ✕
                            </button>
                        </div>
                        <div style={{ fontSize: 12, color: BRUT.t1, fontWeight: 600, marginBottom: 4 }}>
                            {selectedVertexData.name}
                        </div>
                        <div style={{ fontSize: 10, fontFamily: '"JetBrains Mono", monospace', color: BRUT.t3 }}>
                            {selectedVertexData.type} · {selectedVertexData.status}
                            {selectedVertexData.duration_ms ? ` · ${(selectedVertexData.duration_ms / 1000).toFixed(1)}s` : ''}
                        </div>
                    </div>
                )}
            </div>

            {/* Resize handle */}
            <div
                onMouseDown={() => setIsResizing(true)}
                style={{
                    height: 6,
                    background: isResizing ? BRUT.accent : BRUT.border,
                    cursor: 'row-resize',
                    transition: 'background 0.15s',
                    position: 'relative',
                    zIndex: 20,
                    flexShrink: 0,
                }}
            >
                <div style={{
                    position: 'absolute',
                    left: '50%',
                    top: '50%',
                    transform: 'translate(-50%, -50%)',
                    display: 'flex',
                    gap: 3,
                }}>
                    {[0, 1, 2].map(i => (
                        <div key={i} style={{ width: 3, height: 3, background: isResizing ? '#0A0A0A' : BRUT.t4, borderRadius: '50%' }} />
                    ))}
                </div>
            </div>

            {/* Log Stream */}
            <div style={{ flex: 1, overflow: 'hidden', minHeight: 100 }}>
                <LiveLogStream
                    logs={filteredLogs}
                    selectedVertex={selectedVertex}
                    selectedVertexName={selectedVertexData?.name}
                    onClearFilter={() => setSelectedVertex(null)}
                />
            </div>

            {/* Global CSS for animations */}
            <style>{`
        @keyframes shimmer {
          0%, 100% { opacity: 0; transform: translateX(-100%); }
          50% { opacity: 1; transform: translateX(100%); }
        }
        @keyframes pulse {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.5; }
        }
        .react-flow__node.selected { outline: none !important; }
        .react-flow__edge-path { transition: stroke 0.3s ease, stroke-width 0.3s ease; }
      `}</style>
        </div>
    );
}

export default LivePipelineDAG;

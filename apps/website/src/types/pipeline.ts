// ============================================================
// FORGE CI — BuildKit LLB Pipeline Types
// Mirrors: client/llb/core (Go) + client/llb/reactive (Go)
// ============================================================

// ── Vertex Types (mirrors core.VertexType) ─────────────────────
export type VertexType =
    | 'source'       // Image pull, git clone, local, HTTP
    | 'exec'         // RUN command
    | 'file'         // COPY, ADD, mkdir, mkfile
    | 'merge'        // Multi-stage merge
    | 'diff'         // Layer diff
    | 'build'        // Nested build
    | 'conditional'  // Platform/arg conditional
    | 'matrix'       // Cartesian/explicit matrix expansion
    | 'gate'         // Approval gate
    | 'selector'     // Label-based selector
    | 'custom'       // Plugin vertex

// ── Vertex Status ──────────────────────────────────────────────
export type VertexStatus =
    | 'queued'
    | 'running'
    | 'completed'
    | 'failed'
    | 'cached'
    | 'cancelled';

// ── Event Types (mirrors reactive.EventKind + solver events) ───
export type EventKind =
    | 'vertex.added'
    | 'vertex.removed'
    | 'vertex.replaced'
    | 'vertex.started'
    | 'vertex.completed'
    | 'vertex.failed'
    | 'vertex.cached'
    | 'vertex.progress'
    | 'log.chunk'
    | 'dag.complete';

// ── Pipeline Vertex (frontend projection of core.Vertex) ───────
export interface PipelineVertex {
    /** Content-address digest */
    id: string;
    /** Human label, e.g. "FROM alpine:3.19", "RUN npm install" */
    name: string;
    /** Op category */
    type: VertexType;
    /** Input vertex IDs (edges) */
    inputs: string[];
    /** Current execution status */
    status: VertexStatus;
    /** Epoch ms when execution started */
    started_at?: number;
    /** Epoch ms when execution finished */
    completed_at?: number;
    /** Duration in milliseconds */
    duration_ms?: number;
    /** Whether this vertex was cache-hit */
    cached: boolean;
    /** Error message if failed */
    error?: string;
    /** Progress percentage (0–100) for file transfers */
    progress?: number;
    /** Dockerfile source line reference */
    source_line?: number;
    /** Parallel group identifier */
    parallel_group?: string;
}

// ── SSE Event Payload ──────────────────────────────────────────
export interface PipelineEvent {
    kind: EventKind;
    vertex_id: string;
    vertex?: PipelineVertex;
    timestamp: number;
    /** For vertex.progress events */
    progress?: number;
    /** For vertex.failed events */
    error?: string;
}

// ── Log Chunk (SSE log stream) ─────────────────────────────────
export interface LogChunk {
    vertex_id: string;
    vertex_name?: string;
    stream: 'stdout' | 'stderr';
    /** May contain ANSI escape codes */
    data: string;
    timestamp: number;
}

// ── Pipeline Build (top-level metadata) ────────────────────────
export interface PipelineBuild {
    id: string;
    project_name: string;
    branch: string;
    commit_sha: string;
    commit_message: string;
    trigger: 'push' | 'pr' | 'manual' | 'schedule' | 'api';
    author: { name: string; avatar_url?: string };
    started_at: number;
    status: 'running' | 'succeeded' | 'failed' | 'cancelled';
    vertex_count: number;
    cached_count: number;
    dockerfile?: string;
}

// ── DAG Layout Helpers ─────────────────────────────────────────
export interface VertexNode {
    id: string;
    type: 'pipeline-vertex';
    position: { x: number; y: number };
    data: PipelineVertex;
}

export interface VertexEdge {
    id: string;
    source: string;
    target: string;
    animated?: boolean;
    style?: Record<string, unknown>;
}

// ── Icon mapping for vertex types ──────────────────────────────
export const VERTEX_TYPE_ICONS: Record<VertexType, { icon: string; label: string; color: string }> = {
    source: { icon: '📦', label: 'Source', color: '#33AAFF' },
    exec: { icon: '⚡', label: 'Exec', color: '#FFEE00' },
    file: { icon: '📁', label: 'File', color: '#FF7700' },
    merge: { icon: '🔀', label: 'Merge', color: '#AA66FF' },
    diff: { icon: '🔃', label: 'Diff', color: '#00EEFF' },
    build: { icon: '🏗️', label: 'Build', color: '#FF3399' },
    conditional: { icon: '❓', label: 'Conditional', color: '#FFBB00' },
    matrix: { icon: '⊞', label: 'Matrix', color: '#66FF66' },
    gate: { icon: '🚧', label: 'Gate', color: '#FF5555' },
    selector: { icon: '🎯', label: 'Selector', color: '#FF88CC' },
    custom: { icon: '🔧', label: 'Custom', color: '#AAAAAA' },
};

export const VERTEX_STATUS_COLORS: Record<VertexStatus, { border: string; bg: string; text: string; glow: string }> = {
    queued: { border: '#3C3C3C', bg: 'rgba(60,60,60,0.15)', text: '#666666', glow: 'none' },
    running: { border: '#FFEE00', bg: 'rgba(255,238,0,0.08)', text: '#FFEE00', glow: '0 0 20px rgba(255,238,0,0.3)' },
    completed: { border: '#00FF88', bg: 'rgba(0,255,136,0.06)', text: '#00FF88', glow: 'none' },
    failed: { border: '#FF3333', bg: 'rgba(255,51,51,0.08)', text: '#FF3333', glow: '0 0 16px rgba(255,51,51,0.25)' },
    cached: { border: '#00EEFF', bg: 'rgba(0,238,255,0.06)', text: '#00EEFF', glow: 'none' },
    cancelled: { border: '#3C3C3C', bg: 'rgba(60,60,60,0.1)', text: '#666666', glow: 'none' },
};

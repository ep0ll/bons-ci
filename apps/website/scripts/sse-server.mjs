#!/usr/bin/env node
/**
 * Forge CI — Mock SSE Server for BuildKit Pipeline Visualization
 *
 * Simulates a real BuildKit LLB DAG solve with:
 * - Deterministic vertex progression (pull → install → build → test → export)
 * - Parallel execution of independent vertices
 * - Cache hits on repeated runs
 * - Realistic log output with ANSI colors
 * - Proper SSE framing (event: / data: / id:)
 *
 * Usage: node scripts/sse-server.mjs
 * Endpoints:
 *   GET /api/builds/:id/events  — vertex status SSE stream
 *   GET /api/builds/:id/logs    — log chunks SSE stream
 *   GET /api/builds/:id         — build metadata JSON
 */

import http from 'node:http';
import { URL } from 'node:url';
import crypto from 'node:crypto';

const PORT = 3001;

// ── ANSI helpers ─────────────────────────────────────────────────
const C = {
    reset: '\x1b[0m', bold: '\x1b[1m', dim: '\x1b[2m',
    red: '\x1b[31m', green: '\x1b[32m', yellow: '\x1b[33m',
    blue: '\x1b[34m', magenta: '\x1b[35m', cyan: '\x1b[36m',
    gray: '\x1b[90m', white: '\x1b[97m',
};

// ── Simulated DAG definition ─────────────────────────────────────
function makePipelineDAG(buildId) {
    const ts = Date.now();
    const sha = crypto.createHash('sha256').update(buildId).digest('hex').slice(0, 12);

    /** @type {import('../src/types/pipeline').PipelineVertex[]} */
    const vertices = [
        // Stage 0: base images (parallel)
        {
            id: `sha256:${sha}a0`, name: '[base] FROM node:22-alpine', type: 'source',
            inputs: [], status: 'queued', cached: false, parallel_group: 'pull',
        },
        {
            id: `sha256:${sha}a1`, name: '[base] FROM golang:1.23-alpine', type: 'source',
            inputs: [], status: 'queued', cached: false, parallel_group: 'pull',
        },
        {
            id: `sha256:${sha}a2`, name: '[base] FROM alpine:3.20', type: 'source',
            inputs: [], status: 'queued', cached: true, parallel_group: 'pull',
        },

        // Stage 1: install deps (parallel, after their base)
        {
            id: `sha256:${sha}b0`, name: '[frontend] RUN npm ci --frozen-lockfile', type: 'exec',
            inputs: [`sha256:${sha}a0`], status: 'queued', cached: false,
        },
        {
            id: `sha256:${sha}b1`, name: '[backend] RUN go mod download', type: 'exec',
            inputs: [`sha256:${sha}a1`], status: 'queued', cached: true,
        },
        {
            id: `sha256:${sha}b2`, name: '[frontend] COPY package*.json ./', type: 'file',
            inputs: [`sha256:${sha}a0`], status: 'queued', cached: true,
        },

        // Stage 2: build (after deps)
        {
            id: `sha256:${sha}c0`, name: '[frontend] RUN npm run build', type: 'exec',
            inputs: [`sha256:${sha}b0`, `sha256:${sha}b2`], status: 'queued', cached: false,
        },
        {
            id: `sha256:${sha}c1`, name: '[backend] RUN go build -o /app ./cmd/server', type: 'exec',
            inputs: [`sha256:${sha}b1`], status: 'queued', cached: false,
        },

        // Stage 3: test (parallel, after build)
        {
            id: `sha256:${sha}d0`, name: '[test] RUN npm run test:unit', type: 'exec',
            inputs: [`sha256:${sha}c0`], status: 'queued', cached: false, parallel_group: 'test',
        },
        {
            id: `sha256:${sha}d1`, name: '[test] RUN go test ./...', type: 'exec',
            inputs: [`sha256:${sha}c1`], status: 'queued', cached: false, parallel_group: 'test',
        },
        {
            id: `sha256:${sha}d2`, name: '[lint] RUN eslint . && golangci-lint run', type: 'exec',
            inputs: [`sha256:${sha}c0`, `sha256:${sha}c1`], status: 'queued', cached: false, parallel_group: 'test',
        },

        // Stage 4: copy artifacts into final image
        {
            id: `sha256:${sha}e0`, name: '[final] COPY --from=frontend /app/dist ./public', type: 'file',
            inputs: [`sha256:${sha}c0`, `sha256:${sha}a2`], status: 'queued', cached: false,
        },
        {
            id: `sha256:${sha}e1`, name: '[final] COPY --from=backend /app ./server', type: 'file',
            inputs: [`sha256:${sha}c1`, `sha256:${sha}a2`], status: 'queued', cached: false,
        },

        // Stage 5: merge
        {
            id: `sha256:${sha}f0`, name: '[final] Merge layers', type: 'merge',
            inputs: [`sha256:${sha}e0`, `sha256:${sha}e1`, `sha256:${sha}d0`, `sha256:${sha}d1`, `sha256:${sha}d2`],
            status: 'queued', cached: false,
        },

        // Stage 6: export
        {
            id: `sha256:${sha}g0`, name: '[export] Push ghcr.io/forge-ci/app:latest', type: 'build',
            inputs: [`sha256:${sha}f0`], status: 'queued', cached: false,
        },
    ];

    return {
        build: {
            id: buildId,
            project_name: 'forge-ci/monorepo',
            branch: 'main',
            commit_sha: 'a4f9c2b7e8d1',
            commit_message: 'feat: add real-time pipeline viewer',
            trigger: 'push',
            author: { name: 'Sai', avatar_url: '' },
            started_at: ts,
            status: 'running',
            vertex_count: vertices.length,
            cached_count: vertices.filter(v => v.cached).length,
            dockerfile: 'Dockerfile.multi',
        },
        vertices,
    };
}

// ── Log generators per vertex type ───────────────────────────────
const logGenerators = {
    source: (v) => [
        { s: 'stdout', d: `${C.cyan}resolve${C.reset} ${v.name.match(/FROM (.+)/)?.[1] ?? v.name}` },
        { s: 'stdout', d: `${C.dim}sha256:${crypto.randomBytes(32).toString('hex').slice(0, 64)}${C.reset}` },
        ...(v.cached ? [{ s: 'stdout', d: `${C.green}CACHED${C.reset}` }] : [
            { s: 'stdout', d: `${C.yellow}pulling${C.reset} manifest...` },
            { s: 'stdout', d: `${C.dim}downloading${C.reset} layer 1/4 [======>   ] 12.4MB/45.2MB` },
            { s: 'stdout', d: `${C.dim}downloading${C.reset} layer 2/4 [=========>] 3.1MB/3.1MB` },
            { s: 'stdout', d: `${C.dim}downloading${C.reset} layer 3/4 [======>   ] 8.7MB/22.1MB` },
            { s: 'stdout', d: `${C.dim}downloading${C.reset} layer 4/4 [=========>] 1.2MB/1.2MB` },
            { s: 'stdout', d: `${C.green}done${C.reset} pulled in 3.2s` },
        ]),
    ],
    exec: (v) => {
        const cmd = v.name.match(/RUN (.+)/)?.[1] ?? 'exec';
        if (cmd.includes('npm ci')) return [
            { s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` },
            { s: 'stdout', d: `${C.cyan}npm${C.reset} ${C.dim}warn${C.reset} deprecated inflight@1.0.6` },
            { s: 'stdout', d: `${C.dim}added 1847 packages in 14.2s${C.reset}` },
            { s: 'stdout', d: `${C.dim}320 packages are looking for funding${C.reset}` },
        ];
        if (cmd.includes('npm run build')) return [
            { s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` },
            { s: 'stdout', d: `${C.cyan}vite${C.reset} v6.2.4 building for production...` },
            { s: 'stdout', d: `${C.dim}transforming... (487 modules)${C.reset}` },
            { s: 'stdout', d: `${C.dim}rendering chunks...${C.reset}` },
            { s: 'stdout', d: `${C.green}✓${C.reset} 487 modules transformed.` },
            { s: 'stdout', d: `${C.dim}dist/index.html              0.52 kB │ gzip:  0.34 kB${C.reset}` },
            { s: 'stdout', d: `${C.dim}dist/assets/index-DxK9q.js  142.87 kB │ gzip: 45.23 kB${C.reset}` },
            { s: 'stdout', d: `${C.green}✓${C.reset} built in 8.4s` },
        ];
        if (cmd.includes('go build')) return [
            { s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` },
            { s: 'stderr', d: `${C.yellow}# github.com/forge-ci/server/internal/auth${C.reset}` },
            { s: 'stderr', d: `${C.dim}./auth.go:42:3: warning: unreachable code${C.reset}` },
            { s: 'stdout', d: `${C.green}compiled${C.reset} server binary: 24.3 MB (stripped)` },
        ];
        if (cmd.includes('go mod download')) return [
            { s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` },
            { s: 'stdout', d: `${C.green}CACHED${C.reset} — all modules already downloaded` },
        ];
        if (cmd.includes('test:unit')) return [
            { s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` },
            { s: 'stdout', d: `${C.white}PASS${C.reset} src/auth/session.test.ts (1.2s)` },
            { s: 'stdout', d: `${C.white}PASS${C.reset} src/pipeline/dag.test.ts (0.8s)` },
            { s: 'stdout', d: `${C.white}PASS${C.reset} src/api/builds.test.ts (2.1s)` },
            { s: 'stdout', d: `` },
            { s: 'stdout', d: `${C.bold}Test Suites:${C.reset} ${C.green}47 passed${C.reset}, 47 total` },
            { s: 'stdout', d: `${C.bold}Tests:${C.reset}       ${C.green}312 passed${C.reset}, 312 total` },
            { s: 'stdout', d: `${C.bold}Time:${C.reset}        4.82s` },
        ];
        if (cmd.includes('go test')) return [
            { s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` },
            { s: 'stdout', d: `${C.green}ok${C.reset}  github.com/forge-ci/server/pkg/solver   1.234s` },
            { s: 'stdout', d: `${C.green}ok${C.reset}  github.com/forge-ci/server/pkg/cache     0.456s` },
            { s: 'stdout', d: `${C.green}ok${C.reset}  github.com/forge-ci/server/internal/api  2.891s` },
            { s: 'stdout', d: `${C.green}ok${C.reset}  github.com/forge-ci/server/internal/auth 0.312s` },
        ];
        if (cmd.includes('eslint')) return [
            { s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` },
            { s: 'stdout', d: `${C.green}✔${C.reset} ESLint: 0 errors, 2 warnings` },
            { s: 'stdout', d: `${C.green}✔${C.reset} golangci-lint: no issues found` },
        ];
        return [{ s: 'stdout', d: `${C.dim}$ ${cmd}${C.reset}` }, { s: 'stdout', d: `${C.green}done${C.reset}` }];
    },
    file: (v) => [
        { s: 'stdout', d: `${C.dim}${v.name}${C.reset}` },
        { s: 'stdout', d: `${C.dim}copying files...${C.reset}` },
        { s: 'stdout', d: `${C.green}done${C.reset} 47 files, 12.3 MB` },
    ],
    merge: (v) => [
        { s: 'stdout', d: `${C.magenta}merging${C.reset} ${v.inputs.length} layers...` },
        { s: 'stdout', d: `${C.dim}layer 0: sha256:a0b1c2... (4.2 MB)${C.reset}` },
        { s: 'stdout', d: `${C.dim}layer 1: sha256:d3e4f5... (24.3 MB)${C.reset}` },
        { s: 'stdout', d: `${C.green}merged${C.reset} final image: 38.7 MB` },
    ],
    build: (v) => [
        { s: 'stdout', d: `${C.cyan}pushing${C.reset} ghcr.io/forge-ci/app:latest` },
        { s: 'stdout', d: `${C.dim}uploading layer 1/3 [=========>] 4.2MB/4.2MB${C.reset}` },
        { s: 'stdout', d: `${C.dim}uploading layer 2/3 [=========>] 24.3MB/24.3MB${C.reset}` },
        { s: 'stdout', d: `${C.dim}uploading layer 3/3 [=========>] 12.3MB/12.3MB${C.reset}` },
        { s: 'stdout', d: `${C.green}pushed${C.reset} digest: sha256:${crypto.randomBytes(32).toString('hex').slice(0, 64)}` },
        { s: 'stdout', d: `${C.green}✓${C.reset} image pushed in 6.1s` },
    ],
};

// ── Execution timings (ms) per vertex ────────────────────────────
const timings = {
    source_cached: 50,
    source: [800, 1200, 1500, 2000, 3200],
    exec_cached: 80,
    'npm ci': 4500,
    'npm run build': 5200,
    'go build': 3800,
    'go mod download': 100,
    'test:unit': 3200,
    'go test': 2800,
    'eslint': 1800,
    file: [400, 600, 800],
    merge: 1200,
    build: 4800,
};

function getVertexDuration(v) {
    if (v.cached) return timings[`${v.type}_cached`] ?? 80;
    for (const [key, val] of Object.entries(timings)) {
        if (v.name.includes(key)) return Array.isArray(val) ? val[Math.floor(Math.random() * val.length)] : val;
    }
    return 1000;
}

// ── Topological order for execution ──────────────────────────────
function topoSort(vertices) {
    const indeg = new Map(vertices.map(v => [v.id, 0]));
    const adj = new Map(vertices.map(v => [v.id, []]));
    for (const v of vertices) {
        for (const inp of v.inputs) {
            adj.get(inp)?.push(v.id);
            indeg.set(v.id, (indeg.get(v.id) ?? 0) + 1);
        }
    }
    const queue = vertices.filter(v => v.inputs.length === 0).map(v => v.id);
    const layers = [];
    while (queue.length > 0) {
        const layer = [...queue];
        layers.push(layer);
        queue.length = 0;
        for (const id of layer) {
            for (const next of adj.get(id) ?? []) {
                indeg.set(next, (indeg.get(next) ?? 0) - 1);
                if (indeg.get(next) === 0) queue.push(next);
            }
        }
    }
    return layers;
}

// ── SSE writer helpers ───────────────────────────────────────────
let eventId = 0;
function sseWrite(res, eventType, data) {
    eventId++;
    res.write(`id: ${eventId}\nevent: ${eventType}\ndata: ${JSON.stringify(data)}\n\n`);
}

// ── Simulation engine ────────────────────────────────────────────
async function simulateBuild(buildId, eventRes, logRes) {
    const { build, vertices } = makePipelineDAG(buildId);
    const vertexMap = new Map(vertices.map(v => [v.id, { ...v }]));
    const layers = topoSort(vertices);

    // Send initial build metadata
    if (eventRes) {
        for (const v of vertices) {
            sseWrite(eventRes, 'pipeline', { kind: 'vertex.added', vertex_id: v.id, vertex: v, timestamp: Date.now() });
        }
    }

    // Execute layer by layer
    for (const layer of layers) {
        // Start all vertices in this layer concurrently
        const promises = layer.map(async (id) => {
            const v = vertexMap.get(id);
            if (!v) return;

            // Mark started
            v.status = v.cached ? 'cached' : 'running';
            v.started_at = Date.now();

            if (eventRes) {
                sseWrite(eventRes, 'pipeline', {
                    kind: v.cached ? 'vertex.cached' : 'vertex.started',
                    vertex_id: v.id, vertex: { ...v }, timestamp: Date.now(),
                });
            }

            const duration = getVertexDuration(v);
            const logs = (logGenerators[v.type] ?? logGenerators.exec)(v);

            // Stream logs over the duration
            const logInterval = Math.max(80, duration / (logs.length + 1));
            for (let i = 0; i < logs.length; i++) {
                await sleep(logInterval);
                if (logRes) {
                    sseWrite(logRes, 'log', {
                        vertex_id: v.id, vertex_name: v.name,
                        stream: logs[i].s, data: logs[i].d,
                        timestamp: Date.now(),
                    });
                }
                // Progress events for file/source type
                if ((v.type === 'file' || v.type === 'source' || v.type === 'build') && !v.cached && eventRes) {
                    const progress = Math.round(((i + 1) / logs.length) * 100);
                    sseWrite(eventRes, 'pipeline', {
                        kind: 'vertex.progress', vertex_id: v.id, progress, timestamp: Date.now(),
                    });
                }
            }

            // Wait remaining duration
            await sleep(Math.max(0, duration - logs.length * logInterval));

            // Mark completed
            v.completed_at = Date.now();
            v.duration_ms = v.completed_at - v.started_at;
            v.status = v.cached ? 'cached' : 'completed';

            if (eventRes) {
                sseWrite(eventRes, 'pipeline', {
                    kind: v.cached ? 'vertex.cached' : 'vertex.completed',
                    vertex_id: v.id, vertex: { ...v }, timestamp: Date.now(),
                });
            }
        });

        await Promise.all(promises);
    }

    // DAG complete
    if (eventRes) {
        sseWrite(eventRes, 'pipeline', {
            kind: 'dag.complete', vertex_id: '', timestamp: Date.now(),
        });
    }
}

function sleep(ms) {
    return new Promise(r => setTimeout(r, ms));
}

// ── HTTP Server ──────────────────────────────────────────────────
const activeSims = new Map(); // buildId -> { eventClients, logClients }

const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, `http://localhost:${PORT}`);

    // CORS
    res.setHeader('Access-Control-Allow-Origin', '*');
    res.setHeader('Access-Control-Allow-Methods', 'GET, OPTIONS');
    res.setHeader('Access-Control-Allow-Headers', 'Content-Type');
    if (req.method === 'OPTIONS') { res.writeHead(204); res.end(); return; }

    // Route: GET /api/builds/:id
    const metaMatch = url.pathname.match(/^\/api\/builds\/([^/]+)$/);
    if (metaMatch && !url.pathname.includes('events') && !url.pathname.includes('logs')) {
        const { build } = makePipelineDAG(metaMatch[1]);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(build));
        return;
    }

    // Route: GET /api/builds/:id/events
    const eventsMatch = url.pathname.match(/^\/api\/builds\/([^/]+)\/events$/);
    if (eventsMatch) {
        const buildId = eventsMatch[1];
        res.writeHead(200, {
            'Content-Type': 'text/event-stream',
            'Cache-Control': 'no-cache',
            'Connection': 'keep-alive',
        });
        res.write(':ok\n\n');

        // Start simulation if not running
        if (!activeSims.has(buildId)) {
            const sim = { eventClients: new Set(), logClients: new Set() };
            activeSims.set(buildId, sim);

            // Proxy writer: writes to all connected event clients
            const eventProxy = {
                write: (chunk) => { for (const c of sim.eventClients) { try { c.write(chunk); } catch { } } },
            };
            const logProxy = {
                write: (chunk) => { for (const c of sim.logClients) { try { c.write(chunk); } catch { } } },
            };

            simulateBuild(buildId, eventProxy, logProxy).then(() => {
                // After simulation ends, clean up after a delay
                setTimeout(() => activeSims.delete(buildId), 5000);
            });
        }

        activeSims.get(buildId).eventClients.add(res);
        req.on('close', () => activeSims.get(buildId)?.eventClients.delete(res));
        return;
    }

    // Route: GET /api/builds/:id/logs
    const logsMatch = url.pathname.match(/^\/api\/builds\/([^/]+)\/logs$/);
    if (logsMatch) {
        const buildId = logsMatch[1];
        res.writeHead(200, {
            'Content-Type': 'text/event-stream',
            'Cache-Control': 'no-cache',
            'Connection': 'keep-alive',
        });
        res.write(':ok\n\n');

        if (!activeSims.has(buildId)) {
            // Start simulation if not started from events endpoint
            const sim = { eventClients: new Set(), logClients: new Set() };
            activeSims.set(buildId, sim);
            const eventProxy = {
                write: (chunk) => { for (const c of sim.eventClients) { try { c.write(chunk); } catch { } } },
            };
            const logProxy = {
                write: (chunk) => { for (const c of sim.logClients) { try { c.write(chunk); } catch { } } },
            };
            simulateBuild(buildId, eventProxy, logProxy).then(() => {
                setTimeout(() => activeSims.delete(buildId), 5000);
            });
        }

        activeSims.get(buildId).logClients.add(res);
        req.on('close', () => activeSims.get(buildId)?.logClients.delete(res));
        return;
    }

    // 404
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'Not found' }));
});

server.listen(PORT, () => {
    console.log(`\n  ${C.cyan}⚡ Forge CI SSE Server${C.reset}`);
    console.log(`  ${C.dim}Listening on${C.reset} http://localhost:${PORT}`);
    console.log(`  ${C.dim}Endpoints:${C.reset}`);
    console.log(`    GET /api/builds/:id          ${C.dim}→ build metadata${C.reset}`);
    console.log(`    GET /api/builds/:id/events   ${C.dim}→ SSE vertex events${C.reset}`);
    console.log(`    GET /api/builds/:id/logs     ${C.dim}→ SSE log stream${C.reset}`);
    console.log(`\n  ${C.yellow}Try:${C.reset} curl -N http://localhost:${PORT}/api/builds/demo-001/events\n`);
});

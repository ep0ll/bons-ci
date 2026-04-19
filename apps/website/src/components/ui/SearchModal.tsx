/**
 * Forge CI — Global Search Modal (⌘K)
 * Neo-brutalist styled, keyboard-navigable, in-memory fuzzy search
 */
import React, { useState, useEffect, useRef, useCallback } from 'react';
import { BRUT } from '../dashboard/Observability.tsx';

// ── Search index ──────────────────────────────────────────────
interface SearchResult {
    id: string;
    section: 'Builds' | 'Docs' | 'Blog' | 'Changelog' | 'Navigate';
    icon: string;
    title: string;
    subtitle: string;
    href: string;
    accent?: string;
}

const STATIC_INDEX: SearchResult[] = [
    // Navigation
    { id: 'nav-dashboard', section: 'Navigate', icon: '▣', title: 'Dashboard', subtitle: 'Overview & metrics', href: '/dashboard', accent: BRUT.accent },
    { id: 'nav-builds', section: 'Navigate', icon: '▶', title: 'Builds', subtitle: 'All build history', href: '/dashboard/builds', accent: BRUT.accent },
    { id: 'nav-insights', section: 'Navigate', icon: '◈', title: 'Insights', subtitle: 'Heatmap, waterfall, p99', href: '/dashboard/insights', accent: BRUT.accent },
    { id: 'nav-ai', section: 'Navigate', icon: '◎', title: 'Sherlock AI', subtitle: 'AI failure analysis', href: '/dashboard/ai-agents', accent: BRUT.cyan },
    { id: 'nav-docs', section: 'Navigate', icon: '◌', title: 'Docs', subtitle: 'Documentation', href: '/docs', accent: BRUT.t2 },
    { id: 'nav-blog', section: 'Navigate', icon: '◆', title: 'Blog', subtitle: 'Engineering & product news', href: '/blog', accent: BRUT.t2 },
    { id: 'nav-changelog', section: 'Navigate', icon: '▤', title: 'Changelog', subtitle: "What's new", href: '/changelog', accent: BRUT.t2 },
    { id: 'nav-pricing', section: 'Navigate', icon: '◉', title: 'Pricing', subtitle: 'Plans & billing', href: '/pricing', accent: BRUT.t2 },
    // Builds
    { id: 'b1847', section: 'Builds', icon: '✓', title: '#1847 api-service · main', subtitle: 'success · 2m 0s · Ada Lovelace', href: '/dashboard/builds/b1847', accent: BRUT.green },
    { id: 'b1846', section: 'Builds', icon: '▶', title: '#1846 web-app · feat/dashboard', subtitle: 'running · Tomás Reyes', href: '/dashboard/builds/b1846', accent: BRUT.cyan },
    { id: 'b1845', section: 'Builds', icon: '✕', title: '#1845 api-service · fix/null-pointer', subtitle: 'failed · 2m 11s · Priya Nair', href: '/dashboard/builds/b1845', accent: BRUT.red },
    // Docs
    { id: 'doc-qs', section: 'Docs', icon: '◌', title: 'Quick Start', subtitle: 'Getting Started', href: '/docs/getting-started', accent: BRUT.blue },
    { id: 'doc-otel', section: 'Docs', icon: '◌', title: 'OpenTelemetry Integration', subtitle: 'Observability', href: '/docs/observability/otel', accent: BRUT.blue },
    { id: 'doc-cache', section: 'Docs', icon: '◌', title: 'Caching Overview', subtitle: 'Caching', href: '/docs/caching/overview', accent: BRUT.blue },
    { id: 'doc-yaml', section: 'Docs', icon: '◌', title: 'Pipeline YAML Reference', subtitle: 'Pipelines', href: '/docs/pipelines/yaml-reference', accent: BRUT.blue },
    { id: 'doc-secrets', section: 'Docs', icon: '◌', title: 'Secrets & OIDC', subtitle: 'Secrets', href: '/docs/secrets/overview', accent: BRUT.blue },
    { id: 'doc-api', section: 'Docs', icon: '◌', title: 'API Reference', subtitle: 'API Reference', href: '/docs/api/overview', accent: BRUT.blue },
    // Blog
    { id: 'blog-sherlock', section: 'Blog', icon: '◆', title: 'Sherlock AI is now GA', subtitle: 'Product · Apr 10, 2025', href: '/blog/sherlock-ga', accent: BRUT.purple },
    { id: 'blog-byoc', section: 'Blog', icon: '◆', title: 'BYOC now supports ARM64', subtitle: 'Engineering · Mar 28', href: '/blog/byoc-arm64', accent: BRUT.purple },
    { id: 'blog-cache', section: 'Blog', icon: '◆', title: 'How we achieve 89% cache hits', subtitle: 'Engineering · Mar 14', href: '/blog/cache-deep-dive', accent: BRUT.purple },
    // Changelog
    { id: 'cl-240', section: 'Changelog', icon: '▤', title: 'v2.4.0 — Sherlock GA, BYOC ARM64, OTel', subtitle: 'Apr 10, 2025', href: '/changelog', accent: BRUT.orange },
    { id: 'cl-230', section: 'Changelog', icon: '▤', title: 'v2.3.0 — Heatmap, SCIM', subtitle: 'Mar 1, 2025', href: '/changelog', accent: BRUT.orange },
];

function fuzzyScore(query: string, text: string): number {
    const q = query.toLowerCase();
    const t = text.toLowerCase();
    if (t.includes(q)) return q.length * 2;
    let i = 0; let score = 0;
    for (const ch of t) { if (i < q.length && ch === q[i]) { score++; i++; } }
    return i === q.length ? score : 0;
}

function search(query: string): SearchResult[] {
    if (!query.trim()) return STATIC_INDEX.filter(r => r.section === 'Navigate').slice(0, 8);
    const scored = STATIC_INDEX
        .map(r => ({ r, s: fuzzyScore(query, r.title + ' ' + r.subtitle) }))
        .filter(x => x.s > 0)
        .sort((a, b) => b.s - a.s)
        .slice(0, 12)
        .map(x => x.r);
    return scored;
}

// ── Component ──────────────────────────────────────────────────
export default function SearchModal() {
    const [open, setOpen] = useState(false);
    const [query, setQuery] = useState('');
    const [selected, setSelected] = useState(0);
    const inputRef = useRef<HTMLInputElement>(null);
    const listRef = useRef<HTMLDivElement>(null);

    const results = search(query);

    const openModal = useCallback(() => { setOpen(true); setQuery(''); setSelected(0); }, []);
    const closeModal = useCallback(() => setOpen(false), []);

    useEffect(() => {
        const handler = (e: KeyboardEvent) => {
            if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); openModal(); }
            if (e.key === 'Escape') closeModal();
        };
        window.addEventListener('keydown', handler);
        return () => window.removeEventListener('keydown', handler);
    }, [openModal, closeModal]);

    useEffect(() => {
        if (open) setTimeout(() => inputRef.current?.focus(), 60);
    }, [open]);

    useEffect(() => { setSelected(0); }, [query]);

    const handleKeyDown = (e: React.KeyboardEvent) => {
        if (e.key === 'ArrowDown') { e.preventDefault(); setSelected(s => Math.min(s + 1, results.length - 1)); }
        if (e.key === 'ArrowUp') { e.preventDefault(); setSelected(s => Math.max(s - 1, 0)); }
        if (e.key === 'Enter' && results[selected]) { window.location.href = results[selected].href; }
    };

    useEffect(() => {
        const item = listRef.current?.children[selected] as HTMLElement | undefined;
        item?.scrollIntoView({ block: 'nearest' });
    }, [selected]);

    // Group results by section
    const grouped = results.reduce<Record<string, SearchResult[]>>((acc, r) => {
        (acc[r.section] = acc[r.section] ?? []).push(r);
        return acc;
    }, {});

    if (!open) {
        return (
            <button
                id="cmd-k-trigger"
                onClick={openModal}
                style={{
                    display: 'flex', alignItems: 'center', gap: 8,
                    padding: '6px 12px', border: `1px solid ${BRUT.border}`,
                    background: BRUT.surface, color: BRUT.t3,
                    fontFamily: '"JetBrains Mono", monospace', fontSize: 12,
                    cursor: 'pointer', borderRadius: 0,
                }}
                aria-label="Search (⌘K)"
            >
                <svg width="12" height="12" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="square" strokeWidth={2} d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                </svg>
                Search…
                <kbd style={{ border: `1px solid ${BRUT.border}`, padding: '1px 4px', fontSize: 10, color: '#444' }}>⌘K</kbd>
            </button>
        );
    }

    return (
        <div
            style={{ position: 'fixed', inset: 0, zIndex: 1000 }}
            role="dialog"
            aria-modal="true"
            aria-label="Search"
        >
            {/* Backdrop */}
            <div
                onClick={closeModal}
                style={{ position: 'absolute', inset: 0, background: 'rgba(0,0,0,0.85)' }}
            />

            {/* Panel */}
            <div style={{ position: 'relative', display: 'flex', justifyContent: 'center', paddingTop: '13vh', paddingInline: 16 }}>
                <div style={{
                    width: '100%', maxWidth: 560,
                    border: `2px solid ${BRUT.accent}`,
                    background: BRUT.bg,
                    boxShadow: `6px 6px 0 rgba(255,238,0,0.18)`,
                    borderRadius: 0,
                    overflow: 'hidden',
                }}>
                    {/* Input */}
                    <div style={{
                        display: 'flex', alignItems: 'center', gap: 12,
                        padding: '12px 16px',
                        borderBottom: `1px solid ${BRUT.border}`,
                    }}>
                        <svg width="16" height="16" fill="none" viewBox="0 0 24 24" stroke={BRUT.accent} style={{ flexShrink: 0 }}>
                            <path strokeLinecap="square" strokeWidth={2} d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                        </svg>
                        <input
                            ref={inputRef}
                            type="text"
                            value={query}
                            onChange={e => setQuery(e.target.value)}
                            onKeyDown={handleKeyDown}
                            placeholder="Search builds, docs, blog…"
                            style={{
                                flex: 1, background: 'transparent', border: 'none', outline: 'none',
                                color: BRUT.text, fontSize: 14,
                                fontFamily: '"JetBrains Mono", monospace',
                            }}
                            autoComplete="off"
                            spellCheck={false}
                        />
                        <kbd
                            onClick={closeModal}
                            style={{ border: `1px solid ${BRUT.border}`, padding: '2px 6px', fontSize: 11, color: '#666', cursor: 'pointer', fontFamily: 'inherit' }}
                        >ESC</kbd>
                    </div>

                    {/* Results */}
                    <div
                        ref={listRef}
                        style={{ maxHeight: 380, overflowY: 'auto', padding: '8px 0' }}
                    >
                        {results.length === 0 ? (
                            <div style={{ padding: '24px 16px', textAlign: 'center', color: BRUT.t3, fontFamily: '"JetBrains Mono", monospace', fontSize: 12 }}>
                                No results for "{query}"
                            </div>
                        ) : (
                            Object.entries(grouped).map(([section, items]) => (
                                <div key={section}>
                                    <div style={{
                                        padding: '6px 16px 4px',
                                        fontSize: 10, fontFamily: '"JetBrains Mono", monospace',
                                        color: BRUT.t4, textTransform: 'uppercase', letterSpacing: '0.12em',
                                    }}>
                                        {section}
                                    </div>
                                    {items.map(item => {
                                        const idx = results.indexOf(item);
                                        const isSelected = idx === selected;
                                        return (
                                            <a
                                                key={item.id}
                                                href={item.href}
                                                style={{
                                                    display: 'flex', alignItems: 'center', gap: 12,
                                                    padding: '8px 16px', textDecoration: 'none',
                                                    background: isSelected ? BRUT.s2 : 'transparent',
                                                    borderLeft: isSelected ? `2px solid ${BRUT.accent}` : '2px solid transparent',
                                                    transition: 'background 0.05s',
                                                }}
                                                onMouseEnter={() => setSelected(idx)}
                                            >
                                                <span style={{ fontSize: 14, color: item.accent ?? BRUT.t3, flexShrink: 0, width: 18, textAlign: 'center' }}>
                                                    {item.icon}
                                                </span>
                                                <div style={{ flex: 1, minWidth: 0 }}>
                                                    <div style={{ fontSize: 13, fontWeight: 600, color: BRUT.text, fontFamily: '"Space Grotesk", sans-serif', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                                                        {item.title}
                                                    </div>
                                                    <div style={{ fontSize: 11, color: BRUT.t3, fontFamily: '"JetBrains Mono", monospace' }}>
                                                        {item.subtitle}
                                                    </div>
                                                </div>
                                                {isSelected && (
                                                    <span style={{ fontSize: 10, color: BRUT.t4, fontFamily: '"JetBrains Mono", monospace', flexShrink: 0 }}>↵</span>
                                                )}
                                            </a>
                                        );
                                    })}
                                </div>
                            ))
                        )}
                    </div>

                    {/* Footer */}
                    <div style={{
                        padding: '8px 16px', borderTop: `1px solid ${BRUT.border}`,
                        display: 'flex', gap: 16,
                        fontFamily: '"JetBrains Mono", monospace', fontSize: 10, color: BRUT.t4,
                    }}>
                        <span>↑↓ navigate</span>
                        <span>↵ select</span>
                        <span>ESC close</span>
                    </div>
                </div>
            </div>
        </div>
    );
}

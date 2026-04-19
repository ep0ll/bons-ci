# Forge CI — Website Source

> **The fastest CI/CD platform for modern engineering teams.**
> 10× faster builds · Sherlock AI agent · any cloud · SOC 2 Type II

Full-stack marketing + product website for Forge CI. Built with Astro 4, TypeScript strict mode, Tailwind CSS, and zero external UI dependencies.

---

## 🚀 Quick start

```bash
git clone https://github.com/forge-ci/website
cd forge-ci
npm install
npm run dev      # → http://localhost:4321
npm run build    # Production build → dist/
npm run test     # Run 37 unit tests
```

**Requirements:** Node.js ≥ 18 · npm ≥ 9

---

## 📁 Project structure

```
forge-ci/
├── public/
│   └── favicon.svg
│
├── src/
│   ├── components/
│   │   └── layout/
│   │       ├── Header.astro      # Sticky glass header with mega-menu flyouts
│   │       └── Footer.astro      # 5-column nav + newsletter + social + trust badges
│   │
│   ├── data/
│   │   └── mock.ts               # 600+ lines of typed, deterministic mock data
│   │
│   ├── layouts/
│   │   ├── BaseLayout.astro      # HTML shell, SEO meta, OG/Twitter, structured data
│   │   ├── MarketingLayout.astro # Header + Footer wrapper
│   │   ├── AuthLayout.astro      # Split panel: form left, live build stream right
│   │   └── DashboardLayout.astro # Sidebar + topbar + ⌘K command palette
│   │
│   ├── lib/
│   │   ├── auth.ts               # RBAC, sessions, SSO, password strength, slugify
│   │   └── charts.ts             # Pure TS SVG chart utilities (no DOM deps, SSR-safe)
│   │
│   ├── pages/                    # 37 total pages
│   │   ├── index.astro           # Landing page
│   │   ├── features.astro        # Feature deep-dive
│   │   ├── pricing.astro         # Pricing + full comparison matrix
│   │   ├── integrations.astro    # 15+ integrations with search/filter
│   │   ├── changelog.astro       # Versioned release timeline
│   │   ├── blog.astro            # Blog listing + category filter
│   │   ├── blog/[slug].astro     # Blog post detail
│   │   ├── customers.astro       # Case studies + testimonials
│   │   ├── enterprise.astro      # Enterprise landing + contact form
│   │   ├── security.astro        # Security trust page
│   │   ├── byoc.astro            # BYOC runners page
│   │   ├── docs.astro            # Documentation hub
│   │   ├── status.astro          # System status + incident history
│   │   ├── 404.astro             # Custom 404
│   │   │
│   │   ├── auth/
│   │   │   ├── login.astro       # OAuth + SSO + email/password + MFA
│   │   │   ├── signup.astro      # 4-step wizard: account → org → plan → verify
│   │   │   ├── forgot-password.astro
│   │   │   ├── sso.astro         # SAML 2.0 IdP config
│   │   │   └── onboarding.astro  # 4-step: repo → pipeline → secrets → first build
│   │   │
│   │   └── dashboard/
│   │       ├── index.astro       # Overview with real charts from chart utilities
│   │       ├── builds/
│   │       │   ├── index.astro   # Build list with filters, search, Sherlock button
│   │       │   └── [id].astro    # Build detail: logs, steps, resource charts, Sherlock
│   │       ├── projects.astro
│   │       ├── registry.astro    # OCI image registry + vulnerability scan
│   │       ├── artifacts.astro
│   │       ├── templates.astro   # Pipeline templates + YAML preview drawer
│   │       ├── marketplace.astro # Plugin marketplace + install/remove
│   │       ├── ai-agents.astro   # Sherlock AI hub + live chat interface
│   │       ├── sandboxes.astro   # Ephemeral dev environments
│   │       ├── insights.astro    # Analytics: heatmap, team activity, anomalies
│   │       ├── cache.astro       # Cache key management + hit rate charts
│   │       └── settings/
│   │           ├── index.astro   # General, billing, plan, danger zone (tabbed)
│   │           ├── members.astro # RBAC table, invite modal, permissions matrix
│   │           ├── secrets.astro # Env secrets + API tokens with scope badges
│   │           ├── security.astro # SSO, MFA enforcement, sessions, audit log
│   │           ├── integrations.astro
│   │           └── cache.astro   # Cache defaults + egress firewall rules
│   │
│   ├── styles/
│   │   └── global.css            # Full design system: tokens, components, animations
│   │
│   ├── tests/
│   │   └── unit/
│   │       └── core.test.ts      # 37 unit tests (all passing)
│   │
│   └── types/
│       └── index.ts              # 400+ lines of strict TypeScript interfaces
│
├── astro.config.mjs
├── tailwind.config.mjs
├── tsconfig.json
└── package.json
```

---

## 🎨 Design system

### Color palette

| Token              | Value      | Usage                            |
|--------------------|------------|----------------------------------|
| `--bg`             | `#07090E`  | Page background                  |
| `--surface`        | `#0C1018`  | Cards, sidebar, panels           |
| `--surface-2`      | `#111827`  | Hover states, table rows         |
| `--border`         | `#1E2D42`  | Dividers, card borders           |
| `--accent`         | `#C8FF00`  | CTAs, active states, highlights  |
| `--cyan`           | `#22D3EE`  | Sherlock AI, info, secondary     |
| `--text-1`         | `#E8F0FF`  | Primary text                     |
| `--text-2`         | `#94A3B8`  | Secondary/body text              |
| `--text-3`         | `#4B5E7A`  | Muted, placeholder               |
| `--success`        | `#10B981`  | Pass, connected, operational     |
| `--warning`        | `#F59E0B`  | Degraded, caution, expiring      |
| `--danger`         | `#EF4444`  | Failed, blocked, critical        |
| `--info`           | `#3B82F6`  | Info badges, running             |

### Typography

| Font              | Variable           | Usage                     |
|-------------------|--------------------|---------------------------|
| Syne              | `font-display`     | All headings and display  |
| Plus Jakarta Sans | `font-sans`        | Body text, UI copy        |
| JetBrains Mono    | `font-mono`        | Code, terminals, metrics  |

### CSS component classes

```css
/* Buttons */       .btn-primary .btn-secondary .btn-ghost .btn-danger .btn-sm .btn-md .btn-lg .btn-xl
/* Cards */         .card .card-hover .card-glow
/* Inputs */        .input .input-label
/* Badges */        .badge .badge-success .badge-warning .badge-danger .badge-info .badge-accent .badge-neutral
/* Tables */        .table-forge
/* Layout */        .container-forge .section .section-title .section-label .section-subtitle
/* Typography */    .text-gradient .text-gradient-lime .glow-text
/* Misc */          .glass .terminal .terminal-header .terminal-dot .progress .progress-bar .avatar
```

---

## 🛠 Tech stack

| Layer            | Technology                              |
|------------------|-----------------------------------------|
| Framework        | Astro 4 (static output, SSR-compatible) |
| Styling          | Tailwind CSS 3 + CSS custom properties  |
| Language         | TypeScript (strict mode throughout)     |
| Fonts            | Google Fonts (Syne, Plus Jakarta, JetBrains Mono) |
| Icons            | Hand-crafted inline SVG (zero deps)     |
| Animations       | Pure CSS keyframes                      |
| Interactivity    | Vanilla TypeScript via `<script>` tags  |
| Charts           | Custom SVG generation (no chart libs)   |

---

## 📄 Page inventory (37 pages)

### Marketing (14)
`/` `/features` `/pricing` `/integrations` `/changelog` `/blog` `/blog/[slug]`
`/customers` `/enterprise` `/security` `/byoc` `/docs` `/status` `/404`

### Auth (5)
`/auth/login` `/auth/signup` `/auth/forgot-password` `/auth/sso` `/auth/onboarding`

### Dashboard (18)
`/dashboard` `/dashboard/builds` `/dashboard/builds/[id]` `/dashboard/projects`
`/dashboard/registry` `/dashboard/artifacts` `/dashboard/templates` `/dashboard/marketplace`
`/dashboard/ai-agents` `/dashboard/sandboxes` `/dashboard/insights` `/dashboard/cache`
`/dashboard/settings` `/dashboard/settings/members` `/dashboard/settings/secrets`
`/dashboard/settings/security` `/dashboard/settings/integrations` `/dashboard/settings/cache`

---

## ✅ Key features

### Landing page
- Animated terminal build stream with JS-sequenced log lines
- Company logo marquee (20 companies)
- Head-to-head benchmark comparison bars
- 6 feature cards with icon + hover glow
- Sherlock AI showcase with live mock diagnosis panel
- 6 testimonial cards with star ratings and metrics
- 2 full case studies with before/after metric tables
- Pricing tier cards + comparison matrix (30 rows × 4 plans)
- Blog preview cards
- Final CTA section with trust icons

### Dashboard
- **⌘K command palette** with search and navigation shortcuts
- **Overview**: 8 metric cards, SVG build volume line chart, resource gauges (CPU, mem, net, disk), 4 sparkline KPI cards, waterfall P50–P99 percentile chart, recent builds table, plan usage bars
- **Builds list**: Status tabs with live counts, multi-field search, project/trigger/date filters, checkbox multi-select, Sherlock shortcut on failures, pagination
- **Build detail**: Step timeline with proportional duration bar, 460px log viewer with regex search + level filter + timestamp toggle, Sherlock AI panel with code diff, resource usage SVG charts (CPU/mem + net I/O), artifacts panel, test results with coverage bar
- **Insights**: Sparkline KPIs, build volume line chart, 7-day × 24-hour heatmap, project performance table, team activity bars, ML anomaly feed
- **Registry**: Repo list with vuln badges, OCI tag table with digest + platform + vulnerability scan
- **Sherlock AI**: Confidence-ring donut charts on analyses, pattern detection cards, live chat interface with canned AI responses
- **Sandboxes**: Resource meters, port forwarding, embedded terminal panels
- **Templates**: Category filter, right-panel YAML preview drawer with copy
- **Settings/Members**: Full RBAC permissions matrix, invite modal with SSO note

### Auth flows
- **Login**: GitHub/Google OAuth, SSO domain input with redirect, email/password with toggle, MFA 6-digit auto-advance
- **Signup**: 4-step wizard, real-time slug availability check, password strength meter, plan selection
- **SSO**: Per-provider setup guides (Okta, Azure AD, Google Workspace, custom), Forge CI endpoint display with copy

### Charts (all pure TypeScript, SSR-safe)
- `seriesToSmoothPath()` — cubic bezier smooth line path
- `seriesToAreaPath()` — filled area for gradients
- `generateWaterfallBars()` — P50/P75/P90/P95/P99 stacked bar chart
- `generateHeatmapData()` — 7×24 build activity heatmap
- `generateResourceTimeline()` — realistic CPU/mem resource curves
- `filterLogLines()` — regex/text search with level and stream filters
- `highlightMatches()` — text highlight segments for log viewer

---

## 🧪 Tests

```bash
npm test        # Runs 37 unit tests
```

**Test coverage:**
- RBAC role hierarchy (8 tests)
- Email/password/slug validation (6 tests)
- Chart series generation (3 tests)
- Waterfall percentile ordering (3 tests)
- Log line filtering (5 tests)
- Data format utilities (7 tests)
- Data integrity (5 tests)

---

## 📦 Build and deploy

```bash
npm run build
# Output: dist/ (static, ready to deploy anywhere)

# Vercel
npx vercel --prod

# Netlify
npx netlify deploy --prod --dir=dist

# Cloudflare Pages
npx wrangler pages deploy dist
```

---

## 📝 License

© 2025 Forge CI, Inc. All rights reserved.

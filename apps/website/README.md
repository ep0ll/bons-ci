# Forge CI — Website

> **The fastest CI/CD platform for modern engineering teams.**
> 10× faster builds · Sherlock AI agent · any cloud · fully responsive.

This is the full marketing + product website for Forge CI, built with [Astro](https://astro.build), Tailwind CSS, and TypeScript. It covers every major page a CI/CD SaaS platform needs — from landing pages and pricing to a complete authenticated dashboard.

---

## 🚀 Quick start

```bash
# Clone and install
git clone https://github.com/forge-ci/website
cd website
npm install

# Start dev server
npm run dev
# → http://localhost:4321

# Build for production
npm run build

# Preview production build
npm run preview
```

**Requirements:** Node.js ≥ 18, npm ≥ 9

---

## 📁 Project structure

```
forge-ci/
├── public/
│   └── favicon.svg
│
├── src/
│   ├── components/
│   │   ├── layout/
│   │   │   ├── Header.astro       # Marketing nav with mega-menu + mobile drawer
│   │   │   └── Footer.astro       # Full footer with newsletter + social links
│   │   │
│   │   ├── marketing/
│   │   │   ├── Hero.astro         # Animated pipeline terminal hero
│   │   │   ├── Features.astro     # 8-card feature grid + enterprise pillars
│   │   │   ├── Pricing.astro      # Tier cards + FAQ accordion
│   │   │   ├── Testimonials.astro # Quote cards + logo marquee
│   │   │   ├── Integrations.astro # 6-category integration grid
│   │   │   ├── SherlockShowcase.astro  # Sherlock AI feature demo
│   │   │   └── CTA.astro          # Bottom conversion section
│   │   │
│   │   └── ui/
│   │       ├── Button.astro       # All button variants + sizes
│   │       ├── Badge.astro        # Status badge variants
│   │       └── Card.astro         # Card with hover/glow variants
│   │
│   ├── layouts/
│   │   ├── BaseLayout.astro       # HTML shell, SEO meta, OG tags, fonts
│   │   ├── MarketingLayout.astro  # Header + Footer wrapper
│   │   ├── AuthLayout.astro       # Split panel: form + live build stream
│   │   └── DashboardLayout.astro  # Sidebar + topbar + command palette
│   │
│   ├── pages/
│   │   ├── index.astro            # Landing page
│   │   ├── features.astro         # Deep feature breakdown (5 categories)
│   │   ├── pricing.astro          # Pricing + full feature comparison matrix
│   │   ├── integrations.astro     # 36+ integrations with live search/filter
│   │   ├── changelog.astro        # Versioned release timeline
│   │   ├── docs.astro             # Documentation hub
│   │   ├── status.astro           # System status + incident history
│   │   ├── 404.astro              # Custom not-found page
│   │   │
│   │   ├── auth/
│   │   │   ├── login.astro        # Email/OAuth/SSO + MFA panel
│   │   │   ├── signup.astro       # 4-step wizard: account → org → plan → verify
│   │   │   ├── sso.astro          # SAML 2.0 IdP config + SCIM setup
│   │   │   └── onboarding.astro   # Connect repo → pipeline → secrets → first build
│   │   │
│   │   └── dashboard/
│   │       ├── index.astro        # Overview: metrics, charts, recent builds
│   │       ├── projects.astro     # Project cards + new project modal
│   │       ├── artifacts.astro    # Artifact browser with type filter
│   │       ├── registry.astro     # OCI image registry + tag management
│   │       ├── templates.astro    # Pipeline template gallery + preview drawer
│   │       ├── marketplace.astro  # Plugin marketplace + install flow
│   │       ├── ai-agents.astro    # Sherlock AI hub + chat interface
│   │       ├── sandboxes.astro    # Ephemeral dev environments
│   │       ├── insights.astro     # Analytics, heatmaps, cost attribution
│   │       │
│   │       ├── builds/
│   │       │   ├── index.astro    # Build list with filters + search
│   │       │   └── [id].astro     # Build detail: logs + steps + Sherlock + artifacts
│   │       │
│   │       └── settings/
│   │           ├── index.astro    # General + billing + plan + danger zone
│   │           ├── members.astro  # RBAC + team members + invitations
│   │           ├── secrets.astro  # Env secrets + API tokens
│   │           └── cache.astro    # Cache keys + egress firewall rules
│   │
│   ├── styles/
│   │   └── global.css             # Design tokens, component classes, animations
│   │
│   └── types/
│       └── index.ts               # Shared TypeScript interfaces
│
├── astro.config.mjs
├── tailwind.config.mjs
├── tsconfig.json
└── package.json
```

---

## 🎨 Design system

### Color palette

| Token | Value | Usage |
|-------|-------|-------|
| `forge-bg` | `#07090E` | Page background |
| `forge-surface` | `#0C1018` | Cards, sidebar |
| `forge-surface2` | `#111827` | Hover states |
| `forge-border` | `#1E2D42` | Dividers, card borders |
| `forge-accent` | `#C8FF00` | CTA, active states, highlights |
| `forge-cyan` | `#22D3EE` | Secondary accent, Sherlock |
| `forge-text` | `#E8F0FF` | Primary text |
| `forge-text-2` | `#94A3B8` | Secondary text |
| `forge-text-3` | `#4B5E7A` | Muted / placeholder |

### Typography

| Font | Usage |
|------|-------|
| **Syne** | Headings, display text (`font-display`) |
| **Plus Jakarta Sans** | Body text, UI copy (`font-sans`) |
| **JetBrains Mono** | Code, terminals, metrics (`font-mono`) |

### Component classes (global.css)

```css
/* Buttons */
.btn-primary    /* Lime accent CTA */
.btn-secondary  /* Bordered ghost */
.btn-ghost      /* Transparent */
.btn-danger     /* Destructive actions */
.btn-sm / .btn-md / .btn-lg / .btn-xl

/* Cards */
.card           /* Base card */
.card-hover     /* Card with hover lift */

/* Inputs */
.input          /* Standard text input */
.input-label    /* Form label */

/* Badges */
.badge-success / .badge-warning / .badge-danger / .badge-info / .badge-accent / .badge-neutral

/* Typography */
.section-title  /* Large heading */
.section-label  /* Pill label */
.text-gradient  /* White → lime gradient text */

/* Layout */
.container-forge  /* Max-width centered container */
.section          /* Vertical section padding */
```

---

## 🛠 Tech stack

| Layer | Technology |
|-------|-----------|
| Framework | [Astro 4](https://astro.build) — static output, island architecture |
| Styling | [Tailwind CSS 3](https://tailwindcss.com) + custom design tokens |
| Types | TypeScript strict mode |
| Icons | Hand-crafted SVG — no external icon library |
| Fonts | Google Fonts (Syne, Plus Jakarta Sans, JetBrains Mono) |
| Animations | Pure CSS keyframes (no JS animation libraries) |
| Interactivity | Vanilla TS via `<script>` tags (minimal JS) |

---

## 📄 Page inventory

### Marketing (9 pages)
| Page | Route |
|------|-------|
| Landing | `/` |
| Features | `/features` |
| Pricing | `/pricing` |
| Integrations | `/integrations` |
| Changelog | `/changelog` |
| Documentation | `/docs` |
| Status | `/status` |
| 404 | `/404` |

### Auth (4 pages)
| Page | Route |
|------|-------|
| Login | `/auth/login` |
| Sign up | `/auth/signup` |
| SSO/SAML config | `/auth/sso` |
| Onboarding | `/auth/onboarding` |

### Dashboard (13 pages)
| Page | Route |
|------|-------|
| Overview | `/dashboard` |
| Builds list | `/dashboard/builds` |
| Build detail | `/dashboard/builds/[id]` |
| Projects | `/dashboard/projects` |
| Image registry | `/dashboard/registry` |
| Artifacts | `/dashboard/artifacts` |
| Templates | `/dashboard/templates` |
| Marketplace | `/dashboard/marketplace` |
| Sherlock AI | `/dashboard/ai-agents` |
| Sandboxes | `/dashboard/sandboxes` |
| Insights | `/dashboard/insights` |
| Settings | `/dashboard/settings` |
| Members | `/dashboard/settings/members` |
| Secrets & Tokens | `/dashboard/settings/secrets` |
| Cache & Network | `/dashboard/settings/cache` |

---

## 🧪 Key features implemented

### Marketing
- ✅ Animated pipeline terminal in hero (JS-sequenced log lines)
- ✅ Testimonials with auto-scrolling logo marquee
- ✅ Pricing tier cards with annual/monthly toggle
- ✅ Full feature comparison matrix (30 rows × 4 plans)
- ✅ Integrations page with live search + category filter
- ✅ Changelog with versioned timeline
- ✅ Documentation hub with search, quickstart cards, API preview

### Auth
- ✅ OAuth (GitHub, Google) + SSO button with domain input
- ✅ MFA digit panel with auto-focus
- ✅ 4-step signup wizard with org creation + plan selection
- ✅ SAML 2.0 IdP picker, metadata upload, SCIM toggle
- ✅ Password strength meter
- ✅ Onboarding: repo connect → YAML preview → secrets → first build

### Dashboard
- ✅ Sidebar with usage bar, org switcher, command palette (⌘K)
- ✅ Overview: 8 metric cards, SVG build volume chart, resource gauges, 4 sparklines
- ✅ Builds: status tabs, search, project/trigger/date filters, Sherlock button on failures
- ✅ Build detail: step timeline (proportional), 480px log viewer, log search, Sherlock AI panel with code diff
- ✅ Projects: card grid with stats, new project modal, search filter
- ✅ Registry: repo list, tag table with digests + vulnerabilities, pull commands, retention policy
- ✅ Artifacts: type filter tabs, multi-select, expiry warnings
- ✅ Templates: 9 templates, preview drawer with YAML copy
- ✅ Marketplace: 18 plugins, install/remove toggle, search + category filter
- ✅ Sherlock: analysis history with confidence rings, pattern detection, chat interface
- ✅ Sandboxes: live resource meters, port forwarding, embedded terminal
- ✅ Insights: KPI cards, stacked bar chart, day/hour heatmap, team activity, anomalies
- ✅ Settings: tabbed (general/billing/plan/danger), invoices, plan comparison
- ✅ Members: RBAC table, invite modal, full permissions matrix
- ✅ Secrets: masked values, rotation dates, API token scopes, create modals
- ✅ Cache: hit-rate bars, TTL per key, egress firewall rules with drag ordering

---

## 📦 Build and deploy

```bash
# Production build
npm run build
# Output: dist/

# Deploy to Vercel
npx vercel --prod

# Deploy to Netlify
npx netlify deploy --prod --dir=dist

# Deploy to Cloudflare Pages
npx wrangler pages deploy dist
```

---

## 📝 License

© 2025 Forge CI, Inc. All rights reserved.

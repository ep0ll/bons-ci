// ============================================================
// FORGE CI — Docs Mock Data
// ============================================================
import type { DocPage } from '../types/index.ts';

export const DOCS: DocPage[] = [
    // ── Getting Started ─────────────────────────────────────────
    {
        slug: 'getting-started',
        title: 'Quick Start',
        category: 'Getting Started',
        order: 1,
        excerpt: 'Connect your repo, push a commit, and watch Forge CI run your first build in under 2 minutes.',
        content: `## Prerequisites
- A GitHub, GitLab, or Bitbucket account
- A repository you own or have admin access to

## Step 1 — Sign up
Go to [forge-ci.dev/auth/signup](/auth/signup) and create a free account. No credit card required.

## Step 2 — Connect your VCS
During onboarding, authorise Forge CI to access your GitHub / GitLab / Bitbucket account. We request the minimum permissions required to receive webhook events and write commit statuses.

## Step 3 — Import a project
Click **New Project**, select your repository, and confirm the default branch. Forge CI will detect your runtime and suggest a starter pipeline.

## Step 4 — Add a pipeline file
Create \`.forge/pipeline.yml\` in the root of your repo:

\`\`\`yaml
version: '2'
pipelines:
  default:
    - step:
        name: Install & Test
        caches:
          - node
        script:
          - npm ci
          - npm test
\`\`\`

## Step 5 — Push and watch
Push to \`main\`. Forge CI picks up the webhook, queues a runner, and starts your build. Click the build in the dashboard to watch live logs.`,
        toc: [
            { id: 'prerequisites', label: 'Prerequisites', depth: 2 },
            { id: 'step-1-sign-up', label: 'Sign up', depth: 2 },
            { id: 'step-2-connect-your-vcs', label: 'Connect your VCS', depth: 2 },
            { id: 'step-3-import-a-project', label: 'Import a project', depth: 2 },
            { id: 'step-4-add-a-pipeline-file', label: 'Add a pipeline file', depth: 2 },
            { id: 'step-5-push-and-watch', label: 'Push and watch', depth: 2 },
        ],
    },
    {
        slug: 'getting-started/concepts',
        title: 'Core Concepts',
        category: 'Getting Started',
        order: 2,
        excerpt: 'Learn about pipelines, steps, runners, caches, and how they fit together.',
        content: `## Pipelines
A pipeline is a YAML-defined sequence of steps. Pipelines are triggered by VCS events (push, PR, schedule) or via the API.

## Steps
Steps are the atomic unit of work. Each step runs in a fresh shell inside a container on a runner. Steps can be parallelised with \`parallel:\` blocks or fanned out with \`matrix:\`.

## Runners
Runners are the compute units that execute steps. Forge CI provides hosted runners (Linux x64/ARM64, macOS M2, Windows). You can also register BYOC (Bring Your Own Cloud) runners.

## Caches
Caches persist directories between builds. Forge CI uses content-addressed caching (Blake3) for high hit rates. Declare caches by key:

\`\`\`yaml
caches:
  - node          # built-in: caches node_modules
  - custom-key    # custom: you define the paths
\`\`\`

## Artifacts
Artifacts are files produced by a build and stored for later download. Define them in your step \`artifacts:\` block.

## Credits
Credits are the billing unit. Each runner-minute costs a certain number of credits depending on runner size. Your plan includes a monthly credit allocation.`,
        toc: [
            { id: 'pipelines', label: 'Pipelines', depth: 2 },
            { id: 'steps', label: 'Steps', depth: 2 },
            { id: 'runners', label: 'Runners', depth: 2 },
            { id: 'caches', label: 'Caches', depth: 2 },
            { id: 'artifacts', label: 'Artifacts', depth: 2 },
            { id: 'credits', label: 'Credits', depth: 2 },
        ],
    },

    // ── Pipelines ──────────────────────────────────────────────
    {
        slug: 'pipelines/yaml-reference',
        title: 'Pipeline YAML Reference',
        category: 'Pipelines',
        order: 10,
        excerpt: 'Complete reference for the .forge/pipeline.yml v2 schema.',
        content: `## Top-level keys

\`\`\`yaml
version: '2'          # required
image: node:20        # default image for all steps
pipelines:
  default: [...]      # runs on every push
  branches:
    main: [...]       # runs only on main
  pull-requests:
    '**': [...]       # runs on all PRs
  custom:
    release: [...]    # triggered manually or via API
\`\`\`

## Step definition

\`\`\`yaml
- step:
    name: Test            # display name
    image: node:20        # override default image
    runs-on: linux-arm64  # runner override
    timeout: 30           # minutes (default 60)
    caches: [node]
    artifacts:
      - junit.xml
      - coverage/
    script:
      - npm ci
      - npm test -- --ci --coverage
    after-script:         # always runs, even on failure
      - bash cleanup.sh
\`\`\`

## Parallel steps

\`\`\`yaml
- parallel:
    - step:
        name: Lint
        script: [npm run lint]
    - step:
        name: Type check
        script: [npm run typecheck]
\`\`\`

## Matrix builds

\`\`\`yaml
- parallel:
    matrix:
      - NODE_VERSION: ['18', '20', '22']
    steps:
      - step:
          name: Test (Node $NODE_VERSION)
          image: node:$NODE_VERSION
          script: [npm ci, npm test]
\`\`\``,
        toc: [
            { id: 'top-level-keys', label: 'Top-level keys', depth: 2 },
            { id: 'step-definition', label: 'Step definition', depth: 2 },
            { id: 'parallel-steps', label: 'Parallel steps', depth: 2 },
            { id: 'matrix-builds', label: 'Matrix builds', depth: 2 },
        ],
    },

    // ── Caching ─────────────────────────────────────────────────
    {
        slug: 'caching/overview',
        title: 'Caching Overview',
        category: 'Caching',
        order: 20,
        excerpt: 'How Forge CI\'s content-addressed cache works and how to maximise hit rates.',
        content: `## How it works
Forge CI uses **content-addressed caching** with Blake3 hashes. The cache key is derived from the file(s) you specify as key inputs — not a user-supplied string.

This means:
- Cache invalidation is automatic when inputs change
- No stale caches from key mismatches
- Shared cache across runners in the same org

## Built-in cache definitions

| Key    | Paths cached               | Invalidation input     |
|--------|---------------------------|------------------------|
| node   | node_modules/             | package-lock.json      |
| pip    | ~/.cache/pip              | requirements.txt       |
| go     | ~/go/pkg/mod              | go.sum                 |
| gradle | ~/.gradle/caches          | build.gradle           |
| maven  | ~/.m2/repository          | pom.xml                |
| cargo  | ~/.cargo/registry         | Cargo.lock             |

## Hit rate tips

1. **Lock file precision** — always commit lock files (package-lock.json, go.sum) so the cache key is stable
2. **Layer order in Docker** — copy lock file before source code in your Dockerfile
3. **Split caches** — use separate cache keys for \`node_modules\` and Docker layers to maximise partial hits
4. **Avoid writing into cached dirs** — writes inside a cached directory during a build increase restore time`,
        toc: [
            { id: 'how-it-works', label: 'How it works', depth: 2 },
            { id: 'built-in-cache-definitions', label: 'Built-in caches', depth: 2 },
            { id: 'hit-rate-tips', label: 'Hit rate tips', depth: 2 },
        ],
    },

    // ── Observability ───────────────────────────────────────────
    {
        slug: 'observability/otel',
        title: 'OpenTelemetry Integration',
        category: 'Observability',
        order: 30,
        excerpt: 'Export build traces, spans, and metrics to any OTLP endpoint.',
        content: `## What gets exported

Forge CI supports OpenTelemetry (OTel) export for:

| Signal  | Description                                |
|---------|--------------------------------------------|
| Traces  | One trace per build, one span per step     |
| Metrics | Build duration, cache hit rate, queue time |
| Logs    | Structured step log lines with severity    |

## Configuring OTLP export

In your project settings, set:

| Variable             | Description                              |
|----------------------|------------------------------------------|
| OTLP_ENDPOINT        | e.g. https://tempo.myco.com:4317         |
| OTLP_HEADERS         | e.g. Authorization=Bearer <token>        |
| OTLP_PROTOCOL        | grpc (default) or http/protobuf          |

## Trace structure

\`\`\`
build.run (root span)
  ├── runner.provision
  ├── cache.restore
  ├── step.install
  ├── step.lint
  ├── step.test
  │     ├── jest.suite.UserService
  │     └── jest.suite.AuthService
  ├── step.build
  └── step.push_image
\`\`\`

## Waterfall view

Every build in the Forge CI dashboard shows a waterfall chart derived from the OTel trace. Each step is a horizontal bar — hover for resource details (CPU, memory, network I/O).

## Heatmap

The Insights page renders a day-of-week × hour-of-day heatmap of build activity using aggregated metrics. Used for capacity planning and identifying CI hot spots.`,
        toc: [
            { id: 'what-gets-exported', label: 'What gets exported', depth: 2 },
            { id: 'configuring-otlp-export', label: 'Configuring OTLP', depth: 2 },
            { id: 'trace-structure', label: 'Trace structure', depth: 2 },
            { id: 'waterfall-view', label: 'Waterfall view', depth: 2 },
            { id: 'heatmap', label: 'Heatmap', depth: 2 },
        ],
    },

    // ── Secrets ─────────────────────────────────────────────────
    {
        slug: 'secrets/overview',
        title: 'Secrets & OIDC',
        category: 'Secrets',
        order: 40,
        excerpt: 'Store encrypted secrets and use OIDC for zero long-lived credential builds.',
        content: `## Encrypted secrets
Secrets are encrypted at rest with AES-256-GCM and injected as environment variables at step start time. They are never logged or exposed in artifact uploads.

## Scopes
- **Org secrets** — available to all projects in the org
- **Project secrets** — scoped to a single project
- **Environment secrets** — scoped to a project + environment (staging, production)

## OIDC (recommended)
Use OIDC token exchange to assume AWS IAM roles, GCP service accounts, or Azure federated credentials without storing any long-lived key:

\`\`\`yaml
- step:
    name: Deploy to AWS
    oidc:
      role_arn: arn:aws:iam::123456789012:role/forge-ci-deploy
      region: us-east-1
    script:
      - aws s3 sync dist/ s3://my-bucket/
\`\`\`

Forge CI mints a short-lived OIDC token, exchanges it for AWS temporary credentials, and injects \`AWS_ACCESS_KEY_ID\`, \`AWS_SECRET_ACCESS_KEY\`, and \`AWS_SESSION_TOKEN\` into the step environment automatically.`,
        toc: [
            { id: 'encrypted-secrets', label: 'Encrypted secrets', depth: 2 },
            { id: 'scopes', label: 'Scopes', depth: 2 },
            { id: 'oidc-recommended', label: 'OIDC', depth: 2 },
        ],
    },

    // ── API Reference ───────────────────────────────────────────
    {
        slug: 'api/overview',
        title: 'API Reference',
        category: 'API Reference',
        order: 50,
        excerpt: 'The Forge CI REST API — authentication, builds, projects, metrics.',
        content: `## Base URL
\`\`\`
https://api.forge-ci.dev/v1
\`\`\`

## Authentication
Pass your API token in the \`Authorization\` header:
\`\`\`
Authorization: Bearer fci_live_<token>
\`\`\`

## Trigger a build
\`\`\`http
POST /v1/projects/{project_slug}/builds
Content-Type: application/json

{
  "branch": "main",
  "pipeline": "custom:release",
  "variables": { "VERSION": "2.4.0" }
}
\`\`\`

## Get build status
\`\`\`http
GET /v1/builds/{build_id}
\`\`\`

## List builds
\`\`\`http
GET /v1/projects/{project_slug}/builds?status=failed&per_page=20
\`\`\`

## Stream logs
\`\`\`http
GET /v1/builds/{build_id}/steps/{step_id}/logs?stream=true
Accept: text/event-stream
\`\`\`

## Rate limits
| Plan       | Requests / min |
|------------|---------------|
| Hobby      | 60            |
| Pro        | 300           |
| Team       | 1,000         |
| Enterprise | Custom        |`,
        toc: [
            { id: 'base-url', label: 'Base URL', depth: 2 },
            { id: 'authentication', label: 'Authentication', depth: 2 },
            { id: 'trigger-a-build', label: 'Trigger a build', depth: 2 },
            { id: 'get-build-status', label: 'Get build status', depth: 2 },
            { id: 'list-builds', label: 'List builds', depth: 2 },
            { id: 'stream-logs', label: 'Stream logs', depth: 2 },
            { id: 'rate-limits', label: 'Rate limits', depth: 2 },
        ],
    },
];

export const DOCS_CATEGORIES = [...new Set(DOCS.map(d => d.category))];

export function getDocBySlug(slug: string): DocPage | undefined {
    return DOCS.find(d => d.slug === slug);
}

export function getDocsByCategory(category: string): DocPage[] {
    return DOCS.filter(d => d.category === category).sort((a, b) => a.order - b.order);
}

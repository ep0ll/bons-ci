import { a as createComponent, r as renderComponent, b as renderTemplate, m as maybeRenderHead, d as addAttribute } from '../../chunks/astro/server_CCu-t7dI.mjs';
import 'kleur/colors';
import { $ as $$DashboardLayout } from '../../chunks/DashboardLayout_CthgR4Bm.mjs';
export { renderers } from '../../renderers.mjs';

const $$Templates = createComponent(($$result, $$props, $$slots) => {
  const templates = [
    {
      id: "node-ci",
      name: "Node.js CI",
      category: "JavaScript",
      icon: "\u{1F7E2}",
      description: "Install, lint, test with Jest, build, and cache node_modules between runs.",
      stars: 1842,
      official: true,
      tags: ["node", "jest", "npm", "cache"],
      runtime: "Node 18/20/22",
      avgDuration: "1m 20s",
      yaml: `version: '2'
pipelines:
  default:
    - step:
        name: Install & cache
        caches: [node]
        script:
          - npm ci
    - parallel:
        - step:
            name: Test
            script:
              - npm test -- --coverage
        - step:
            name: Lint
            script:
              - npm run lint
    - step:
        name: Build
        script:
          - npm run build
        artifacts:
          - dist/**`
    },
    {
      id: "docker-build-push",
      name: "Docker Build & Push",
      category: "Containers",
      icon: "\u{1F433}",
      description: "Build a Docker image with BuildKit layer caching and push to Forge CI registry or ECR/GCR.",
      stars: 1621,
      official: true,
      tags: ["docker", "buildkit", "ecr", "registry"],
      runtime: "Docker 26",
      avgDuration: "3m 40s",
      yaml: `version: '2'
pipelines:
  default:
    - step:
        name: Build & push
        services: [docker]
        script:
          - export IMAGE=registry.forge-ci.dev/$REPO:$COMMIT
          - docker build
              --cache-from $IMAGE:cache
              --cache-to   $IMAGE:cache,mode=max
              --tag $IMAGE
              .
          - docker push $IMAGE`
    },
    {
      id: "terraform-plan-apply",
      name: "Terraform Plan & Apply",
      category: "Infrastructure",
      icon: "\u{1F3D7}",
      description: "Run terraform plan on PRs, post the diff as a comment, and apply on merge to main.",
      stars: 984,
      official: true,
      tags: ["terraform", "aws", "iac", "gcp"],
      runtime: "Terraform 1.8",
      avgDuration: "2m 55s",
      yaml: `version: '2'
pipelines:
  pull-requests:
    - step:
        name: Terraform plan
        script:
          - terraform init
          - terraform plan -out=plan.tfplan
          - terraform show -json plan.tfplan > plan.json
        artifacts: [plan.tfplan, plan.json]
  branches:
    main:
      - step:
          name: Terraform apply
          trigger: manual
          script:
            - terraform apply -auto-approve`
    },
    {
      id: "go-ci",
      name: "Go CI",
      category: "Go",
      icon: "\u{1F439}",
      description: "Build, vet, test with race detector, and staticcheck for Go projects.",
      stars: 741,
      official: true,
      tags: ["go", "golang", "vet", "staticcheck"],
      runtime: "Go 1.22",
      avgDuration: "1m 05s",
      yaml: `version: '2'
pipelines:
  default:
    - step:
        name: Build & test
        caches: [go]
        script:
          - go build ./...
          - go vet ./...
          - go test -race -coverprofile=coverage.out ./...
        artifacts:
          - coverage.out`
    },
    {
      id: "python-pytest",
      name: "Python / pytest",
      category: "Python",
      icon: "\u{1F40D}",
      description: "Matrix test across Python 3.10/3.11/3.12 with pytest, coverage, and mypy.",
      stars: 692,
      official: true,
      tags: ["python", "pytest", "mypy", "matrix"],
      runtime: "Python 3.10\u20133.12",
      avgDuration: "2m 10s",
      yaml: `version: '2'
pipelines:
  default:
    - parallel:
        matrix:
          - PYTHON_VERSION: ['3.10', '3.11', '3.12']
        steps:
          - step:
              name: Test (Python $PYTHON_VERSION)
              image: python:$PYTHON_VERSION-slim
              caches: [pip]
              script:
                - pip install -e ".[dev]"
                - pytest --cov=src tests/
                - mypy src/`
    },
    {
      id: "react-native",
      name: "React Native",
      category: "Mobile",
      icon: "\u{1F4F1}",
      description: "Build and sign iOS (.ipa) and Android (.apk/.aab) with cache for pods and gradle.",
      stars: 548,
      official: true,
      tags: ["react-native", "ios", "android", "xcode"],
      runtime: "macOS M2 \xB7 Xcode 15",
      avgDuration: "12m 30s",
      yaml: `version: '2'
pipelines:
  default:
    - parallel:
        - step:
            name: iOS build
            runs-on: macos-m2-large
            caches: [cocoapods]
            script:
              - bundle exec fastlane ios build
        - step:
            name: Android build
            caches: [gradle]
            script:
              - ./gradlew assembleRelease`
    },
    {
      id: "rust-ci",
      name: "Rust CI",
      category: "Rust",
      icon: "\u{1F980}",
      description: "Clippy lint, cargo test, and cargo build --release with sccache layer caching.",
      stars: 503,
      official: false,
      tags: ["rust", "cargo", "clippy", "sccache"],
      runtime: "Rust stable/nightly",
      avgDuration: "4m 20s",
      yaml: `version: '2'
pipelines:
  default:
    - step:
        name: Check & test
        caches: [cargo, sccache]
        script:
          - cargo clippy -- -D warnings
          - cargo test
          - cargo build --release`
    },
    {
      id: "monorepo-nx",
      name: "Nx Monorepo",
      category: "Monorepo",
      icon: "\u{1F3DB}",
      description: "Change-aware builds using Nx affected commands. Only builds and tests what changed.",
      stars: 487,
      official: true,
      tags: ["nx", "monorepo", "affected", "cache"],
      runtime: "Node 20 \xB7 Nx 18",
      avgDuration: "0m 45s",
      avgNote: "avg on cache hit",
      yaml: `version: '2'
pipelines:
  default:
    - step:
        name: Nx affected
        caches: [node, nx]
        script:
          - npx nx affected --target=lint --parallel=4
          - npx nx affected --target=test --parallel=4
          - npx nx affected --target=build --parallel=4`
    },
    {
      id: "k8s-deploy",
      name: "Kubernetes Deploy",
      category: "Deployment",
      icon: "\u2638",
      description: "Build, push, and deploy to Kubernetes using kubectl or Helm with rollback support.",
      stars: 412,
      official: true,
      tags: ["k8s", "helm", "kubectl", "deploy"],
      runtime: "kubectl 1.29 \xB7 Helm 3",
      avgDuration: "5m 10s",
      yaml: `version: '2'
pipelines:
  branches:
    main:
      - step:
          name: Deploy to production
          trigger: manual
          script:
            - helm upgrade --install api ./charts/api
                --set image.tag=$COMMIT
                --wait --timeout 5m
            - kubectl rollout status deployment/api`
    }
  ];
  const categories = ["All", ...new Set(templates.map((t) => t.category))];
  return renderTemplate`${renderComponent($$result, "DashboardLayout", $$DashboardLayout, { "title": "Templates", "activeNav": "templates", "breadcrumbs": [{ label: "Templates" }] }, { "default": ($$result2) => renderTemplate` ${maybeRenderHead()}<div class="p-4 sm:p-6 lg:p-8 max-w-[1400px] mx-auto space-y-5"> <!-- Header --> <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4"> <div> <h1 class="font-bold text-2xl text-[#F0F0F0]">Pipeline Templates</h1> <p class="text-[#F0F0F0]-3 text-sm mt-0.5">${templates.length} ready-made pipelines. One click to use.</p> </div> <a href="/docs/pipeline-yaml" class="btn-secondary btn-sm"> <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"></path> </svg>
YAML reference
</a> </div> <!-- Search + category filter --> <div class="flex flex-col sm:flex-row gap-3"> <div class="relative flex-1"> <svg class="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-[#F0F0F0]-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path> </svg> <input type="search" id="tmpl-search" placeholder="Search templates…" class="input !pl-9 !py-2 !text-xs w-full"> </div> <div class="flex flex-wrap gap-1.5"> ${categories.map((cat, i) => renderTemplate`<button${addAttribute(`cat-btn text-xs font-medium px-3 py-1.5 rounded-full border transition-all ${i === 0 ? "border-forge-accent/40 bg-forge-accent-muted text-[#FFEE00]" : "border-[#2C2C2C] text-[#F0F0F0]-3 hover:border-[#2C2C2C]-2 hover:text-[#F0F0F0]"}`, "class")}${addAttribute(cat, "data-cat")} onclick="filterCat(this)"> ${cat} </button>`)} </div> </div> <!-- Template grid --> <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-4" id="tmpl-grid"> ${templates.map((t) => renderTemplate`<div class="card-hover p-0 overflow-hidden template-card cursor-pointer group"${addAttribute(t.category, "data-cat")}${addAttribute(`${t.name} ${t.category} ${t.tags.join(" ")}`.toLowerCase(), "data-text")}${addAttribute(`openTemplate('${t.id}')`, "onclick")}> <!-- Card header --> <div class="px-5 pt-5 pb-4 border-b border-[#2C2C2C]/50"> <div class="flex items-start justify-between gap-3 mb-3"> <div class="flex items-center gap-3"> <span class="text-2xl flex-shrink-0" aria-hidden="true">${t.icon}</span> <div> <h2 class="font-bold text-base text-[#F0F0F0] group-hover:text-[#FFEE00] transition-colors"> ${t.name} </h2> <div class="text-2xs text-[#F0F0F0]-3">${t.category}</div> </div> </div> ${t.official ? renderTemplate`<span class="badge-success border border-[rgba(0,255,136,0.3)] text-2xs flex-shrink-0">Official</span>` : renderTemplate`<span class="badge-neutral border border-[#2C2C2C] text-2xs flex-shrink-0">Community</span>`} </div> <p class="text-xs text-[#F0F0F0]-3 leading-relaxed mb-3">${t.description}</p> <div class="flex flex-wrap gap-1.5"> ${t.tags.map((tag) => renderTemplate`<span class="badge-neutral border border-[#2C2C2C] text-2xs">${tag}</span>`)} </div> </div> <!-- Card footer --> <div class="px-5 py-3.5 flex items-center justify-between"> <div class="flex items-center gap-4 text-2xs text-[#F0F0F0]-3 font-mono"> <span>⚡ ${t.avgDuration}</span> <span>⭐ ${t.stars.toLocaleString()}</span> </div> <div class="flex gap-2 opacity-0 group-hover:opacity-100 transition-opacity"> <button class="text-2xs border border-[#2C2C2C] text-[#F0F0F0]-2 rounded-md px-2.5 py-1 hover:border-[#2C2C2C]-2 hover:text-[#F0F0F0] transition-all" onclick="event.stopPropagation(); openTemplate('{t.id}')">Preview</button> <button class="text-2xs border border-forge-accent/40 bg-forge-accent-muted text-[#FFEE00] rounded-md px-2.5 py-1 hover:bg-forge-accent hover:text-forge-bg transition-all" onclick="event.stopPropagation(); useTemplate('{t.id}')">Use template</button> </div> </div> </div>`)} </div> <div id="tmpl-empty" class="hidden py-14 text-center"> <div class="text-3xl mb-3">🔍</div> <p class="text-[#F0F0F0]-2">No templates match your search.</p> </div> </div>  <div id="tmpl-drawer" class="fixed inset-y-0 right-0 w-full max-w-2xl z-50 hidden transform translate-x-full transition-transform duration-300 ease-spring"> <div class="absolute inset-0 bg-[#111111] border-l border-[#2C2C2C] flex flex-col shadow-modal overflow-hidden"> <!-- Drawer header --> <div class="flex items-center gap-3 px-6 py-5 border-b border-[#2C2C2C] flex-shrink-0"> <div> <h2 id="drawer-title" class="font-bold text-xl text-[#F0F0F0]"></h2> <p id="drawer-meta" class="text-xs text-[#F0F0F0]-3 mt-0.5"></p> </div> <div class="ml-auto flex gap-2"> <button id="use-btn" class="btn-primary btn-md">Use this template</button> <button id="drawer-close" class="btn-ghost btn-md text-[#F0F0F0]-3"> <svg class="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"> <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path> </svg> </button> </div> </div> <!-- Drawer body --> <div class="flex-1 overflow-y-auto p-6 space-y-5"> <p id="drawer-desc" class="text-sm text-[#F0F0F0]-2 leading-relaxed"></p> <div class="grid grid-cols-3 gap-3" id="drawer-stats"></div> <div class="terminal"> <div class="terminal-header"> <div class="terminal-dot bg-[#FF5F57]"></div> <div class="terminal-dot bg-[#FFBD2E]"></div> <div class="terminal-dot bg-[#28C840]"></div> <span class="text-xs text-[#F0F0F0]-3 ml-2 font-mono">.forge/pipeline.yml</span> <button id="copy-yaml" class="ml-auto text-xs text-[#FFEE00] hover:text-[#FFEE00]-dim transition-colors">Copy</button> </div> <pre id="drawer-yaml" class="p-5 text-xs font-mono text-[#F0F0F0]-2 overflow-x-auto leading-relaxed"></pre> </div> </div> </div> </div>  <div id="drawer-backdrop" class="fixed inset-0 bg-black/60 z-40 hidden backdrop-blur-sm"></div> ` })} `;
}, "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/templates.astro", void 0);

const $$file = "/Users/sai/vscode/bons-ci/apps/website/src/pages/dashboard/templates.astro";
const $$url = "/dashboard/templates";

const _page = /*#__PURE__*/Object.freeze(/*#__PURE__*/Object.defineProperty({
  __proto__: null,
  default: $$Templates,
  file: $$file,
  url: $$url
}, Symbol.toStringTag, { value: 'Module' }));

const page = () => _page;

export { page };

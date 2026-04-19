import"./hoisted.UR2HsBvZ.js";import"./BaseLayout.astro_astro_type_script_index_0_lang.BKQ29WDz.js";const d={"node-ci":{name:"Node.js CI",category:"JavaScript",description:"Install, lint, test with Jest, build, and cache node_modules between runs.",runtime:"Node 18/20/22",avgDuration:"1m 20s",stars:1842,tags:["node","jest","npm"],yaml:`version: '2'
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
          - dist/**`},"docker-build-push":{name:"Docker Build & Push",category:"Containers",description:"Build a Docker image with BuildKit layer caching and push to Forge CI registry or ECR/GCR.",runtime:"Docker 26",avgDuration:"3m 40s",stars:1621,tags:["docker","buildkit"],yaml:`version: '2'
pipelines:
  default:
    - step:
        name: Build & push
        services: [docker]
        script:
          - export IMAGE=registry.forge-ci.dev/$REPO:$COMMIT
          - docker build --cache-from $IMAGE:cache --tag $IMAGE .
          - docker push $IMAGE`},"terraform-plan-apply":{name:"Terraform",category:"Infrastructure",description:"Run terraform plan on PRs, post diff as comment, and apply on merge to main.",runtime:"Terraform 1.8",avgDuration:"2m 55s",stars:984,tags:["terraform","aws"],yaml:`version: '2'
pipelines:
  pull-requests:
    - step:
        name: Terraform plan
        script:
          - terraform init
          - terraform plan -out=plan.tfplan
        artifacts: [plan.tfplan]`},"go-ci":{name:"Go CI",category:"Go",description:"Build, vet, test with race detector, and staticcheck for Go projects.",runtime:"Go 1.22",avgDuration:"1m 05s",stars:741,tags:["go","cargo","clippy"],yaml:`version: '2'
pipelines:
  default:
    - step:
        name: Build & test
        caches: [go]
        script:
          - go build ./...
          - go vet ./...
          - go test -race ./...`},"python-pytest":{name:"Python / pytest",category:"Python",description:"Matrix test across Python 3.10/3.11/3.12 with pytest, coverage, and mypy.",runtime:"Python 3.10–3.12",avgDuration:"2m 10s",stars:692,tags:["python","pytest","mypy"],yaml:`version: '2'
pipelines:
  default:
    - parallel:
        matrix:
          - PYTHON_VERSION: ['3.10', '3.11', '3.12']
        steps:
          - step:
              name: Test
              script:
                - pytest --cov=src tests/`},"react-native":{name:"React Native",category:"Mobile",description:"Build and sign iOS and Android with cache for pods and gradle.",runtime:"macOS M2 · Xcode 15",avgDuration:"12m 30s",stars:548,tags:["rn","ios","android"],yaml:`version: '2'
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
              - ./gradlew assembleRelease`},"rust-ci":{name:"Rust CI",category:"Rust",description:"Clippy lint, cargo test, and cargo build --release with sccache layer caching.",runtime:"Rust stable/nightly",avgDuration:"4m 20s",stars:503,tags:["rust","cargo","clippy"],yaml:`version: '2'
pipelines:
  default:
    - step:
        name: Check & test
        caches: [cargo, sccache]
        script:
          - cargo clippy -- -D warnings
          - cargo test
          - cargo build --release`},"monorepo-nx":{name:"Nx Monorepo",category:"Monorepo",description:"Change-aware builds using Nx affected commands.",runtime:"Node 20 · Nx 18",avgDuration:"0m 45s",stars:487,tags:["nx","monorepo"],yaml:`version: '2'
pipelines:
  default:
    - step:
        name: Nx affected
        caches: [node, nx]
        script:
          - npx nx affected --target=lint --parallel=4
          - npx nx affected --target=test --parallel=4
          - npx nx affected --target=build --parallel=4`},"k8s-deploy":{name:"Kubernetes Deploy",category:"Deployment",description:"Build, push, and deploy to Kubernetes with Helm and rollback support.",runtime:"kubectl 1.29 · Helm 3",avgDuration:"5m 10s",stars:412,tags:["k8s","helm"],yaml:`version: '2'
pipelines:
  branches:
    main:
      - step:
          name: Deploy to production
          trigger: manual
          script:
            - helm upgrade --install api ./charts/api --set image.tag=$COMMIT --wait`}},s=document.getElementById("tmpl-drawer"),o=document.getElementById("drawer-backdrop");function l(t){const e=d[t];if(!e)return;document.getElementById("drawer-title").textContent=e.name,document.getElementById("drawer-meta").textContent=`${e.category} · ⭐ ${e.stars.toLocaleString()}`,document.getElementById("drawer-desc").textContent=e.description,document.getElementById("drawer-yaml").textContent=e.yaml;const n=document.getElementById("drawer-stats");n.innerHTML=[{label:"Runtime",value:e.runtime},{label:"Avg duration",value:e.avgDuration},{label:"Stars",value:e.stars.toLocaleString()}].map(({label:a,value:r})=>`
      <div class="p-3 rounded-none border border-[#2C2C2C] bg-[#111111]2/40">
        <div class="text-2xs text-[#F0F0F0]-3 mb-1">${a}</div>
        <div class="text-sm font-mono font-bold text-[#F0F0F0]">${r}</div>
      </div>
    `).join(""),s.classList.remove("hidden"),o.classList.remove("hidden"),requestAnimationFrame(()=>s.classList.remove("translate-x-full"))}function i(){s.classList.add("translate-x-full"),setTimeout(()=>{s.classList.add("hidden"),o.classList.add("hidden")},300)}document.getElementById("drawer-close")?.addEventListener("click",i);o.addEventListener("click",i);document.addEventListener("keydown",t=>{t.key==="Escape"&&i()});document.getElementById("copy-yaml")?.addEventListener("click",()=>{const t=document.getElementById("drawer-yaml").textContent??"";navigator.clipboard.writeText(t);const e=document.getElementById("copy-yaml");e.textContent="Copied!",setTimeout(()=>e.textContent="Copy",1500)});const m=document.querySelectorAll(".template-card"),p=document.getElementById("tmpl-empty");function c(){const t=document.getElementById("tmpl-search").value.toLowerCase(),e=document.querySelector(".cat-btn.border-forge-accent\\/40")?.dataset.cat??"All";let n=0;m.forEach(a=>{const r=(!t||a.dataset.text.includes(t))&&(e==="All"||a.dataset.cat===e);a.style.display=r?"":"none",r&&n++}),p.classList.toggle("hidden",n>0)}document.getElementById("tmpl-search")?.addEventListener("input",c);function u(t){document.querySelectorAll(".cat-btn").forEach(e=>{e.className="cat-btn text-xs font-medium px-3 py-1.5 rounded-full border transition-all border-[#2C2C2C] text-[#F0F0F0]-3 hover:border-[#2C2C2C]-2 hover:text-[#F0F0F0]"}),t.className="cat-btn text-xs font-medium px-3 py-1.5 rounded-full border transition-all border-forge-accent/40 bg-forge-accent-muted text-[#FFEE00]",c()}window.openTemplate=l;window.filterCat=u;window.useTemplate=t=>l(t);

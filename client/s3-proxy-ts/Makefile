.PHONY: help install dev deploy deploy-staging typecheck test test-watch test-coverage lint fmt keys provision clean kv-tenants kv-uploads

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n",$$1,$$2}'

install: ## Install all dependencies
	npm ci

dev: ## Start local dev worker (hot-reload)
	npx wrangler dev --env dev

deploy: ## Deploy to production
	npx wrangler deploy

deploy-staging: ## Deploy to staging
	npx wrangler deploy --env staging

typecheck: ## TypeScript type-check (no emit)
	npx tsc --noEmit

test: ## Run all unit tests
	npm test

test-watch: ## Run tests in watch mode
	npm run test:watch

test-coverage: ## Run tests with V8 coverage report
	npm run test:coverage

lint: ## ESLint
	npm run lint

fmt: ## Prettier format all source files
	npm run fmt

keys: ## Generate a new 32-byte AES-256 master key (64 hex chars)
	@echo "New MASTER_ENC_KEY (64 hex chars):"
	@openssl rand -hex 32

set-key: ## Interactively set MASTER_ENC_KEY Workers Secret
	npx wrangler secret put MASTER_ENC_KEY

provision: ## Provision a tenant (set env vars first)
	@MASTER_ENC_KEY=$(MASTER_ENC_KEY) npx tsx scripts/provision.ts \
		--tenant-id    "$(TENANT_ID)" \
		--name         "$(TENANT_NAME)" \
		--access-key   "$(ACCESS_KEY)" \
		--secret-key   "$(SECRET_KEY)" \
		--backend      "$(BACKEND)" \
		--endpoint     "$(ENDPOINT)" \
		--region       "$(REGION)" \
		--bucket       "$(BUCKET)" \
		--b-access-key "$(B_ACCESS_KEY)" \
		--b-secret-key "$(B_SECRET_KEY)"

kv-tenants: ## List all tenant credential keys in TENANT_KV
	npx wrangler kv:key list --binding TENANT_KV

kv-uploads: ## List in-progress multipart uploads in UPLOAD_KV
	npx wrangler kv:key list --binding UPLOAD_KV --prefix "upload:meta:"

clean: ## Remove build artifacts and node_modules
	rm -rf dist node_modules .wrangler

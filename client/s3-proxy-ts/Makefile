.PHONY: help install dev deploy deploy-staging test test-coverage typecheck lint fmt keys provision-example clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS=":.*?## "}; {printf "\033[36m%-24s\033[0m %s\n", $$1, $$2}'

install: ## Install dependencies
	npm ci

dev: ## Start local dev worker
	npx wrangler dev

deploy: ## Deploy to production
	npm run deploy

deploy-staging: ## Deploy to staging
	npx wrangler deploy --env staging

test: ## Run unit tests
	npm test

test-coverage: ## Run tests with coverage
	npm run test:coverage

typecheck: ## TypeScript typecheck
	npm run typecheck

lint: ## ESLint
	npm run lint

fmt: ## Prettier format
	npm run fmt

keys: ## Generate a new AES-256 master key (64 hex chars)
	@echo "New MASTER_ENC_KEY:"
	@openssl rand -hex 32

set-key: ## Set MASTER_ENC_KEY as a Workers Secret interactively
	npx wrangler secret put MASTER_ENC_KEY

provision-example: ## Provision a demo B2 tenant
	MASTER_ENC_KEY=$(shell wrangler secret list 2>/dev/null | grep MASTER_ENC_KEY) \
	npx tsx scripts/provision_tenant.ts \
		--tenant-id example \
		--name "Example Tenant" \
		--access-key "EXAMPLE00000000000000000" \
		--secret-key "$(shell openssl rand -base64 30)" \
		--backend b2 \
		--endpoint s3.us-west-004.backblazeb2.com \
		--region us-west-004 \
		--bucket company-master-bucket \
		--b-access-key "$(B2_KEY_ID)" \
		--b-secret-key "$(B2_APP_KEY)"

kv-list-tenants: ## List all tenant credential keys
	npx wrangler kv:key list --binding TENANT_KV

kv-list-uploads: ## List in-progress multipart uploads
	npx wrangler kv:key list --binding UPLOAD_KV --prefix "upload:meta:"

clean: ## Remove build artifacts
	rm -rf dist node_modules

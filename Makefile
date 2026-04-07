.PHONY: dev dev-db dev-server dev-web stop build test test-integration test-e2e check lint clean install-tools help

# Load .env if it exists (GITHUB_TOKEN, etc.)
ifneq (,$(wildcard .env))
include .env
export
endif

# — Development ——————————————————————————————————————————————

dev: dev-db ## Start full dev stack (postgres + Go server with hot reload + Vite HMR)
	@echo "Waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; do sleep 0.5; done
	@echo "Running migrations..."
	@docker compose up migrate --wait 2>/dev/null || true
	@trap 'kill 0' INT TERM; \
		DATABASE_URL="postgres://postgres:postgres@localhost:5433/cordon?sslmode=disable" \
		AUTH_MODE=static \
		CORDON_INSECURE=true \
		DEFAULT_TENANT_ID=00000000-0000-0000-0000-000000000001 \
		go run github.com/air-verse/air@latest & \
		cd web && npm install --silent && npm run dev & \
		wait

dev-db: ## Start only postgres + run migrations
	@docker compose up -d --force-recreate postgres
	@echo "Waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; do sleep 0.5; done
	@docker compose up migrate --wait 2>/dev/null || true
	@echo "Postgres ready on :5433"

dev-server: ## Start Go server with hot reload (assumes postgres running)
	DATABASE_URL="postgres://postgres:postgres@localhost:5433/cordon?sslmode=disable" \
	AUTH_MODE=static \
	CORDON_INSECURE=true \
	DEFAULT_TENANT_ID=00000000-0000-0000-0000-000000000001 \
	go run github.com/air-verse/air@latest

dev-web: ## Start Vite dev server with HMR (proxies to :8443)
	cd web && npm install --silent && npm run dev

stop: ## Stop all services
	@docker compose down
	@echo "Stopped"

# — Build ————————————————————————————————————————————————————

build: ## Build Go binaries
	go build -o ./tmp/cordon-server ./cmd/server
	go build -o ./tmp/cordon ./cmd/cordon

build-web: ## Build frontend for production
	cd web && npm install --silent && npm run build

build-docker: ## Build Docker image
	docker compose build server

# — Test —————————————————————————————————————————————————————

test: ## Run unit + arch tests (Go + frontend types)
	go test ./... -short
	cd web && npx svelte-check

test-go: ## Run Go unit + arch tests only
	go test ./... -short

test-integration: ## Run integration tests (starts testcontainers)
	go test ./internal/infrastructure/postgres/... -v

test-e2e: ## Run E2E tests (starts full docker compose stack)
	@docker compose up -d --build --wait
	CORDON_SERVER=http://localhost:8443 go test ./e2e/... -tags=e2e -v
	@docker compose down

check: ## Type-check frontend
	cd web && npx svelte-check

lint-web: ## Check frontend for banned patterns (alert, silent catches)
	@echo "Checking for alert() calls..."
	@! grep -rn 'alert(' web/src/routes/ web/src/lib/ --include='*.svelte' --include='*.ts' | grep -v node_modules | grep -v '// allowed' || (echo "ERROR: Use MessageBanner instead of alert()" && exit 1)
	@echo "No alert() calls found."

# — Utilities ————————————————————————————————————————————————

clean: ## Remove build artifacts
	rm -rf tmp/ web/build/ web/.svelte-kit/

install-tools: ## Install development dependencies
	go install github.com/air-verse/air@latest
	cd web && npm install

migrate: ## Run database migrations (assumes postgres running)
	@docker compose up migrate --wait 2>/dev/null || true

# — Help —————————————————————————————————————————————————————

help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

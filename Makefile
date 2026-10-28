# KongPay Webhook Listener — auto-generated Makefile
SHELL       := /bin/bash
COMPOSE     ?= docker compose
PG_SERVICE ?= postgres
PG_USER     ?= postgres
PG_DB       ?= kongpay_db
BASE_URL    ?= https://localhost:8443
HMAC_KEY    ?= KONGPAY_SUPER_SECRET_KEY_2026
TEST_DSN    ?= postgres://postgres:password@localhost:5432/kongpay_db?sslmode=disable

.DEFAULT_GOAL := help
.PHONY: help up down restart rebuild reset ps logs build vet fmt tidy test test-short test-db test-live test-webhook metrics history db psql grafana prom-targets clean

help: ## Show all targets
	@grep -E "^[a-zA-Z0-9_-]+:.*?## " $(MAKEFILE_LIST) | awk "BEGIN{FS=\":.*?## \"}{printf \"  %-16s %s\\n\", \$$1, \$$2}"

up: ## Start the stack
	$(COMPOSE) up -d

down: ## Stop the stack
	$(COMPOSE) down

restart: ## Restart all
	$(COMPOSE) restart

rebuild: ## Rebuild backend
	$(COMPOSE) build kongpay-backend
	$(COMPOSE) up -d --force-recreate kongpay-backend

reset: ## Nuke + up
	$(COMPOSE) down -v --remove-orphans
	$(COMPOSE) up --build -d

ps: ## Container status
	$(COMPOSE) ps

logs: ## Tail backend logs
	$(COMPOSE) logs -f --tail 50 kongpay_backend

vet: ## go vet
	go vet .

fmt: ## gofmt
	gofmt -w .

tidy: ## go mod tidy
	go mod tidy

build: ## Build binary
	go build -o /tmp/kongpay-app .

test-short: ## Unit tests
	go test . -short -v

test-db: ## DB tests
	TEST_DATABASE_URL="$(TEST_DSN)" go test . -v

test-live: ## Live mTLS test
	go test . -run TestWebhookMTLS_Live -v

test: ## Unit + DB
	make test-short
	make test-db

test-all: ## All tests
	make test
	make test-live

test-webhook: ## Send signed webhook
	./scripts/test-webhook.sh

metrics: ## KongPay metrics
	@curl -s http://localhost:9090/metrics | grep -E "^kongpay_"

history: ## GET transaction history
	@curl -s "$(BASE_URL)/api/v1/transactions" --cacert ca.crt --cert client.crt --key client.key | jq .

db: ## Dump DB
	@$(COMPOSE) exec -T $(PG_SERVICE) psql -U $(PG_USER) -d $(PG_DB) -c "SELECT id,event_id,status,created_at FROM bank_transactions ORDER BY id DESC;"

psql: ## Open psql
	$(COMPOSE) exec $(PG_SERVICE) psql -U $(PG_USER) -d $(PG_DB)

prom-targets: ## Prometheus targets
	@curl -s http://localhost:9091/api/v1/targets | jq -r ".data.activeTargets[] | \"\(.labels.job)\t\(.health)\t\(.scrapeUrl)\""

grafana: ## Open Grafana
	@xdg-open http://localhost:3000 2>/dev/null || open http://localhost:3000 2>/dev/null || echo "Open http://localhost:3000 (admin/admin)"

clean: ## Clean artifacts
	rm -f san.cnf server.csr kongpay-app


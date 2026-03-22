SHELL := /bin/bash

BUILD_DIR := ./build

.PHONY: help dev dev-clean build fmt lint test query \
        run-source-freshrss run-source-miniflux run-source-linkwarden \
        run-extractor run-analyzer \
        run-search-openlibrary run-search-arxiv run-search-semantic-scholar \
        run-koha-check run-notifier run-store run-state \
        trigger digest

# ── Help ──────────────────────────────────────────────────────────────────────

help: ## Show this help message
	@echo "Minerva - AI Curator of Knowledge"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-28s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Infrastructure ────────────────────────────────────────────────────────────

dev: ## Start Mosquitto and PostgreSQL for local development
	docker compose up mosquitto postgres -d
	@echo "Mosquitto: localhost:1883 | PostgreSQL: localhost:5432"

dev-clean: ## Stop and wipe Mosquitto and PostgreSQL (destroys all data)
	docker compose down mosquitto postgres -v
	@echo "Infrastructure stopped and volumes removed"

# ── Build ─────────────────────────────────────────────────────────────────────

build: ## Build all primitive binaries
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/source-freshrss         ./cmd/source-freshrss/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/source-miniflux         ./cmd/source-miniflux/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/source-linkwarden       ./cmd/source-linkwarden/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/extractor               ./cmd/extractor/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/analyzer                ./cmd/analyzer/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/search-openlibrary      ./cmd/search-openlibrary/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/search-arxiv            ./cmd/search-arxiv/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/search-semantic-scholar ./cmd/search-semantic-scholar/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/koha-check              ./cmd/koha-check/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/notifier                ./cmd/notifier/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/store                   ./cmd/store/
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/state                   ./cmd/state/
	@echo "Done. Binaries in $(BUILD_DIR)/"

# ── Code quality ──────────────────────────────────────────────────────────────

fmt: ## Format Go code
	go fmt ./...

lint: ## Lint Go code
	golangci-lint run

test: ## Run tests
	go test -v ./...

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR)

# ── Primitives ────────────────────────────────────────────────────────────────
# Each target builds only the binary it needs, then runs it with .env.dev

run-source-freshrss: ## Run FreshRSS source primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/source-freshrss ./cmd/source-freshrss/ && \
	$(BUILD_DIR)/source-freshrss -config .env.dev

run-source-miniflux: ## Run Miniflux source primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/source-miniflux ./cmd/source-miniflux/ && \
	$(BUILD_DIR)/source-miniflux -config .env.dev

run-source-linkwarden: ## Run Linkwarden source primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/source-linkwarden ./cmd/source-linkwarden/ && \
	$(BUILD_DIR)/source-linkwarden -config .env.dev

run-extractor: ## Run extractor primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/extractor ./cmd/extractor/ && \
	$(BUILD_DIR)/extractor -config .env.dev

run-analyzer: ## Run analyzer primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/analyzer ./cmd/analyzer/ && \
	$(BUILD_DIR)/analyzer -config .env.dev

run-search-openlibrary: ## Run OpenLibrary search primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/search-openlibrary ./cmd/search-openlibrary/ && \
	$(BUILD_DIR)/search-openlibrary -config .env.dev

run-search-arxiv: ## Run arXiv search primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/search-arxiv ./cmd/search-arxiv/ && \
	$(BUILD_DIR)/search-arxiv -config .env.dev

run-search-semantic-scholar: ## Run Semantic Scholar search primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/search-semantic-scholar ./cmd/search-semantic-scholar/ && \
	$(BUILD_DIR)/search-semantic-scholar -config .env.dev

run-koha-check: ## Run Koha ownership check primitive
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/koha-check ./cmd/koha-check/ && \
	$(BUILD_DIR)/koha-check -config .env.dev

run-notifier: ## Run notifier primitive (stub — awaiting consolidator)
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/notifier ./cmd/notifier/ && \
	$(BUILD_DIR)/notifier -config .env.dev

run-store: ## Run store primitive (Postgres knowledge base observer)
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/store ./cmd/store/ && \
	$(BUILD_DIR)/store -config .env.dev

run-state: ## Run state primitive (pipeline crash recovery)
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/state ./cmd/state/ && \
	$(BUILD_DIR)/state -config .env.dev

# ── Pipeline ──────────────────────────────────────────────────────────────────

trigger: ## Fire pipeline trigger (also replays any partially processed articles via state primitive)
	mosquitto_pub -h localhost -p 1883 -t "minerva/pipeline/trigger" -m "{}"

digest: ## Send digest trigger to notifier
	mosquitto_pub -h localhost -p 1883 -t "minerva/pipeline/digest" -m "{}"

# ── Database ──────────────────────────────────────────────────────────────────

query: ## Open psql session to the Minerva knowledge base
	docker exec -it minerva_postgres psql -U minerva -d minerva

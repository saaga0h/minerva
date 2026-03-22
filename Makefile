SHELL := /bin/bash

# Build configuration
BINARY_NAME := minerva
DOCKER_IMAGE := minerva
DOCKER_TAG := latest

# Directories
BUILD_DIR := ./build

# Go configuration
GOOS := linux
GOARCH := amd64
CGO_ENABLED := 1

# MQTT broker URL for trigger helper
MQTT_BROKER ?= tcp://localhost:1883

.PHONY: help build build-primitives test clean docker run dev deps fmt lint \
        run-source-freshrss run-source-miniflux run-source-linkwarden run-extractor run-analyzer \
        run-search-openlibrary run-search-arxiv run-search-semantic-scholar \
        run-storage run-koha-check run-notifier run-store trigger digest mosquitto pg

# Default target
help: ## Show this help message
	@echo "Minerva - AI Curator of Knowledge"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Development dependencies
deps: ## Install Go dependencies
	go mod download
	go mod verify

# Build all primitives for production (Linux/amd64)
build: build-primitives ## Build all primitive binaries for production (Linux/amd64)

# Build for development (with debug info)
build-dev: ## Build all primitives for development with debug symbols
	@echo "Building primitives for development..."
	@mkdir -p $(BUILD_DIR)
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/source-freshrss    ./cmd/source-freshrss/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/source-miniflux    ./cmd/source-miniflux/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/source-linkwarden  ./cmd/source-linkwarden/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/extractor          ./cmd/extractor/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/analyzer              ./cmd/analyzer/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/search-openlibrary    ./cmd/search-openlibrary/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/search-arxiv           ./cmd/search-arxiv/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/search-semantic-scholar ./cmd/search-semantic-scholar/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/storage               ./cmd/storage/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/koha-check             ./cmd/koha-check/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/notifier               ./cmd/notifier/
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/store                  ./cmd/store/

# Run tests
test: ## Run tests
	go test -v ./...

# Run tests with coverage
test-coverage: ## Run tests with coverage report
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Format code
fmt: ## Format Go code
	go fmt ./...

# Lint code
lint: ## Lint Go code
	golangci-lint run

# Clean build artifacts
clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Docker targets
docker: ## Build Docker image
	@echo "Building Docker image $(DOCKER_IMAGE):$(DOCKER_TAG)..."
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-dev: ## Build and run with development services
	docker-compose --profile dev up --build

# Run the application — start all primitives (requires Mosquitto running)
run: build ## Build and show instructions for running primitives
	@echo "All primitives built. Start each in a separate terminal:"
	@echo "  $(BUILD_DIR)/source-freshrss    -config .env"
	@echo "  $(BUILD_DIR)/source-miniflux    -config .env"
	@echo "  $(BUILD_DIR)/source-linkwarden  -config .env"
	@echo "  $(BUILD_DIR)/extractor          -config .env"
	@echo "  $(BUILD_DIR)/analyzer         -config .env"
	@echo "  $(BUILD_DIR)/search-openlibrary    -config .env"
	@echo "  $(BUILD_DIR)/search-arxiv           -config .env"
	@echo "  $(BUILD_DIR)/search-semantic-scholar -config .env"
	@echo "  $(BUILD_DIR)/storage           -config .env"
	@echo "  $(BUILD_DIR)/koha-check        -config .env"
	@echo "  $(BUILD_DIR)/notifier          -config .env"
	@echo "Then trigger the pipeline: make trigger"

# Run in development mode
dev: mosquitto pg build-dev ## Start infrastructure and build all primitives for development
	@echo "Mosquitto and PostgreSQL started. Primitives built. Run each with: -config .env.dev"

# Run with dry-run flag (kept for compatibility, now just shows help)
dry-run: build-dev ## Build primitives (dry-run mode removed — use trigger for testing)
	@echo "Dry-run mode removed. Start primitives individually and use 'make trigger'."

# Nomad deployment
deploy-nomad: docker ## Deploy to Nomad
	@echo "Deploying to Nomad..."
	nomad job run deploy/nomad/minerva.nomad

# Database operations
db-init: ## Initialize the database directory
	@echo "Initializing database..."
	mkdir -p ./data

# Development setup
setup-dev: deps ## Setup development environment
	@echo "Setting up development environment..."
	cp .env.example .env.dev
	@echo "Please edit .env.dev with your configuration"

# Git hooks
install-hooks: ## Install git hooks
	cp scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit

# Release targets
version: ## Show current version
	@echo $(shell git describe --tags --always --dirty)

# Quick development workflow
quick-test: fmt test ## Quick development test (format + test)

# Full CI pipeline simulation
ci: deps fmt lint test build ## Simulate CI pipeline

# Monitor logs in development
logs: ## Show application logs (requires running container)
	docker-compose logs -f minerva

reset-db:
	@echo "Resetting database..."
	rm -f ./data/minerva.db
	@echo "Database reset. Will be recreated on next run."

.PHONY: query
query:
	@sqlite3 ./data/minerva.db

# ── Primitive builds ──────────────────────────────────────────────────────────

build-primitives: ## Build all primitive binaries (native, for local dev)
	@echo "Building primitives..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/source-freshrss    ./cmd/source-freshrss/
	go build -o $(BUILD_DIR)/source-miniflux    ./cmd/source-miniflux/
	go build -o $(BUILD_DIR)/source-linkwarden  ./cmd/source-linkwarden/
	go build -o $(BUILD_DIR)/extractor          ./cmd/extractor/
	go build -o $(BUILD_DIR)/analyzer           ./cmd/analyzer/
	go build -o $(BUILD_DIR)/search-openlibrary     ./cmd/search-openlibrary/
	go build -o $(BUILD_DIR)/search-arxiv            ./cmd/search-arxiv/
	go build -o $(BUILD_DIR)/search-semantic-scholar ./cmd/search-semantic-scholar/
	go build -o $(BUILD_DIR)/storage             ./cmd/storage/
	go build -o $(BUILD_DIR)/koha-check          ./cmd/koha-check/
	go build -o $(BUILD_DIR)/notifier            ./cmd/notifier/
	go build -o $(BUILD_DIR)/store               ./cmd/store/
	@echo "Done. Binaries in $(BUILD_DIR)/"

# ── Primitive run targets ─────────────────────────────────────────────────────

run-source-freshrss: build-primitives ## Run FreshRSS source primitive
	$(BUILD_DIR)/source-freshrss -config .env.dev

run-source-miniflux: build-primitives ## Run Miniflux source primitive
	$(BUILD_DIR)/source-miniflux -config .env.dev

run-source-linkwarden: build-primitives ## Run Linkwarden source primitive
	$(BUILD_DIR)/source-linkwarden -config .env.dev

run-extractor: build-primitives ## Run extractor primitive
	$(BUILD_DIR)/extractor -config .env.dev

run-analyzer: build-primitives ## Run analyzer primitive
	$(BUILD_DIR)/analyzer -config .env.dev

run-search-openlibrary: build-primitives ## Run search-openlibrary primitive
	$(BUILD_DIR)/search-openlibrary -config .env.dev

run-search-arxiv: build-primitives ## Run search-arxiv primitive
	$(BUILD_DIR)/search-arxiv -config .env.dev

run-search-semantic-scholar: build-primitives ## Run search-semantic-scholar primitive
	$(BUILD_DIR)/search-semantic-scholar -config .env.dev

run-storage: build-primitives ## Run storage primitive
	$(BUILD_DIR)/storage -config .env.dev

run-koha-check: build-primitives ## Run koha-check primitive
	$(BUILD_DIR)/koha-check -config .env.dev

run-notifier: build-primitives ## Run notifier primitive
	$(BUILD_DIR)/notifier -config .env.dev

run-store: build-primitives ## Run store primitive (knowledge base observer, requires PostgreSQL)
	$(BUILD_DIR)/store -config .env.dev

# ── Pipeline trigger ──────────────────────────────────────────────────────────

trigger: ## Publish a pipeline trigger to MQTT (requires mosquitto_pub in PATH)
	@echo "Triggering pipeline via MQTT..."
	mosquitto_pub -h localhost -p 1883 -t "minerva/pipeline/trigger" -m "{}"
	@echo "Trigger sent to minerva/pipeline/trigger"

digest: ## Send a digest notification trigger to MQTT (requires mosquitto_pub in PATH)
	@echo "Sending digest trigger via MQTT..."
	mosquitto_pub -h localhost -p 1883 -t "minerva/pipeline/digest" -m "{}"
	@echo "Digest trigger sent to minerva/pipeline/digest"

# ── Local Mosquitto ───────────────────────────────────────────────────────────

mosquitto: ## Start Mosquitto broker via docker compose
	docker compose up mosquitto -d
	@echo "Mosquitto started on localhost:1883"

# ── Local PostgreSQL ──────────────────────────────────────────────────────────

pg: ## Start PostgreSQL via docker compose
	docker compose up postgres -d
	@echo "PostgreSQL started on localhost:5432 (user: minerva, db: minerva)"

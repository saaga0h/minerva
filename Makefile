SHELL := /bin/bash

# Build configuration
BINARY_NAME := minerva
DOCKER_IMAGE := minerva
DOCKER_TAG := latest

# Directories
CMD_DIR := ./cmd/minerva
BUILD_DIR := ./build

# Go configuration
GOOS := linux
GOARCH := amd64
CGO_ENABLED := 1

.PHONY: help build test clean docker run dev deps fmt lint

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

# Build the application
build: ## Build the application binary
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "-X main.version=$(shell git describe --tags --always --dirty)" \
		-o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

# Build for development (with debug info)
build-dev: ## Build for development with debug symbols
	@echo "Building $(BINARY_NAME) for development..."
	@mkdir -p $(BUILD_DIR)
	go build -gcflags="all=-N -l" -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

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

# Run the application
run: build ## Build and run the application
	@echo "Running $(BINARY_NAME)..."
	$(BUILD_DIR)/$(BINARY_NAME)

# Run in development mode
dev: build-dev ## Build and run in development mode
	@echo "Running $(BINARY_NAME) in development mode..."
	$(BUILD_DIR)/$(BINARY_NAME) -config .env.dev

# Run with dry-run flag
dry-run: build-dev ## Build and run in dry-run mode
	@echo "Running $(BINARY_NAME) in dry-run mode..."
	$(BUILD_DIR)/$(BINARY_NAME) -config .env.dev -dry-run

# Nomad deployment
deploy-nomad: docker ## Deploy to Nomad
	@echo "Deploying to Nomad..."
	nomad job run deploy/nomad/minerva.nomad

# Database operations
db-init: ## Initialize the database
	@echo "Initializing database..."
	mkdir -p ./data
	$(BUILD_DIR)/$(BINARY_NAME) -config .env.dev -dry-run

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
	
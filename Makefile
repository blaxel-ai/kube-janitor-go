.PHONY: all build test lint clean docker-build docker-push help

# Variables
BINARY_NAME := kube-janitor-go
DOCKER_IMAGE := ghcr.io/blaxel-ai/kube-janitor-go
VERSION := $(shell git describe --tags --always --dirty)
GOFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(shell git rev-parse HEAD) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)"

# Default target
all: build

## build: Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	go build $(GOFLAGS) -o bin/$(BINARY_NAME) cmd/$(BINARY_NAME)/main.go

## test: Run tests
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## lint: Run linters
lint:
	@echo "Running linters..."
	golangci-lint run --timeout=5m ./...

## fmt: Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...
	gofumpt -w .

## tidy: Tidy go modules
tidy:
	@echo "Tidying modules..."
	go mod tidy

## clean: Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf bin/ coverage.out coverage.html

## docker-build: Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(VERSION) -t $(DOCKER_IMAGE):latest .

## docker-push: Push Docker image
docker-push: docker-build
	@echo "Pushing Docker image..."
	docker push $(DOCKER_IMAGE):$(VERSION)
	docker push $(DOCKER_IMAGE):latest

## run: Run locally with example config
run: build
	./bin/$(BINARY_NAME) --dry-run --log-level=debug

## install-tools: Install development tools
install-tools:
	@echo "Installing development tools..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install mvdan.cc/gofumpt@latest

## helm-lint: Lint Helm chart
helm-lint:
	@echo "Linting Helm chart..."
	helm lint helm/kube-janitor-go

## helm-test: Test Helm chart
helm-test:
	@echo "Testing Helm chart..."
	./helm/test-helm-chart.sh

## helm-package: Package Helm chart
helm-package:
	@echo "Packaging Helm chart..."
	helm package helm/kube-janitor-go -d helm/dist/
	helm repo index helm/dist/ --url https://blaxel-ai.github.io/kube-janitor-go

## helm-template: Template Helm chart with default values
helm-template:
	@echo "Templating Helm chart..."
	helm template kube-janitor-go helm/kube-janitor-go

## helm-install-dry-run: Dry run Helm installation
helm-install-dry-run:
	@echo "Dry run Helm installation..."
	helm install kube-janitor-go helm/kube-janitor-go --dry-run --debug

## helm-docs: Generate Helm values documentation
helm-docs:
	@echo "Generating Helm values documentation..."
	@cd helm/kube-janitor-go && make docs

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST) 
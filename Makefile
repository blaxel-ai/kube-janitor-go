.PHONY: all build test lint clean docker-build docker-push help

# Variables
BINARY_NAME := kube-janitor-go
DOCKER_IMAGE := ghcr.io/blaxel-ai/kube-janitor-go
VERSION := $(shell git describe --tags --always --dirty)
NAMESPACE := default
COUNT := 10
GOFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(shell git rev-parse HEAD) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)"
## logs: Get logs from kube-janitor pods (use NAMESPACE=kube-janitor, TAIL=300)
JANITOR_NAMESPACE := kube-janitor
TAIL := 300

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

## docker-buildx: Build multi-arch Docker image with buildx
docker-buildx:
	@echo "Building multi-arch Docker image..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):latest \
		--load .

## docker-buildx-push: Build and push multi-arch Docker image
docker-buildx-push:
	@echo "Building and pushing multi-arch Docker image..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):latest \
		--push .

## docker-push: Push Docker image
docker-push: docker-build
	@echo "Pushing Docker image..."
	docker push $(DOCKER_IMAGE):$(VERSION)

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
	helm template kube-janitor-go helm/kube-janitor-go --values ta

## helm-install-dry-run: Test Helm chart installation (no cluster required)
helm-install-dry-run:
	@echo "Testing Helm chart installation (template mode)..."
	helm template kube-janitor-go helm/kube-janitor-go --debug

## helm-docs: Generate Helm values documentation
helm-docs:
	@echo "Generating Helm values documentation..."
	@cd helm/kube-janitor-go && make docs

## helm-deploy: Deploy Helm chart to cluster (use VERSION=xxx to set image tag)
helm-deploy:
	@./scripts/helm-deploy.sh --version $(VERSION)

## helm-deploy-sharding: Deploy Helm chart with sharding enabled
helm-deploy-sharding:
	@./scripts/helm-deploy.sh --version $(VERSION) --sharding --replicas 3

## helm-upgrade: Upgrade existing Helm release
helm-upgrade:
	@./scripts/helm-deploy.sh --version $(VERSION) --upgrade

## helm-upgrade-sharding: Upgrade existing Helm release with sharding enabled
helm-upgrade-sharding:
	@./scripts/helm-deploy.sh --version $(VERSION) --upgrade --sharding --replicas 2

## test-pods: Deploy test pods with various TTLs (use NAMESPACE=xxx COUNT=n)
test-pods:
	@NAMESPACE=$(NAMESPACE) COUNT=$(COUNT) ./scripts/deploy-test-pods.sh

## test-pods-cleanup: Clean up test pods
test-pods-cleanup:
	@NAMESPACE=$(NAMESPACE) ./scripts/deploy-test-pods.sh --cleanup

## logs-follow: Follow logs from kube-janitor pods
logs:
	@kubectl logs -n $(JANITOR_NAMESPACE) -l app.kubernetes.io/name=kube-janitor-go -c kube-janitor-go --tail=$(TAIL) -f

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
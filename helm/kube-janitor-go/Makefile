.PHONY: all docs lint test package help

# Default target
all: lint docs

## docs: Generate values documentation
docs:
	@echo "Generating values documentation..."
	@python3 scripts/generate-values-docs.py

## lint: Lint Helm chart
lint:
	@echo "Linting Helm chart..."
	@helm lint .

## test: Test Helm chart rendering
test:
	@echo "Testing Helm chart..."
	@helm template test-release . > /tmp/test-render.yaml
	@echo "✓ Chart rendered successfully"

## package: Package Helm chart
package: lint docs
	@echo "Packaging Helm chart..."
	@helm package . -d dist/

## install: Install chart locally (template only, no cluster required)
install:
	@echo "Testing Helm chart installation (template mode)..."
	@helm template kube-janitor-go . --debug

## clean: Clean generated files
clean:
	@echo "Cleaning..."
	@rm -rf dist/

## help: Show this help message
help:
	@echo "Available targets:"
	@echo "  docs           Generate values documentation"
	@echo "  lint           Lint Helm chart"
	@echo "  test           Test Helm chart rendering"
	@echo "  package        Package Helm chart"
	@echo "  install        Test chart installation (template mode)"
	@echo "  clean          Clean generated files"
	@echo "  help           Show this help message" 
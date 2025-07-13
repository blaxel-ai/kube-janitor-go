#!/bin/bash
# Test script for kube-janitor-go Helm chart

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/kube-janitor-go"

echo "Testing kube-janitor-go Helm chart..."
echo "=================================="

# Check if helm is installed
if ! command -v helm &> /dev/null; then
    echo "Error: helm is not installed"
    exit 1
fi

# Lint the chart
echo "1. Linting chart..."
helm lint "${CHART_DIR}"
echo "✓ Chart linting passed"
echo

# Template the chart with default values
echo "2. Testing template rendering with default values..."
helm template test-release "${CHART_DIR}" > /tmp/kube-janitor-go-default.yaml
echo "✓ Default template rendering successful"
echo

# Template the chart with test values
echo "3. Testing template rendering with test values..."
helm template test-release "${CHART_DIR}" -f "${CHART_DIR}/values-test.yaml" > /tmp/kube-janitor-go-test.yaml
echo "✓ Test values template rendering successful"
echo

# Test dry-run installation
echo "4. Testing dry-run installation..."
helm install test-release "${CHART_DIR}" --dry-run --debug > /dev/null
echo "✓ Dry-run installation successful"
echo

# Package the chart
echo "5. Packaging chart..."
helm package "${CHART_DIR}" -d /tmp/
echo "✓ Chart packaging successful"
echo

# Validate generated manifests
echo "6. Validating generated Kubernetes manifests..."
if command -v kubectl &> /dev/null; then
    kubectl apply --dry-run=client -f /tmp/kube-janitor-go-default.yaml > /dev/null
    echo "✓ Kubernetes manifest validation successful"
else
    echo "⚠ kubectl not found, skipping manifest validation"
fi
echo

# Show summary
echo "Summary"
echo "======="
echo "✓ All tests passed!"
echo
echo "Generated files:"
echo "  - /tmp/kube-janitor-go-default.yaml (default values)"
echo "  - /tmp/kube-janitor-go-test.yaml (test values)"
echo "  - /tmp/kube-janitor-go-*.tgz (packaged chart)"
echo
echo "To install the chart:"
echo "  helm install kube-janitor-go ${CHART_DIR}"
echo
echo "To install with test values:"
echo "  helm install kube-janitor-go ${CHART_DIR} -f ${CHART_DIR}/values-test.yaml" 
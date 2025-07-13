#!/bin/bash

# Update documentation for kube-janitor-go Helm chart

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}"

echo "Updating kube-janitor-go Helm chart documentation..."
echo "============================================="

# Check if Python is installed
if ! command -v python3 &> /dev/null; then
    echo "Error: Python 3 is required but not installed."
    exit 1
fi

# Check if PyYAML is installed
if ! python3 -c "import yaml" &> /dev/null; then
    echo "Error: PyYAML is required but not installed."
    echo "Please install it with: pip install pyyaml"
    exit 1
fi

# Run the documentation generator
echo "Generating values documentation..."
python3 "${SCRIPT_DIR}/scripts/generate-values-docs.py"

echo ""
echo "Documentation updated successfully!"
echo ""
echo "Please review the changes in README.md before committing." 
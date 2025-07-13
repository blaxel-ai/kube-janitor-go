# Values Documentation Generation

This document explains how the Helm values documentation is automatically generated and maintained.

## Overview

The values documentation in the README.md is automatically generated from `values.yaml` to ensure it stays in sync with the actual configurable parameters.

## Script

The documentation is generated using:

**`scripts/generate-values-docs.py`**
   - Python-based generator
   - Robust YAML parsing
   - Proper handling of nested structures
   - **Requirements:**
     - Python 3.6 or higher
     - PyYAML (`pip install pyyaml`)

## Usage

### Manual Generation

```bash
# From the helm/kubernetes-event-exporter directory
make docs

# Or run directly
./scripts/generate-values-docs.py

# Or use the convenience script
./update-docs.sh
```

### Automatic Generation

The documentation is automatically generated:
- In CI when creating Helm releases
- Can be checked in PR workflows

## How It Works

1. **Parsing**: The script reads `values.yaml` and extracts:
   - Parameter names (with full path like `aws.region`)
   - Default values
   - Comments above each parameter (used as descriptions)

2. **Formatting**: Values are formatted for markdown:
   - Strings → `"value"`
   - Numbers → `123`
   - Booleans → `true`/`false`
   - Empty → `""`
   - Complex → `See values.yaml`

3. **Updating**: The script finds the `## Values` section in README.md and replaces the table

## Writing Good Comments

To generate useful documentation, add comments above values:

```yaml
# AWS region where EventBridge is located
aws:
  # Enable AWS EventBridge integration
  enabled: true
  # AWS region for EventBridge
  region: "eu-west-1"
```

These comments will be extracted and used as descriptions in the generated table.

## CI Integration

### PR Checks

The workflow `.github/workflows/helm-docs-check.yml` ensures documentation is up to date:
- Runs on PRs that modify `values.yaml` or `README.md`
- Regenerates documentation and compares
- Fails if there are differences

### Release Process

The workflow `.github/workflows/helm-chart.yml` automatically regenerates documentation before packaging the chart.

## Troubleshooting

### "See values.yaml" Everywhere

This happens when parameters don't have comments. Add descriptive comments above each value in `values.yaml`.

### Script Not Found

Ensure scripts are executable:
```bash
chmod +x scripts/generate-values-docs.py
chmod +x update-docs.sh
```

### Python Dependencies

Install required dependencies:
```bash
pip install pyyaml
```

## Development

When adding new values to `values.yaml`:
1. Add descriptive comments above each new parameter
2. Run `make docs` to update README.md
3. Commit both files together 
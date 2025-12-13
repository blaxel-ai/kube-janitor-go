# kube-janitor-go

Clean up (delete) Kubernetes resources after a configured TTL (time to live) - written in Go.

[![Go Report Card](https://goreportcard.com/badge/github.com/blaxel-ai/kube-janitor-go)](https://goreportcard.com/report/github.com/blaxel-ai/kube-janitor-go)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

## Overview

kube-janitor-go is a Kubernetes controller that automatically cleans up resources based on TTL (time to live) annotations or custom rules. It's a Go implementation inspired by the original [kube-janitor](https://codeberg.org/hjacobs/kube-janitor) project.

## Why Go Implementation?

While the original Python-based kube-janitor works well, this Go implementation offers several advantages:

### Performance Benefits
- **Lower Memory Footprint**: Go implementation typically uses 50-70% less memory than the Python version
- **Faster Startup Time**: Near-instant startup compared to Python's interpreter initialization
- **Better CPU Efficiency**: Compiled binary with efficient concurrent processing
- **Native Concurrency**: Go's goroutines provide efficient parallel resource processing

### Operational Advantages
- **Single Binary Deployment**: No runtime dependencies or interpreter needed
- **Smaller Container Images**: Alpine-based images are typically 10-20MB vs 100MB+ for Python
- **Better Resource Utilization**: Important for resource-constrained environments or large clusters
- **Native Kubernetes Client**: Uses the official Go client library with better performance

### Feature Enhancements
- **CEL Expression Language**: More powerful and performant than JMESPath for rule evaluation
- **Concurrent Workers**: Configurable worker pool for parallel resource processing
- **Structured Metrics**: Built-in Prometheus metrics with minimal overhead
- **Type Safety**: Compile-time type checking reduces runtime errors

### Use Cases for Go Version
- Large clusters with thousands of resources
- Resource-constrained environments (edge computing, IoT)
- High-frequency cleanup requirements
- Organizations standardizing on Go for Kubernetes operators

## Features

- 🧹 Clean up Kubernetes resources based on TTL annotations
- 📅 Support for expiration timestamps
- 📝 Rules-based cleanup with CEL (Common Expression Language) expressions
- 🔍 Namespace and resource type filtering
- 🚀 High performance with concurrent processing
- 📊 Prometheus metrics for monitoring
- 📋 Kubernetes event creation for audit trail and monitoring
- 🐳 Lightweight container image
- ⚡ Written in Go for better performance and lower resource usage

## Installation

> **Note**: Docker images are available for both `linux/amd64` and `linux/arm64` architectures.

### Using Helm (Recommended)

```bash
# From Helm repository
helm repo add kube-janitor-go https://blaxel-ai.github.io/kube-janitor-go
helm repo update
helm install kube-janitor-go kube-janitor-go/kube-janitor-go

# From GitHub releases
helm install kube-janitor-go https://github.com/blaxel-ai/kube-janitor-go/releases/download/v1.0.0/kube-janitor-go-1.0.0.tgz

# From source
git clone https://github.com/blaxel-ai/kube-janitor-go.git
cd kube-janitor-go
helm install kube-janitor-go ./helm/kube-janitor-go

# With custom values
helm install kube-janitor-go ./helm/kube-janitor-go -f my-values.yaml

# In specific namespace
helm install kube-janitor-go ./helm/kube-janitor-go -n kube-janitor --create-namespace
```

See the [Helm chart README](helm/kube-janitor-go/README.md) for detailed configuration options.

### Using kubectl

```bash
kubectl apply -f https://raw.githubusercontent.com/blaxel-ai/kube-janitor-go/main/deploy/kubernetes.yaml
```

### Building from source

```bash
git clone https://github.com/blaxel-ai/kube-janitor-go.git
cd kube-janitor-go
make build
```

## Usage

### Expiration Policies

kube-janitor-go supports multiple ways to define when resources should be deleted. The new annotation-based system allows for more flexible and powerful cleanup policies.

#### New Annotation Format (Recommended)

##### Max-Age Policies
Delete resources that are older than the specified duration since their creation time:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  annotations:
    janitor/delete-if-max-age: "7d"  # Delete if older than 7 days
spec:
  # ... deployment spec
```

##### Idle Policies
Delete resources that haven't been used for longer than the specified duration. Requires the `lastUsedAt` annotation to be set by your application:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  annotations:
    janitor/delete-if-idle: "1h"                        # Delete if idle for 1 hour
    lastUsedAt: "2025-09-11T14:53:56.754Z"             # Set by your application
spec:
  # ... deployment spec
```

**Important**: If the `lastUsedAt` annotation is not present, idle policies are ignored (not treated as an error).

##### Multiple Rules Support
You can define multiple rules of the same type using numbered suffixes, or combine different policy types:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pr-environment
  annotations:
    # Multiple max-age rules
    janitor/delete-if-max-age: "7d"      # Rule 1: Delete after 7 days
    janitor/delete-if-max-age-2: "30d"   # Rule 2: Backup rule after 30 days

    # Multiple idle rules
    janitor/delete-if-idle: "1h"         # Rule 1: Delete if idle for 1 hour
    janitor/delete-if-idle-2: "24h"      # Rule 2: Backup rule if idle for 24 hours

    # Mixed policy types
    janitor/delete-if-max-age-3: "3d"    # Also delete after 3 days maximum

    lastUsedAt: "2025-09-11T14:53:56.754Z"
spec:
  # ... deployment spec
```

**Rule Evaluation Logic**: If ANY rule matches, the resource will be deleted.

#### Duration Format

The system supports extended duration formats:

- **Standard Go durations**: `1h30m`, `2h`, `30m`
- **Days**: `7d`, `1.5d`
- **Weeks**: `2w`, `0.5w`
- **Months**: `1month`, `3months` (approximated as 30 days)
- **Combined**: `1month2w3d12h30m`

#### Legacy Annotations (Still Supported)

##### `janitor/ttl`
Relative time duration after which the resource should be deleted:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: temp-pod
  annotations:
    janitor/ttl: "24h"
spec:
  containers:
  - name: app
    image: nginx
```

##### `janitor/expires`
Absolute timestamp for deletion:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: feature-branch-deployment
  annotations:
    janitor/expires: "2024-12-31T23:59:59Z"
spec:
  # ... deployment spec
```

#### Priority Order

The system evaluates annotations in this order:
1. **New annotations** (`janitor/delete-if-*`) - highest priority
2. **Legacy TTL** (`janitor/ttl`) - fallback
3. **Legacy expires** (`janitor/expires`) - fallback
4. **Rules engine** - lowest priority

#### Real-World Examples

##### PR Environment Cleanup
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pr-123-feature-branch
  annotations:
    janitor/delete-if-idle: "2h"       # Clean up if no activity for 2 hours
    janitor/delete-if-max-age: "3d"    # Force cleanup after 3 days
    lastUsedAt: "2025-09-11T14:53:56.754Z"
```

##### Development Sandbox
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dev-sandbox
  annotations:
    janitor/delete-if-idle: "4h"       # Clean up if unused for 4 hours
    janitor/delete-if-max-age: "1d"    # Daily cleanup
    lastUsedAt: "2025-09-11T14:53:56.754Z"
```

##### Temporary Resources
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: temp-config
  annotations:
    janitor/delete-if-max-age: "1h"    # Temporary config, 1 hour max
```

### Command Line Options

```
Usage:
  kube-janitor-go [flags]

Flags:
      --dry-run                      Dry run mode: print what would be deleted without actually deleting
      --interval duration            Interval between cleanup runs (default 30s)
      --once                         Run once and exit
      --include-resources strings    Resource types to include (default: all)
      --exclude-resources strings    Resource types to exclude (default: events,controllerrevisions)
      --include-namespaces strings   Namespaces to include (default: all)
      --exclude-namespaces strings   Namespaces to exclude (default: kube-system,kube-public,kube-node-lease)
      --rules-file string           Path to YAML file containing cleanup rules
      --metrics-port int            Port for Prometheus metrics (default 8080)
      --log-level string            Log level: debug, info, warn, error (default "info")
      --max-workers int             Maximum number of concurrent workers (default 10)
  -h, --help                        help for kube-janitor-go
```

### Rules File

Create custom cleanup rules using CEL expressions:

```yaml
rules:
  # Delete deployments without app label after 7 days
  - id: require-app-label
    resources:
      - deployments
      - statefulsets
    expression: "!has(object.spec.template.metadata.labels.app)"
    ttl: 7d

  # Delete PR deployments after 4 hours
  - id: temporary-pr-deployments
    resources:
      - deployments
    expression: 'object.metadata.name.startsWith("pr-")'
    ttl: 4h

  # Clean up resources in temp namespaces
  - id: temp-namespace-cleanup
    resources:
      - "*"
    expression: 'object.metadata.namespace == "temp"'
    ttl: 3d

  # Delete unbound PVCs after 7 days
  - id: cleanup-unbound-pvcs
    resources:
      - persistentvolumeclaims
    expression: 'object.status.phase == "Pending"'
    ttl: 7d
```

## Deployment

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-janitor-go
  namespace: kube-janitor
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kube-janitor-go
  template:
    metadata:
      labels:
        app: kube-janitor-go
    spec:
      serviceAccountName: kube-janitor-go
      containers:
      - name: kube-janitor-go
        image: ghcr.io/blaxel-ai/kube-janitor-go:latest
        args:
          - --interval=60s
          - --exclude-namespaces=kube-system,kube-public,kube-node-lease
          - --log-level=info
        resources:
          requests:
            memory: "64Mi"
            cpu: "100m"
          limits:
            memory: "256Mi"
            cpu: "500m"
```

## Metrics

kube-janitor-go exposes Prometheus metrics on the `/metrics` endpoint:

- `kube_janitor_resources_deleted_total`: Total number of resources deleted
- `kube_janitor_resources_evaluated_total`: Total number of resources evaluated
- `kube_janitor_cleanup_duration_seconds`: Histogram of cleanup run durations
- `kube_janitor_errors_total`: Total number of errors encountered

## Events

kube-janitor-go creates Kubernetes events to provide an audit trail of its actions. Events are created for:

- **Resource Deletion**: When a resource is successfully deleted
- **Resource Scheduled for deletion**: When a resource is scheduled for deletion
- **Deletion Failure**: When a resource deletion fails
- **Dry Run**: When a resource would be deleted (in dry-run mode)

### Viewing Events

```bash
# View events for a specific resource
kubectl describe deployment my-deployment

# View all kube-janitor events in a namespace
kubectl get events -n default --field-selector source=kube-janitor

# Watch events in real-time
kubectl get events -n default -w --field-selector source=kube-janitor

# View events with more details
kubectl get events -n default -o wide | grep kube-janitor
```

### Event Examples

```
LAST SEEN   TYPE     REASON            OBJECT                     MESSAGE
5s          Normal   ResourceDeleted   deployment/test-app        Deleted deployment default/test-app - TTL expired (age: 2h1m, ttl: 2h)
10s         Normal   ResourceDeleted   configmap/temp-config      Deleted configmap default/temp-config - Expiration time reached (2024-01-15T10:00:00Z)
15s         Warning  DeletionFailed    service/broken-svc         Failed to delete service default/broken-svc: services "broken-svc" not found
20s         Normal   DryRunDeletion    pod/test-pod              DRY RUN: Would delete pod default/test-pod - Rule 'cleanup-test-pods' matched (age: 1h, ttl: 30m)
```

## Development

### Prerequisites

- Go 1.24+
- Docker (for building images)
- kubectl (for testing)
- Kind or Minikube (for local testing)

### Building

```bash
# Build binary
make build

# Build Docker image (single architecture)
make docker-build

# Build multi-architecture Docker image (linux/amd64, linux/arm64)
make docker-buildx

# Build and push multi-architecture Docker image
make docker-buildx-push

# Run all tests
make test

# Run tests with coverage
go test -v -race -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html

# Run specific package tests
go test -v ./internal/janitor
go test -v ./internal/rules
go test -v ./internal/metrics

# Run integration tests
go test -v ./internal -tags=integration

# Run linting
make lint
```

### Running locally

```bash
# Run against current kubeconfig context
go run cmd/kube-janitor-go/main.go --dry-run --log-level=debug

# Run with custom rules
go run cmd/kube-janitor-go/main.go --rules-file=examples/rules.yaml --dry-run
```

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for details.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -am 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- Inspired by the original [kube-janitor](https://codeberg.org/hjacobs/kube-janitor) project by Henning Jacobs
- Built with [client-go](https://github.com/kubernetes/client-go) and [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
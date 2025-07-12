# Contributing to kube-janitor-go

Thank you for your interest in contributing to kube-janitor-go! This document provides guidelines and instructions for contributing.

## Code of Conduct

Please be respectful and considerate in all interactions. We aim to maintain a welcoming and inclusive community.

## Getting Started

### Prerequisites

- Go 1.24 or later
- Docker (for building images)
- Access to a Kubernetes cluster (for testing)
- golangci-lint (for linting)

### Setting up the development environment

1. Fork and clone the repository:
```bash
git clone https://github.com/blaxel-ai/kube-janitor-go.git
cd kube-janitor-go
```

2. Install dependencies:
```bash
go mod download
```

3. Install development tools:
```bash
make install-tools
```

## Development Workflow

### Building

```bash
make build
```

### Running tests

```bash
make test
```

### Running linting

```bash
make lint
```

### Running locally

```bash
# Dry run against your current kubeconfig context
make run

# Or directly:
go run cmd/kube-janitor-go/main.go --dry-run --log-level=debug
```

## Making Changes

1. Create a new branch for your feature or fix:
```bash
git checkout -b feature/your-feature-name
```

2. Make your changes following the code style guidelines

3. Add or update tests as needed

4. Run tests and linting:
```bash
make test lint
```

5. Commit your changes with a descriptive commit message:
```bash
git commit -am "feat: add support for X"
```

### Commit Message Format

We follow conventional commits format:

- `feat:` for new features
- `fix:` for bug fixes
- `docs:` for documentation changes
- `chore:` for maintenance tasks
- `test:` for test changes
- `refactor:` for code refactoring

## Code Style Guidelines

### Go Code

- Follow standard Go conventions and idioms
- Use `gofmt` and `gofumpt` for formatting
- Keep functions small and focused
- Add comments for exported types and functions
- Handle errors explicitly
- Use meaningful variable names

### Example:

```go
// ProcessResource evaluates and potentially deletes a Kubernetes resource
// based on configured rules and annotations.
func ProcessResource(ctx context.Context, resource *unstructured.Unstructured) error {
    if resource == nil {
        return fmt.Errorf("resource cannot be nil")
    }
    
    // Process the resource...
    return nil
}
```

## Testing

### Unit Tests

- Write unit tests for new functionality
- Aim for good test coverage
- Use table-driven tests where appropriate
- Mock external dependencies

### Integration Tests

- Test against a real Kubernetes cluster when possible
- Use Kind or Minikube for local testing

## Documentation

- Update README.md if adding new features or changing behavior
- Add comments to complex code sections
- Update examples if needed

## Submitting Changes

1. Push your changes to your fork:
```bash
git push origin feature/your-feature-name
```

2. Create a Pull Request with:
   - Clear title and description
   - Reference to any related issues
   - Summary of changes made
   - Any breaking changes noted

3. Wait for review and address any feedback

## Reporting Issues

When reporting issues, please include:

- Kubernetes version
- kube-janitor-go version
- Steps to reproduce
- Expected vs actual behavior
- Any relevant logs or error messages

## Feature Requests

Feature requests are welcome! Please:

- Check existing issues first
- Describe the use case
- Explain why this would be valuable
- Consider submitting a PR if you can implement it

## Questions?

Feel free to open an issue for questions or join our discussions.

Thank you for contributing! 
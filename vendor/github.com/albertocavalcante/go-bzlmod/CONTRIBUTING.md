# Contributing to Go-bzlmod

## Getting Started

**Prerequisites:** Go 1.23.0+, Git, Bazel/Bazelisk (optional for e2e tests)

**Setup:**

```bash
git clone https://github.com/your-username/go-bzlmod.git
cd go-bzlmod
go mod download
go test ./...
cd e2e && go test ./...
```

## Testing

**Unit tests:**

```bash
go test ./...
go test -race ./...
go test -coverprofile=coverage.out ./...
```

**E2E tests:**

```bash
cd e2e && go test ./... -v
cd e2e && go test ./... -run="TestDiagnostic" -v
cd e2e && GO_BZLMOD_E2E_RELEASE_MATRIX=1 GO_BZLMOD_E2E_RELEASE_MATRIX_REFRESH=1 go test ./... -run TestE2E_BazelReleaseMatrix_ToolSelectionParity
```

Aim for >90% test coverage.

## Code Quality

**Linting:**

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
golangci-lint run
```

**Formatting:**

```bash
go fmt ./...
goimports -w .
```

## Development Guidelines

Follow Go conventions. Document all public APIs. Keep functions small and focused. Use meaningful names. Test public APIs thoroughly with table-driven tests. Mock external dependencies.

## Bug Reports

Include Go version, OS/architecture, minimal reproduction case, expected vs actual behavior, and full error messages.

## Feature Requests

Describe the use case, provide API examples, consider alternatives, discuss breaking changes.

## Pull Request Process

Create an issue first for significant changes. Write tests. Update documentation. Run CI locally.

**Checklist:**

```
Tests pass: go test ./...
E2E tests pass: cd e2e && go test ./...
Linting passes: golangci-lint run
Documentation updated (if applicable)
Commits signed off: git commit -s
```

**Commit format:**

```
type(scope): description

Signed-off-by: Your Name <your.email@example.com>
```

Types: feat, fix, docs, style, refactor, test, chore

## Architecture

**Core components:**
api.go (public API), types.go (data structures), parser.go (MODULE.bazel parsing), registry.go (registry client), resolver.go (MVS algorithm)

**Design principles:**
Pure MVS implementation, thread-safe operations, context support, detailed error information.

## Resources

[Go Documentation](https://golang.org/doc/), [Effective Go](https://golang.org/doc/effective_go.html), [Bazel Module System](https://bazel.build/external/module), [Minimal Version Selection](https://research.swtch.com/vgo-mvs)

## License

MIT License applies to all contributions.

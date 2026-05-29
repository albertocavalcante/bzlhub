# Agent Guidelines

Guidelines for AI agents and contributors working on this project.

## Commit Messages

This project follows [Semantic Commit Messages](https://www.conventionalcommits.org/).

### Format

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

### Types

| Type       | Description                                |
| ---------- | ------------------------------------------ |
| `feat`     | New feature                                |
| `fix`      | Bug fix                                    |
| `docs`     | Documentation changes                      |
| `chore`    | Maintenance, dependencies, tooling         |
| `refactor` | Code restructuring without behavior change |
| `test`     | Adding or updating tests                   |
| `ci`       | CI/CD changes                              |
| `perf`     | Performance improvements                   |
| `style`    | Code style (formatting, whitespace)        |
| `build`    | Build system changes                       |

### Scopes

Common scopes for this project:

| Scope      | Description               |
| ---------- | ------------------------- |
| `parser`   | MODULE.bazel parsing      |
| `resolver` | MVS dependency resolution |
| `registry` | BCR registry client       |
| `api`      | Public API changes        |
| `types`    | Core data types           |
| `e2e`      | End-to-end tests          |

### Examples

```
feat(resolver): implement compatibility_level handling
feat(parser): add support for archive_override
fix(registry): handle rate limiting from BCR
fix(resolver): correct MVS version comparison
docs: update README with resolution examples
chore: upgrade buildtools dependency
refactor(registry): extract HTTP client config
test(resolver): add edge cases for diamond dependencies
perf(registry): add response caching
```

### Validation

Commit messages are validated by lefthook. Install hooks with:

```bash
lefthook install
```

## Code Style

- Follow standard Go conventions (`gofmt`, `goimports`)
- Run `golangci-lint run` before committing
- Keep functions focused and small
- Document all exported types and functions
- Handle errors explicitly, no silent failures

## Testing

Run tests before committing:

```bash
# Unit tests
go test ./...

# With race detection
go test -race ./...

# E2E tests
cd e2e && go test ./... -v

# Release-matrix Bazel parity refresh
cd e2e && GO_BZLMOD_E2E_RELEASE_MATRIX=1 GO_BZLMOD_E2E_RELEASE_MATRIX_REFRESH=1 go test ./... -run TestE2E_BazelReleaseMatrix_ToolSelectionParity
```

Aim for >90% test coverage on core logic.

## Architecture

### Core Components

| File          | Purpose                               |
| ------------- | ------------------------------------- |
| `api.go`      | Public API entry points               |
| `types.go`    | Core data structures                  |
| `parser.go`   | MODULE.bazel parsing using buildtools |
| `registry.go` | BCR registry HTTP client with caching |
| `resolver.go` | MVS dependency resolution algorithm   |

### Design Principles

- **Pure MVS**: Follow Go's Minimal Version Selection algorithm
- **Thread-safe**: All operations safe for concurrent use
- **Context-aware**: Support cancellation and timeouts
- **Detailed errors**: Include context for debugging

## Dependencies

- Use `go mod tidy` after adding/removing dependencies
- Prefer standard library when possible
- Key dependencies:
  - `github.com/bazelbuild/buildtools` - Starlark/BUILD file parsing
  - `golang.org/x/mod/semver` - Semantic version comparison

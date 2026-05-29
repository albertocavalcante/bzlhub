# go-bzlmod development commands
# Run `just --list` to see available commands

# Packages to exclude from tests/linting (vendored code, tools)
exclude_pattern := "third_party|tools/"

# Default recipe: run tests
default: test

# Build all packages (includes third_party since it's part of the module)
build:
    go build ./...

# Run all tests (excludes third_party and tools)
test:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go test

# Run end-to-end tests from the independent e2e module
test-e2e:
    cd e2e && go test ./...

# Run end-to-end tests with verbose output
test-e2e-v:
    cd e2e && go test ./... -v

# Run tests with verbose output
test-v:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go test -v

# Run tests with race detector
test-race:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go test -race

# Run tests with gotestsum (better output)
test-sum:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go tool -modfile=tools.go.mod gotestsum --format pkgname-and-test-fails --

# Run tests with gotestsum and race detector
test-sum-race:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go tool -modfile=tools.go.mod gotestsum --format pkgname-and-test-fails -- -race

# Run benchmarks
bench:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go test -bench=. -benchmem

# Run golangci-lint (uses tools/lint module)
lint:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go run -modfile=tools/lint/go.mod github.com/golangci/golangci-lint/v2/cmd/golangci-lint run

# Run staticcheck (uses tools/lint module)
staticcheck:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go run -modfile=tools/lint/go.mod honnef.co/go/tools/cmd/staticcheck

# Run all linters
lint-all: lint staticcheck

# Tidy all go.mod files
tidy:
    go mod tidy
    cd e2e && go mod tidy
    go mod tidy -modfile=tools.go.mod
    cd tools/lint && go mod tidy

# Verify the module has no external dependencies
verify-deps:
    #!/usr/bin/env bash
    echo "Checking for direct dependencies..."
    deps=$(go list -m -f '{{"{{if not .Indirect}}{{.Path}}{{end}}"}}' all 2>/dev/null | grep -v "^github.com/albertocavalcante/go-bzlmod$" || true)
    if [ -z "$deps" ]; then
        echo "✓ No direct dependencies!"
    else
        echo "✗ Found direct dependencies:"
        echo "$deps"
        exit 1
    fi

# Run go vet (excludes third_party and tools)
vet:
    go list ./... | grep -v -E '{{exclude_pattern}}' | xargs go vet

# Format code (excludes third_party)
fmt:
    find . -name '*.go' -not -path './third_party/*' -not -path './tools/lint/*' | xargs gofmt -w

# Check if code is formatted (excludes third_party)
fmt-check:
    #!/usr/bin/env bash
    unformatted=$(find . -name '*.go' -not -path './third_party/*' -not -path './tools/lint/*' -exec gofmt -l {} +)
    if [ -n "$unformatted" ]; then
        echo "Code is not formatted. Run 'just fmt'"
        echo "$unformatted"
        exit 1
    fi

# Clean build cache
clean:
    go clean -cache

# Vendor the buildtools parser (updates third_party/buildtools)
vendor-parser tag="":
    #!/usr/bin/env bash
    if [ -z "{{tag}}" ]; then
        go run ./tools/vendor-parser
    else
        go run ./tools/vendor-parser -tag {{tag}}
    fi

# Show vendored parser version
vendor-version:
    @cat third_party/buildtools/VERSION | jq .

# Run all checks (CI)
ci: build test lint-all vet fmt-check

# Run unit checks plus e2e module
ci-e2e: ci test-e2e

# Development workflow: format, lint, test
dev: fmt lint test

# Show which packages will be tested/linted
show-packages:
    @echo "Packages included in test/lint:"
    @go list ./... | grep -v -E '{{exclude_pattern}}'

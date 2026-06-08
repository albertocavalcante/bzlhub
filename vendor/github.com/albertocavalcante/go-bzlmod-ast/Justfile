# go-bzlmod-ast — MODULE.bazel parse + AST + structured surface.
# Run `just --list` to see available commands.
#
# Tools (golangci-lint, gotestsum, shfmt) are pinned in tools.go.mod
# and built into ./bin/ via `just sync-tools`. shellcheck + just
# itself install via committed hermetic scripts under tools/.

set shell := ["bash", "-uc"]

# Default recipe: run the CI gate.
default: check

# ============================================================================
# Tool sync
# ============================================================================

# Build pinned tool binaries from tools.go.mod into ./bin/.
# Re-run after touching tools.go.mod or after a `git clean`.
sync-tools:
    @mkdir -p bin
    @echo "→ building golangci-lint into bin/"
    go build -mod=mod -modfile=tools.go.mod -o bin/golangci-lint github.com/golangci/golangci-lint/v2/cmd/golangci-lint
    @echo "→ building gotestsum into bin/"
    go build -mod=mod -modfile=tools.go.mod -o bin/gotestsum gotest.tools/gotestsum
    @echo "→ building shfmt into bin/"
    go build -mod=mod -modfile=tools.go.mod -o bin/shfmt mvdan.cc/sh/v3/cmd/shfmt

# Tidy the tools module (after editing tools.go.mod).
sync-tools-tidy:
    go mod tidy -modfile=tools.go.mod

# Hermetic install of `just` itself (pinned version + sha256 per
# platform). Useful for bootstrapping a fresh CI runner or for local
# devs who don't have `just` available via a package manager. See
# tools/README.md for the threat model and bump procedure.
install-just:
    ./tools/install-just.sh ./bin

# Hermetic install of shellcheck (pinned version + sha256 per
# platform). Same pattern as install-just; see tools/README.md.
install-shellcheck:
    ./tools/install-shellcheck.sh ./bin

# Bootstrap every hermetic tool (just + shellcheck). Run once after
# clone; `sync-tools` covers the Go-backed tools (golangci-lint,
# gotestsum, shfmt) separately.
install-tools: install-just install-shellcheck

# ============================================================================
# Format / vet / lint
# ============================================================================

# Format every Go file under this module (writes in place). vendor/
# and third_party/ are upstream code and left alone.
fmt:
    #!/usr/bin/env bash
    set -euo pipefail
    find . -name '*.go' -not -path './vendor/*' -not -path './bin/*' -not -path './third_party/*' -print0 | xargs -0 gofmt -w

# Fail if anything outside vendor/ isn't gofmt-clean.
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    dirty="$(gofmt -l . | grep -v '^vendor/' | grep -v '^third_party/' || true)"
    if [ -n "$dirty" ]; then
        echo "files need gofmt:"
        echo "$dirty"
        exit 1
    fi

# go vet over the whole tree, minus vendored upstream
# (third_party/buildtools/build carries pre-existing %#q + int
# Sprintf usage we don't modify).
vet:
    go vet $(go list ./... | grep -v /third_party/)

# golangci-lint (sync-tools must have been run at least once).
# third_party/ skip is honored by .golangci.toml's exclude-dirs.
lint: _require-tools
    ./bin/golangci-lint run ./...

# Format every committed shell script. Skips vendor/ + bin/.
sh-fmt: _require-tools
    #!/usr/bin/env bash
    set -euo pipefail
    find . -name '*.sh' -not -path './vendor/*' -not -path './bin/*' -print0 \
        | xargs -0 ./bin/shfmt -w -i 4 -ci -sr

# Fail if any shell script isn't shfmt-clean.
sh-fmt-check: _require-tools
    #!/usr/bin/env bash
    set -euo pipefail
    dirty="$(find . -name '*.sh' -not -path './vendor/*' -not -path './bin/*' -print0 \
        | xargs -0 ./bin/shfmt -l -i 4 -ci -sr || true)"
    if [ -n "$dirty" ]; then
        echo "shell scripts need shfmt -w:"
        echo "$dirty"
        exit 1
    fi

# Static-analyze every committed shell script. Skips vendor/ + bin/.
# bin/shellcheck installed via `just install-shellcheck`.
shellcheck:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -x bin/shellcheck ]; then
        echo "bin/shellcheck missing — run 'just install-shellcheck' first" >&2
        exit 2
    fi
    find . -name '*.sh' -not -path './vendor/*' -not -path './bin/*' -print0 \
        | xargs -0 ./bin/shellcheck

# ============================================================================
# Modernize (Go 1.26 `go fix`)
# ============================================================================

# Apply Go 1.26 modernize fixes in place (slices.Contains, range-int,
# strings.Cut, new(expr), etc.). Review the diff before committing.
# third_party/ is vendored upstream; excluded.
modernize:
    go fix $(go list ./... | grep -v /third_party/)

# Fail if `go fix` has any pending modernize suggestions OUTSIDE
# third_party/ (vendored upstream code we don't modify). go fix
# follows imports into third_party regardless of which packages
# we list, so we filter the diff and only fail if non-third_party
# changes remain.
modernize-check:
    #!/usr/bin/env bash
    set -euo pipefail
    diff=$(go fix -diff ./... 2>&1 || true)
    filtered=$(echo "$diff" | grep -v '^[+-].*third_party/' | grep -v '^diff .*third_party/' || true)
    if echo "$filtered" | grep -q '^diff '; then
        echo "go fix has pending modernize suggestions:"
        echo "$diff"
        exit 1
    fi

# ============================================================================
# Test
# ============================================================================

# Standard test run via gotestsum (testdox format). third_party/
# is vendored upstream and excluded (carries pre-existing vet
# issues that upstream owns).
test: _require-tools
    ./bin/gotestsum --format testdox --packages "$(go list ./... | grep -v /third_party/)" --

# Race-detector run; CI's gate.
test-race: _require-tools
    ./bin/gotestsum --format testdox --packages "$(go list ./... | grep -v /third_party/)" -- -race -count=1

# Coverage report (HTML + summary).
test-cover: _require-tools
    ./bin/gotestsum --format testdox --packages "$(go list ./... | grep -v /third_party/)" -- -race -coverprofile=coverage.out
    go tool cover -func=coverage.out
    go tool cover -html=coverage.out -o coverage.html
    @echo "coverage report: coverage.html"

# ============================================================================
# Aggregates
# ============================================================================

# Full CI gate. Same recipe CI runs.
check: fmt-check sh-fmt-check shellcheck vet lint modernize-check test-race

# Quick dev loop: format then re-run tests. Skips lint + modernize so
# the iteration stays fast; CI is the eventual safety net.
dev: fmt test

# Print every pinned version across the build/CI surface so the
# quarterly bump review has one place to start. Pure read; no
# network calls. Cross-reference each pin against upstream's
# current release per tools/README.md.
audit-pins:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "== Go toolchain =="
    grep -E '^go ' go.mod tools.go.mod
    echo
    echo "== Go-based dev tools (tools.go.mod) =="
    awk '/^tool \(/,/^\)/' tools.go.mod | grep -v -E '^tool|^\)' | sed 's/^[[:space:]]*/  /'
    echo
    echo "== Hermetic non-Go tools (tools/install-*.sh) =="
    grep -E '^(JUST|SHELLCHECK)_VERSION=' tools/install-*.sh
    echo
    echo "== CI action pins =="
    grep -h 'uses:' .forgejo/workflows/*.yml .github/workflows/*.yml | sort -u
    echo
    echo "Bump procedure: tools/README.md"

# ============================================================================
# Internal helpers
# ============================================================================

# Ensure tool binaries exist; bootstrap them if not.
_require-tools:
    @if [ ! -x bin/golangci-lint ] || [ ! -x bin/gotestsum ]; then \
        echo "tool binaries missing — running sync-tools first"; \
        just sync-tools; \
    fi

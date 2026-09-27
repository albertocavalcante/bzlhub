# Pin the toolchain by name. The `toolchain` directive in go.mod is only a
# MINIMUM -- with a newer Go installed it is ignored, so it cannot pin downward.
# Only GOTOOLCHAIN does. Keep this in lockstep with go.mod and the
# GO_BUILDER_BASE in Dockerfile; `just toolchain` prints what actually ran.
export GOTOOLCHAIN := "go1.26.6"

# Recipes stay to a single command. A recipe body is invisible to shellcheck and
# shfmt, so anything with real logic lives in tools/*.sh instead.
#
# This mirrors .github/workflows/ci.yml, which is named "ci (dormant)" and does
# not run. That is exactly why this file exists: canopy was the only repo in the
# workspace with no local gate, and a server sat on ten reachable
# vulnerabilities long enough for a routine dependency bump to land on top of
# them without anyone noticing.

default: ci

# The gate. Mirrors the dormant workflow's go job, in the same order.
ci: toolchain mod-verify fmt vet build test vuln

# Everything, including the UI. Slower: pnpm install plus a vite build.
ci-full: ci ui-check ui-test

# Print the Go that actually ran. Drift is invisible otherwise.
toolchain:
    go version

# `tidy -diff` fails instead of rewriting, so a dirty graph is a red gate
# rather than a surprise diff in the working tree.
mod-verify:
    go mod tidy -diff && go mod verify

fmt:
    gofmt -l . | grep -v '^vendor/' | (! grep .) || (echo "run: gofmt -w ." >&2; exit 1)

vet:
    go vet ./...

build:
    go build ./...

# -race and -count=1 both matter: this is a concurrent server, and without
# -count=1 Go serves a cached PASS and the gate reports green having run nothing.
test *ARGS:
    go test -race -count=1 {{ARGS}} ./...

# Reachable-CVE scan. Pinned version so the gate cannot change under you.
vuln:
    go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

# Stage the SvelteKit bundle where the Go binary embeds it from.
ui-embed:
    ./tools/ui-embed.sh

ui-check:
    pnpm --dir ui install --frozen-lockfile && pnpm --dir ui run check

ui-test:
    pnpm --dir ui install --frozen-lockfile && pnpm --dir ui test

# Run the server against a local store. Ctrl-C to stop.
serve *ARGS:
    go run ./cmd/bzlhub serve {{ARGS}}

# Container equivalents, for checking the image rather than the source.
up:
    podman compose up --build -d

down:
    podman compose down -v

logs:
    podman compose logs -f bzlhub

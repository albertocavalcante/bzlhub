# bzlhub: containerized build for self-hosted deployments (alpine).
#
# Three-stage build:
#   1) ui-builder — pnpm builds the SvelteKit bundle; output lands at
#                   /src/ui/build/ for the go-builder to overlay onto
#                   internal/embed/ui/ (the //go:embed location).
#   2) go-builder — go build links the embedded UI plus the bzlhub
#                   server/CLI into a single static binary.
#   3) runtime    — alpine:3 base with the binary, a non-root user,
#                   and /var/lib/bzlhub/{mirror,index} as the
#                   persistent data dirs.
#
# Default build target: linux/arm64 (Hetzner CAX servers are Ampere
# Altra). Cross-build for amd64 with
# `docker buildx build --platform linux/amd64`. CGO is disabled —
# bzlhub.s SQLite driver is the pure-Go modernc.org/sqlite path; no
# musl-libc shim needed.
#
# Corporate-base-image overrides
# ------------------------------
# Each stage's base image is parameterized via an `ARG` so corporate
# environments that mandate internal hardened images can swap any
# stage without forking this file. Example:
#
#   docker build \
#     --build-arg UI_BUILDER_BASE=registry.corp.example.com/blessed/node:22 \
#     --build-arg GO_BUILDER_BASE=registry.corp.example.com/blessed/golang:1.26 \
#     --build-arg RUNTIME_BASE=registry.corp.example.com/blessed/alpine:3.20 \
#     -t bzlhub:corp .
#
# The override image must satisfy the stage's requirements: ui-builder
# needs Node 20+ with corepack/pnpm support, go-builder needs Go
# 1.26+, runtime must be alpine-family (uses `apk add`). For RHEL/UBI
# runtimes use Dockerfile.rhel9 instead.
ARG UI_BUILDER_BASE=node:22-alpine
ARG GO_BUILDER_BASE=golang:1.26-alpine
ARG RUNTIME_BASE=alpine:3

# -------- ui-builder --------------------------------------------------------
FROM ${UI_BUILDER_BASE} AS ui-builder
WORKDIR /src

# pnpm via corepack, pinned to the version the dev workstation uses.
RUN corepack enable && corepack prepare pnpm@10.33.0 --activate

# Lock files first for a cache-friendly install layer.
COPY ui/package.json ui/pnpm-lock.yaml ./ui/
RUN cd ui && pnpm install --frozen-lockfile

# Then the full UI source. adapter-static emits into ui/build/.
COPY ui/ ./ui/
RUN cd ui && pnpm run build

# -------- go-builder --------------------------------------------------------
#
# bzlhub has two kinds of private-or-local Go module deps:
#   - local-path replace directives (assay, go-bzlmod — sibling repos
#     in the same workspace; have v0.0.0 placeholders)
#   - private GitHub modules pulled from tagged versions (scip-bazel,
#     scip-starlark, understory)
#
# Both kinds resolve cleanly via `go mod vendor` on the dev machine
# (host has GitHub auth + workspace dirs visible). The ship.env's
# PRE_BUILD_CMD ensures `go mod vendor` runs before this image build
# starts, so vendor/ is already in the build context. We then build
# with -mod=vendor — no network, no auth, no replace gymnastics.
FROM ${GO_BUILDER_BASE} AS go-builder
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOFLAGS="-trimpath -mod=vendor"

COPY . .

# Overlay the UI bundle the ui-builder produced. //go:embed in
# internal/embed/embed.go expects ui/ as a sibling.
COPY --from=ui-builder /src/ui/build/ ./internal/embed/ui/

# Refuse to build without a vendor/ tree — surfaces the pre-build
# requirement as a clear error rather than a cryptic go-resolve
# failure. Run `go mod vendor` in the source repo first (or use
# self-hosted/scripts/ship-local.sh which automates it).
RUN test -d vendor || (echo "ERROR: bzlhub/vendor/ missing — run 'go mod vendor' first" >&2 && exit 1)

# Guard the embed overlay too — a silent failure here ships a binary
# with the "UI not built" stub instead of the actual UI.
RUN test -f internal/embed/ui/index.html || (echo "ERROR: internal/embed/ui/index.html missing — ui-builder stage didn't produce it" >&2 && exit 1)

# Build-time version metadata. Defaults to sentinels so the image
# still builds without ship-local.sh's --build-arg flags. ship-local
# fills them from `git describe`, the short SHA, and a UTC timestamp.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown

# Static, stripped, no DWARF. ldflags -X injects version metadata
# into internal/version's runtime vars; /api/version, the UI footer,
# and `bzlhub --version` all read from them.
RUN go build \
      -ldflags="-s -w \
        -X github.com/albertocavalcante/bzlhub/internal/version.Version=${VERSION} \
        -X github.com/albertocavalcante/bzlhub/internal/version.Commit=${COMMIT} \
        -X github.com/albertocavalcante/bzlhub/internal/version.BuiltAt=${BUILT_AT}" \
      -o /out/bzlhub \
      ./cmd/bzlhub

# -------- runtime -----------------------------------------------------------
FROM ${RUNTIME_BASE}

LABEL org.opencontainers.image.title="bzlhub" \
      org.opencontainers.image.description="Bazel-first self-hosted module registry" \
      org.opencontainers.image.source="https://github.com/albertocavalcante/bzlhub" \
      org.opencontainers.image.licenses="MIT"

# UID/GID pinned to 65532 (de-facto "nonroot" convention used by
# distroless). Data dirs are chown'd to 65532:0 with g+rwX so the
# image runs cleanly under three identity models:
#   - vanilla k8s with podSecurityContext.runAsUser=65532/fsGroup=65532
#   - OpenShift SCC, which assigns a random UID per namespace and
#     always runs containers with supplementary GID 0
#   - rootless Podman, which maps host UID into the container
# The group-writable bit on data dirs is the OpenShift contract; it
# isn't a security relaxation because GID 0 inside an unprivileged
# container is not the same as host root.
# `git` is required by the procurement admit pipeline:
# internal/publish/GitDirectPublisher shells out to `git commit` +
# `git push` to land approved modules back into the registry repo.
# Without it, any request that reaches the fetching state crashes
# with "exec: git: executable file not found in $PATH". Deployments
# that don't enable procurement (no BZLHUB_POLICY_FILE) never touch
# the binary; ~5 MiB of alpine git is a cheap floor to pay.
RUN apk add --no-cache ca-certificates tzdata wget git \
 && addgroup -g 65532 -S bzlhub \
 && adduser  -u 65532 -S -G bzlhub -h /var/lib/bzlhub bzlhub \
 && mkdir -p /var/lib/bzlhub/mirror /var/lib/bzlhub/index /var/lib/bzlhub/sources \
 && chown -R 65532:0 /var/lib/bzlhub \
 && chmod -R g+rwX  /var/lib/bzlhub

COPY --from=go-builder /out/bzlhub /usr/local/bin/bzlhub

# Entrypoint is a real file in the repo (deploy/entrypoint.sh) rather
# than a Dockerfile heredoc. Heredoc COPY (and COPY --chmod=) are
# BuildKit-specific frontend features; the classic COPY + RUN chmod
# pattern works under every recent Docker/Podman/buildah without
# depending on a specific frontend version.
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

USER bzlhub
WORKDIR /var/lib/bzlhub

ENV BZLHUB_BIND=0.0.0.0:8090
ENV BZLHUB_ROOT=/var/lib/bzlhub/mirror
ENV BZLHUB_DB=/var/lib/bzlhub/index/bzlhub.db
ENV BZLHUB_MIRROR_BASE_URL=

EXPOSE 8090

# bzlhub.s /healthz is unconditional 200 — both BCR-only and DB-only
# deployments answer it. wget is the smallest probe binary alpine
# ships by default; --spider would also work.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:8090/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

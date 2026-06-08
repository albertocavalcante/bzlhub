# Container images — two flavors + corporate base override

bzlhub ships two Dockerfiles. Pick based on your deployment's
runtime requirements:

| File | Runtime base | When to use |
|---|---|---|
| `Dockerfile` (default) | `alpine:3` | Personal installs, corp envs that allow alpine, Hetzner/cheap-VPS deployments, anywhere musl is fine |
| `Dockerfile.rhel9` | `registry.access.redhat.com/ubi9/ubi-minimal` | RHEL 9 / CentOS Stream 9 / Rocky 9 / AlmaLinux 9 mandates; corp envs that require glibc + RH provenance; OpenShift environments that prefer UBI |

Both produce a binary-identical bzlhub executable. CGO is disabled
and the SQLite driver is pure Go (`modernc.org/sqlite`), so the
static binary built in either flavor runs on either runtime
unchanged. The choice is purely about the runtime distribution +
its package manager.

## Building

Default (alpine):

```bash
docker build -t bzlhub:latest .
```

RHEL9 family:

```bash
docker build -f Dockerfile.rhel9 -t bzlhub:latest-rhel9 .
```

Both Dockerfiles assume `vendor/` is present in the build context —
run `go mod vendor` first (or use `self-hosted/scripts/ship-local.sh`
which automates it).

## Overriding the base image (corporate environments)

Both files parameterize all three stage bases as `ARG` so internal
hardened images can replace any combination. The default values
point at public registry images; `--build-arg` swaps them.

### Override one stage (most common — runtime only)

Corporate env mandates an internal hardened RHEL9 image:

```bash
docker build -f Dockerfile.rhel9 \
  --build-arg RUNTIME_BASE=registry.corp.example.com/hardened/ubi9-minimal:9.4-1234 \
  -t bzlhub:corp-rhel9 .
```

### Override all three (air-gapped or strict allowlist)

When the build host can't reach docker.io at all:

```bash
docker build -f Dockerfile.rhel9 \
  --build-arg UI_BUILDER_BASE=registry.corp.example.com/blessed/node:22-bookworm \
  --build-arg GO_BUILDER_BASE=registry.corp.example.com/blessed/golang:1.26 \
  --build-arg RUNTIME_BASE=registry.corp.example.com/hardened/ubi9-minimal:9.4-1234 \
  -t bzlhub:corp-rhel9 .
```

### Build args available in both Dockerfiles

| ARG | Default (alpine) | Default (rhel9) | Stage |
|---|---|---|---|
| `UI_BUILDER_BASE` | `node:22-alpine` | `node:22-alpine` | UI build |
| `GO_BUILDER_BASE` | `golang:1.26-alpine` | `golang:1.26-alpine` | Go build |
| `RUNTIME_BASE` | `alpine:3` | `registry.access.redhat.com/ubi9/ubi-minimal:latest` | Runtime |
| `VERSION` | `dev` | `dev` | `bzlhub --version` string |
| `COMMIT` | `unknown` | `unknown` | injected into `/api/version` |
| `BUILT_AT` | `unknown` | `unknown` | injected into `/api/version` |

## Override constraints

The override base image must satisfy the stage's requirements:

- **`UI_BUILDER_BASE`**: Node 20+ with `corepack` enabled. The
  build invokes `pnpm` via corepack — the image must allow that or
  ship pnpm 10.33.0 directly.
- **`GO_BUILDER_BASE`**: Go 1.26+. CGO is disabled by the build
  args, so no C toolchain is required.
- **`RUNTIME_BASE`**:
  - For `Dockerfile`: must be alpine-family (uses `apk add` to
    install `ca-certificates`, `tzdata`, `wget`, `git`).
  - For `Dockerfile.rhel9`: must have `microdnf` available (UBI9
    minimal default) OR provide a full `dnf`; the install line
    can be edited to match. Must be glibc-family.

If your corporate base image ships bzlhub's runtime deps
pre-installed, the `RUN microdnf install ...` line will be
mostly a no-op — but it's still required for the user/group
creation. Edit if needed.

## Why two files instead of one

We considered a single Dockerfile with a `BASE_FAMILY=alpine|rhel9`
build-arg that branches the install line. Rejected because:

- Conditional `RUN` based on ARG values is awkward (no native
  `if` in Dockerfiles; requires shell-level dispatch).
- Linters and security scanners (Trivy, Snyk, Anchore) want a
  one-Dockerfile-per-target-OS model — distinguishing layers
  across families would confuse them.
- Forking the file is honest: the package-manager invocation IS
  different; pretending it's one file via shell tricks hides
  that.

Two files, one base-image override mechanism each. Symmetric
contract across both.

## Image size comparison

Approximate sizes for a default build (no overrides):

| Flavor | Final image | Runtime layer |
|---|---|---|
| `Dockerfile` (alpine) | ~50 MiB | ~5 MiB on top of alpine:3 (~7 MiB) |
| `Dockerfile.rhel9` (UBI9-minimal) | ~120 MiB | ~10 MiB on top of ubi9-minimal (~30 MiB compressed) |

UBI9 is bigger but inherits RHEL's certified-CVE-feed posture,
which is the whole point for corp envs that pick it.

## Validating an override built image

After building with overrides, sanity-check:

```bash
docker run --rm bzlhub:corp-rhel9 bzlhub --version
# → bzlhub <version-string>

docker run --rm bzlhub:corp-rhel9 sh -c "which git wget && git --version"
# → /usr/bin/git
# → /usr/bin/wget
# → git version 2.x.x

# Verify the binary runs as the expected non-root UID
docker run --rm bzlhub:corp-rhel9 id
# → uid=65532(bzlhub) gid=65532(bzlhub) groups=65532(bzlhub)
```

If `git` is missing from the override image, the admit pipeline
will crash on the first `git push` — see Plan 76 §"Sharp edges"
for the failure mode. Add `git` to the corp base image OR keep
the `microdnf install git` line in `Dockerfile.rhel9` and let it
no-op on already-present packages.

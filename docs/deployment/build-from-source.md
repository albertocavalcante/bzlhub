# Building canopy from source

> **Status**: stable.
> **Audience**: operators who build the canopy container image
> themselves rather than pulling a pre-built one.

For many enterprise deployments, build-from-source + push-to-your-own-
registry is the default path, not a fallback. Reasons:

- Air-gapped clusters with no internet access from the cluster network.
- Corporate policies that require vetting third-party images.
- Pull-through registry proxies that need to host the image themselves.
- Local development against unmerged patches.
- Reproducibility verification (build it yourself, compare digests).

This doc walks through the build, push, and consume cycle for canopy.
The chart at `deploy/helm/canopy/` is designed to consume any
canopy-compatible image regardless of where it came from — see
[`k8s-parity.md`](./k8s-parity.md) §2 for the contract.

---

## 1. Prerequisites

- An OCI-compatible image builder: Docker (with BuildKit), Podman, or
  Buildah. BuildKit is recommended for multi-stage builds.
- Go 1.26+ on the build host (only if you intend to run `go mod
  vendor` outside the container — see §2).
- pnpm 10.x for the UI build stage — handled inside the container by
  corepack, but a host install is useful for UI dev work.
- A target registry you can push to (any OCI-Distribution-compliant
  registry).

The Dockerfile produces `linux/arm64` images by default (Hetzner CAX
servers are Ampere Altra). For other architectures, see §3.

---

## 2. Vendor dependencies (required before any image build)

canopy intentionally builds with `-mod=vendor` so the image build has
no network or auth requirements at Go compile time. The source tree
MUST have a populated `vendor/` directory before `docker build`.

```bash
cd ~/path/to/canopy
go mod vendor
```

The Dockerfile fails fast if `vendor/` is missing:

```
ERROR: canopy/vendor/ missing — run 'go mod vendor' first
```

If your network policy doesn't allow `go mod vendor` from the build
host either, vendor on a connected machine and `rsync` the tree, or
run `go mod vendor` through your corporate proxy (see
`docs/plans/17-upstream-proxy.md` for the runtime equivalent — same
proxy works for `go mod`).

---

## 3. Build the image

### A. Plain `docker build` (simplest)

```bash
docker build \
  --tag myregistry.corp/canopy:v0.1.0 \
  --build-arg VERSION=v0.1.0 \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILT_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  .
```

Default platform: `linux/arm64`. For x86_64:

```bash
docker buildx build --platform linux/amd64 ...
```

For both architectures in one push:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --tag myregistry.corp/canopy:v0.1.0 \
  --push \
  .
```

### B. Dagger (reproducible, cache-friendly)

If you're already using the daggerverse modules, the
`docker-multistage` module wraps `docker build` with consistent caching
and SBOM generation:

```bash
dagger -m github.com/albertocavalcante/daggerverse/docker-multistage \
  call build \
  --source=. \
  --tag=myregistry.corp/canopy:v0.1.0 \
  --build-args VERSION=v0.1.0,COMMIT=$(git rev-parse --short HEAD)
```

Dagger gives reproducible context (deterministic file ordering, no
host-state bleed) which matters for compliance audits.

### C. Plain Go binary (no container)

For diagnostic builds or embedding the binary into a custom image:

```bash
# Build the UI bundle first — go:embed needs it at internal/embed/ui/.
( cd ui && pnpm install --frozen-lockfile && pnpm run build )
cp -R ui/build/* internal/embed/ui/

# Then the Go binary.
go build \
  -trimpath -mod=vendor \
  -ldflags="-s -w \
    -X github.com/albertocavalcante/canopy/internal/version.Version=v0.1.0 \
    -X github.com/albertocavalcante/canopy/internal/version.Commit=$(git rev-parse --short HEAD) \
    -X github.com/albertocavalcante/canopy/internal/version.BuiltAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o ./canopy \
  ./cmd/canopy
```

Without the UI overlay, the binary serves a "UI not built" stub from
the embedded fallback.

---

## 4. Push to your registry

```bash
docker push myregistry.corp/canopy:v0.1.0
```

Common registry-specific auth:

| Registry | Auth |
|---|---|
| Docker Hub | `docker login` (standard) |
| GitHub Container Registry | `echo $PAT | docker login ghcr.io -u <user> --password-stdin` |
| Harbor / Quay (self-hosted) | `docker login <hostname>` |
| AWS ECR | `aws ecr get-login-password --region <r> \| docker login --username AWS --password-stdin <id>.dkr.ecr.<r>.amazonaws.com` |
| Azure ACR | `az acr login --name <registry>` |
| Google Artifact Registry | `gcloud auth configure-docker <region>-docker.pkg.dev` |
| Artifactory Docker | `docker login <artifactory-host>` |
| GitLab Container Registry | `docker login registry.gitlab.com` (or self-hosted GitLab equivalent) |

If your registry requires mTLS or trust in a private CA, configure
your container engine's per-registry CA bundle before pushing:

- Docker: `/etc/docker/certs.d/<registry>/ca.crt` + restart daemon
- Podman: `/etc/containers/certs.d/<registry>/ca.crt`
- Buildah: same as Podman

---

## 5. Reference in the Helm chart

Override the chart's default image to point at your build:

```bash
helm install bzlhub ./deploy/helm/canopy \
  --set image.repository=myregistry.corp/canopy \
  --set image.tag=v0.1.0 \
  --set image.pullPolicy=IfNotPresent
```

For private registries that require pull auth:

```bash
# Create the pull secret in the target namespace.
kubectl create namespace bzlhub
kubectl create secret docker-registry myreg-creds \
  --docker-server=myregistry.corp \
  --docker-username=<user> \
  --docker-password=<password> \
  --namespace=bzlhub

# Install, referencing the secret.
helm install bzlhub ./deploy/helm/canopy \
  --namespace=bzlhub \
  --set image.repository=myregistry.corp/canopy \
  --set image.tag=v0.1.0 \
  --set image.pullSecrets[0].name=myreg-creds
```

For per-namespace pull config (avoids repeating `pullSecrets` in
every chart):

```bash
kubectl patch serviceaccount canopy \
  --patch '{"imagePullSecrets": [{"name": "myreg-creds"}]}' \
  -n bzlhub
```

For cloud-IAM-backed pulls (ECR, GAR, ACR), prefer node-level IAM
configuration over `pullSecrets`:

- EKS: attach an IAM role with `AmazonEC2ContainerRegistryReadOnly` to
  the node group; no `pullSecrets` needed.
- GKE: nodes inherit Workload Identity; no `pullSecrets` needed.
- AKS: AKS-managed identity with `AcrPull` role; no `pullSecrets`
  needed.

---

## 6. Local development with kind / k3d / minikube

For iteration without a registry, load the locally-built image directly
into the cluster:

```bash
# kind
docker build -t canopy:dev .
kind load docker-image canopy:dev

# k3d
docker build -t canopy:dev .
k3d image import canopy:dev

# minikube
docker build -t canopy:dev .
minikube image load canopy:dev
```

Install with `pullPolicy: Never` so K8s doesn't try to fetch from a
registry:

```bash
helm install bzlhub ./deploy/helm/canopy \
  --set image.repository=canopy \
  --set image.tag=dev \
  --set image.pullPolicy=Never
```

This is the recommended workflow for K8s parity smoke tests during
development.

---

## 7. Pull-through registry caches

A common enterprise pattern: Harbor (or similar) acts as a
pull-through proxy for public registries. Instead of pulling from
`ghcr.io` directly, the cluster pulls from
`harbor.corp/proxy-ghcr/albertocavalcante/canopy` and Harbor caches
transparently.

This requires zero canopy-side changes — just override
`image.repository`:

```bash
helm install bzlhub ./deploy/helm/canopy \
  --set image.repository=harbor.corp/proxy-ghcr/albertocavalcante/canopy
```

If Harbor needs explicit project creation to start caching a given
public image, that's a Harbor admin task (the "project as proxy
cache" feature in Harbor's UI/API). Outside canopy's scope.

---

## 8. Reproducible builds

The Dockerfile is designed to be reproducible given the same source
tree:

- `vendor/` ensures zero network resolution during the Go build stage.
- `-trimpath -mod=vendor` strips host-specific paths from the binary.
- `CGO_ENABLED=0` means no system libc variation between build hosts.
- `alpine:3` is the only floating reference; pin to a specific digest
  (`alpine:3@sha256:...`) for byte-for-byte reproducibility across
  alpine point releases.

For supply-chain attestation:

```bash
# Generate an SBOM
syft myregistry.corp/canopy:v0.1.0 -o spdx-json > canopy-v0.1.0.sbom.json

# Sign the image with cosign (keyless OIDC)
cosign sign myregistry.corp/canopy:v0.1.0

# Verify a signed image before deploy
cosign verify myregistry.corp/canopy:v0.1.0 \
  --certificate-identity-regexp=https://github.com/.*/canopy/ \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com
```

cosign signatures live as additional manifests in the same registry,
under the same repository. No separate signature store required.

---

## 9. Verification

After build + push + helm install:

```bash
# Version check from a one-shot pod
kubectl run canopy-version --rm -it --image=myregistry.corp/canopy:v0.1.0 \
  --restart=Never -- /usr/local/bin/canopy --version

# Version check inside the running StatefulSet pod
kubectl exec -n bzlhub canopy-0 -- /usr/local/bin/canopy --version

# Healthz from inside the pod
kubectl exec -n bzlhub canopy-0 -- wget -q -O- http://127.0.0.1:8090/healthz

# Healthz from outside via Service port-forward
kubectl port-forward -n bzlhub svc/canopy 8090:80
curl http://localhost:8090/healthz
```

The `/version` API endpoint (if exposed; audit per
[`k8s-parity.md`](./k8s-parity.md) §11) returns the ldflags-injected
VERSION / COMMIT / BUILT_AT.

---

## 10. Air-gap workflow

If your build environment is itself air-gapped:

1. **Mirror the dependencies** into your internal artifact repository:
   - Go modules: vendor on a connected machine, transfer the
     `vendor/` tree alongside the source.
   - pnpm packages: `pnpm install` against an internal npm mirror
     (Verdaccio / Artifactory npm).
   - Alpine packages (`ca-certificates`, `tzdata`, `wget`): mirror via
     your internal apk repository, point alpine's `/etc/apk/repositories`
     at it during build.
2. **Pin the alpine base** via SHA digest to whatever's in your
   approved-images registry.
3. **Layer the dependency mirror config** into a "builder base" image
   that the canopy Dockerfile's `FROM` lines reference.
4. **Build inside the air-gap** using the builder base.
5. **Push** to your internal registry.
6. **Reference from the chart** with `image.repository` pointing
   internal.

A worked example of an air-gap-ready Dockerfile diff is out of scope
here; the principle is "all `FROM` and `RUN apk add` operations
resolve from internal mirrors only".

See [`docs/plans/17-upstream-proxy.md`](../plans/17-upstream-proxy.md)
for the related runtime concern: outbound fetches from canopy at run
time may also need to go through your corporate proxy (potentially
with Kerberos auth).

---

## 11. Common gotchas

| Symptom | Likely cause | Fix |
|---|---|---|
| `ERROR: canopy/vendor/ missing` during build | `go mod vendor` wasn't run | Run `go mod vendor` in the source tree before `docker build` |
| `ERROR: internal/embed/ui/index.html missing` during build | ui-builder stage failed silently OR vendored a stub | Check pnpm output in the ui-builder stage; the build fails fast on this |
| `exec format error` when the pod starts | Image was built for arm64, cluster is amd64 (or vice versa) | Rebuild with `--platform linux/amd64` or use buildx multi-arch |
| `unauthorized: authentication required` on `docker push` | Registry login expired or wrong | Re-run the registry-specific login from §4 |
| `ImagePullBackOff` in cluster | `image.pullSecrets` not configured, or secret in wrong namespace | Check `kubectl describe pod canopy-0` for the exact error; create the secret in the StatefulSet's namespace |
| `x509: certificate signed by unknown authority` on pull | Cluster node doesn't trust the registry's CA | Add the registry CA to the node's container runtime trust store (containerd `certs.d`, Docker `certs.d`) |
| `manifest unknown` on pull | Tag doesn't exist at the registry, or wrong repository name | `docker pull` from a workstation to reproduce |
| Pod runs as UID 0 unexpectedly | `securityContext` override removed `runAsUser` | Re-apply the chart defaults; do not unset `securityContext.runAsUser` |

---

## 12. Adjacent concerns

- **Build through a corporate proxy at runtime**:
  [`docs/plans/17-upstream-proxy.md`](../plans/17-upstream-proxy.md)
- **K8s parity contract** the built image must honour:
  [`k8s-parity.md`](./k8s-parity.md)
- **bzlhub.com-specific deployment**: `self-hosted/canopy/DEPLOY-BZLHUB.md`
  (internal; not in this repo's public sync)

---

## 13. Change log

| Date | Change |
|---|---|
| 2026-05-28 | Initial doc. Vendor requirement, three build modes (docker / dagger / plain-go), six registry auth examples, kind/k3d/minikube paths, air-gap workflow, gotcha table. |

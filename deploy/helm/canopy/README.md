# canopy Helm chart

A Helm chart for deploying [canopy](https://github.com/albertocavalcante/canopy)
— the Bazel-first self-hosted module registry — onto any Kubernetes
cluster.

## Status & scope

- **Single-replica**, single-writer. canopy uses SQLite + a local
  filesystem mirror; the chart pins `replicas: 1` as a hard invariant.
  If you need HA, that's not solvable at the chart layer.
- **Ships an Ingress** (core `networking.k8s.io/v1`) — works with
  nginx-ingress, traefik, contour, istio-gateway, alb-ingress,
  gke-ingress, and anything else that honors the core API. CRD-specific
  resources (Traefik `IngressRoute`, Contour `HTTPProxy`, Istio
  `VirtualService`) are out of scope; wrap externally if needed.
- **Optional Gateway API HTTPRoute** for clusters on Cilium, Envoy
  Gateway, modern Istio, etc.
- **Optional CronJobs** for periodic mirror refresh + drift report.
  Implemented via `kubectl exec` into the running pod (sidesteps RWO
  PVC contention).

## Install paths

The chart's image references are registry-agnostic. Three scenarios
cover every realistic deployment. **The OCI publish location below is
illustrative** — the chart isn't published yet; for now, install from
the local path.

### 1) Local checkout (works today)

```sh
git clone https://github.com/albertocavalcante/canopy
helm install canopy ./canopy/deploy/helm/canopy
```

### 2) Public registry (after publish)

```sh
helm install canopy oci://ghcr.io/albertocavalcante/charts/canopy --version 0.1.0
```

### 3) Corporate OCI mirror

You've mirrored the image (and optionally the chart) into your internal
registry. Use a values file rather than `--set` because `--set` syntax
for nested lists doesn't survive every shell:

```yaml
# values-prod.yaml
image:
  registry: registry.corp.internal
  repository: infra/canopy
  digest: sha256:<pinned-digest>
  pullSecrets:
    - name: corp-registry-creds
```

```sh
helm install canopy ./canopy/deploy/helm/canopy -f values-prod.yaml
```

The `Secret` `corp-registry-creds` must exist in the target namespace
before install — create it out-of-band (`kubectl create secret
docker-registry`, sealed-secrets, external-secrets, SOPS, …). The chart
never templates a Secret from values.

For umbrella charts / ArgoCD users who set `global.imageRegistry`
universally, that override is honored too.

### 4) Local development (kind / minikube)

```sh
# Build the image locally. CONTAINER defaults to docker; export
# CONTAINER=podman to use podman instead.
${CONTAINER:-docker} build -t canopy:dev /path/to/canopy

# Make it visible to the cluster.
kind load docker-image canopy:dev
# or: minikube image load canopy:dev

helm install canopy /path/to/canopy/deploy/helm/canopy \
  --set image.registry="" \
  --set image.repository=canopy \
  --set image.tag=dev \
  --set image.pullPolicy=Never
```

`image.registry=""` produces a bare `canopy:dev` reference with no
registry prefix, which is what `kind`/`minikube` need for sideloaded
images.

## Building the image

The chart consumes any OCI-compliant canopy image. Build it with either
Docker or Podman — the `Dockerfile` deliberately avoids BuildKit-only
features:

```sh
docker build -t canopy:dev .
podman build -t canopy:dev .
```

For multi-arch publish:

```sh
# Docker buildx
docker buildx build --platform linux/arm64,linux/amd64 -t ghcr.io/you/canopy:0.1.0 --push .

# Podman + manifest
podman build --platform linux/arm64,linux/amd64 --manifest ghcr.io/you/canopy:0.1.0 .
podman manifest push ghcr.io/you/canopy:0.1.0
```

### Run the chart's manifests under Podman without a cluster

Podman natively consumes Kubernetes YAML. The chart's rendered
manifests `podman kube play` directly:

```sh
helm template canopy ./deploy/helm/canopy --set persistence.mirror.enabled=false --set persistence.index.enabled=false > /tmp/canopy.yaml
podman kube play /tmp/canopy.yaml
```

Useful for laptop iteration without a kind cluster.

## Ingress + SSE

canopy serves Server-Sent Events on `/api/events`. nginx-ingress
buffers responses by default, which breaks SSE. Add these annotations
when enabling Ingress with the nginx controller:

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
```

For Traefik / Contour / Istio / ALB, consult their respective
documentation for the equivalent setting. The chart's `Ingress`
resource passes annotations through verbatim — it doesn't pre-render
any controller-specific keys.

## CronJobs

Optional CronJobs hit canopy's HTTP API from inside the cluster
network — `POST /api/ingest-recursive` for periodic mirror refresh,
`GET /api/drift` for daily drift reports. Both reuse the canopy image
itself (which already ships `wget` for the HEALTHCHECK), so there is
no extra image to mirror, audit, or keep current.

This avoids three traps of the more obvious `kubectl exec` design:
no SQLite contention from a second process opening the index, no
`pods/exec` RBAC blast radius, and no second image dependency.

**Security implication.** The ingest endpoint is gated by a feature
flag — `CANOPY_INGEST_WRITE_ENABLED=true`. The chart **refuses to
render** the ingest CronJob unless you set this flag on the running
canopy server. Once it's on, any HTTP client able to reach the canopy
Service can trigger ingests. Inside the cluster that's fine; **do not
expose canopy via Ingress in this mode** until canopy ships auth (and
the chart README will say so when it does).

Example values for a weekly recursive ingest + daily drift:

```yaml
env:
  CANOPY_INGEST_WRITE_ENABLED: "true"   # required for cronjobs.ingest

cronjobs:
  ingest:
    enabled: true
    schedule: "0 3 * * 0"
    upstream: https://bcr.bazel.build
    modules:
      - bazel_skylib@1.7.1
      - rules_go@0.50.1
  drift:
    enabled: true
    schedule: "30 6 * * *"
    upstream: https://bcr.bazel.build
```

## Values

See [`values.yaml`](./values.yaml) for the full surface — every key is
commented inline.

## License

MIT. See the canopy repo `LICENSE`.

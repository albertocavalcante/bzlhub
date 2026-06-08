# Deployment parity — bzlhub on docker-compose and Kubernetes

> **Status**: stable contract; implementation audits pending — see §11.
> **Audience**: operators deploying bzlhub; bzlhub maintainers.
> **Authority**: this is the contract. The compose stack at
> `self-hosted/bzlhub/` and the Helm chart at `deploy/helm/bzlhub/` are
> consumers of it. When a feature needs to break a rule here, change this
> document in the same PR — don't drift.

This doc specifies what the bzlhub binary expects from its environment
so that the same image and the same operational decisions work in both
deployment shapes:

- **docker-compose** on a single VPS host (the public `bzlhub.com`
  demo + private deployments on the existing `shared` Hetzner box).
- **Kubernetes** — any conformant cluster, via the Helm chart.

The contract is deliberately small. The defaults are deliberately
minimal. Everything optional is opt-in.

---

## 1. Architectural model

Bzlhub is a **stateless-tolerable cache + search index** over upstream
sources of module truth.

```
   ┌──────────────────────────────────────────┐
   │  Sources of truth (upstream)              │
   │   • BCR HTTP  (shipped)                   │
   │   • Federated BCR-shape registries        │
   │   • Git repos with BCR layout  (planned)  │
   │   • Artifactory generic repos  (planned)  │
   └──────────────┬───────────────────────────┘
                  │ federation / pull-through
                  ▼
   ┌──────────────────────────────────────────┐
   │  bzlhub / bzlhub                          │
   │   • mirror/   (warm cache, derived)       │
   │   • index/    (SQLite + FTS5, derived)    │
   └──────────────┬───────────────────────────┘
                  │
                  ▼
              Bazel users + UI + MCP
```

Bzlhub is **not** the source of truth for any module. Upstream is.
The on-disk `mirror/` and `index/` are derived state, recoverable by
re-fetching from upstreams.

**Operational consequence**: a deployment with empty volumes is a
valid state. Boot, serve degraded-but-available, rehydrate in the
background. The instance is repaveable; the upstreams are the durable
truth.

---

## 2. Deployment targets

| Target | Form | Status |
|---|---|---|
| docker-compose on a single host | `self-hosted/bzlhub/compose.yml` | shipped |
| Kubernetes | `deploy/helm/bzlhub/` | chart present; cluster smoke-test pending |

Both forms MUST honour the contract in §3.

### Image sourcing

The chart's default `image.repository` points at the upstream-published
image, but the **expected enterprise path is build-from-source** —
clone the repo, `docker build`, push to your own registry (Harbor,
Artifactory, ECR, ACR, GCR, GitLab, internal Quay, or anything else
that speaks the OCI Distribution spec), reference from the chart with
`--set image.repository=…`. See
[`build-from-source.md`](./build-from-source.md) for the full BYO-image
workflow, including air-gap, private CAs, and pull-through caches.

A third image-sourcing path exists for single-host VPS deployments:
`self-hosted/scripts/ship-local.sh` does `docker save | ssh | docker
load`, bypassing a registry entirely. This is what the bzlhub.com
demo uses.

The chart MUST work with all three sourcing paths without
modifications — `image.repository`, `image.tag`, `image.pullPolicy`,
and `image.pullSecrets` are the only knobs that should ever need to
differ between deployments.

---

## 3. Required application behaviour (the contract)

Bzlhub MUST exhibit these properties. They are not optional. Breaking
any of them is a breaking change and requires a corresponding edit to
this document.

The terms MUST / SHOULD / MAY follow RFC 2119.

### 3.1 Cold-start without state

Bzlhub MUST boot and start serving traffic with empty `mirror/` and
empty `index/`. It MUST NOT crash, refuse all traffic, or require an
out-of-band ingest step before answering requests.

When booted with empty state:

- `/healthz` returns 200 as soon as the process is up. *(Verified)*
- `/readyz` SHOULD return 200 once the application loop is running.
  *(Not implemented today — `/readyz` does not exist; both K8s probes
  target `/healthz`. See §3.7.)*
- BCR endpoints serve pull-through responses from configured upstreams
  (§3.5). *(Verified — federation cascade)*
- The Web UI rendering on an empty index is audit-pending; an explicit
  "rehydrating, N/M modules" banner is a contract goal not yet
  implemented (depends on §3.2).
- Cache misses on the BCR surface MAY write to `mirror/`, gated by
  `CANOPY_PROMOTE_ON_SERVE=true` (default: off — "curated, not
  greedy"). When off, pull-through serves the response from upstream
  without persisting. *(Verified — `serve.go:176`)*

Edge case — empty upstream list (`CANOPY_UPSTREAMS` unset):

- BCR endpoints return `404` for any module not already in `mirror/`;
  no upstream lookup is attempted. *(Verified — cascade.go:156 "no-op
  wrapper")*
- The UI on a fully-empty deployment (no upstreams, no mirror) is
  audit-pending.

### 3.2 Self-rehydration on cold start

> **Implementation status (2026-05-28 audit)**: this section describes
> a CONTRACT GOAL. The auto-rehydration loop, strategy selector, and
> progress UX described below are NOT implemented today. Current
> behaviour: cold-start with empty volumes serves pull-through via
> the federation cascade only; the index stays empty unless the
> operator runs `bzlhub ingest` manually or enables the
> `cronjobs.ingest` Helm addon.

When the index is detected to be empty at boot AND `upstreams` is
non-empty, bzlhub SHOULD initiate a background rehydration loop. The
strategy is selectable via configuration; default is **hybrid**
(pull-through serves traffic from second 1, background loop fills the
mirror and rebuilds the index opportunistically).

When `upstreams` is empty, rehydration is a no-op regardless of
strategy. The empty state is preserved until an upstream is configured
and bzlhub is restarted or sent an explicit rehydration trigger.

### Today's operational alternative

Until self-rehydration ships, the equivalent is:

- For one-shot warm: run `bzlhub ingest --recursive --upstream
  https://bcr.bazel.build` on the host (or `kubectl exec` into the
  pod for K8s).
- For ongoing refresh: enable `cronjobs.ingest.enabled: true` in
  `values.yaml` with a sensible schedule and `modules` list (or
  recursive against a known closure root).

The CronJob addon predates the auto-rehydration design; once §3.2 is
implemented, the CronJob remains useful for scheduled deep refresh
(e.g. catching upstream yanks) while cold-start handles initial warm.

| Strategy | When traffic starts being served | When full warm |
|---|---|---|
| `eager` | After background ingest completes | Bounded but slow |
| `pullthrough` | Immediately; per-module on first request | Eventually warm, never fully if some modules never get requested |
| `hybrid` *(default)* | Immediately + background loop indexing all upstreams | Eventually warm, parallel to traffic |

### 3.3 No required external services

Bzlhub MUST NOT require, at install time, run time, or restore time:

- An object storage bucket (S3, R2, GCS, Azure Blob).
- An external database. The index is local SQLite via
  `modernc.org/sqlite` — no cgo, no Postgres.
- An external message broker, queue, or coordination service.
- A sidecar container.

It MAY *use* any of these as an optional addon (§6), but a fresh
default install must not depend on any of them.

### 3.4 Configuration surface

All configuration MUST be expressible through environment variables.
Secrets MUST also be readable from files via an `*_FILE` env-var
indirection (e.g. `CANOPY_FOO_FILE=/run/secrets/foo`). The plain-value
env var (`CANOPY_FOO`) MAY be supported for non-secret config and
local development. Rationale: secrets that exist only as env vars leak
into `docker inspect`, `/proc/<pid>/environ`, crash dumps, and child
processes. File-mounted secrets do not.

Bzlhub MUST NOT require a configuration file as the primary surface.
A file is acceptable as an alternative, but env vars are the contract.

### 3.5 Pluggable upstreams

The upstream backend interface MUST accept zero or more upstream
declarations, each tagged with a `type`. Initial set:

- `bcr` — BCR HTTP at a URL (default upstream is the public BCR).
- `git` — *(planned)* git repository serving BCR layout, with auth.
- `artifactory` — *(planned)* Artifactory generic repo holding modules.

`upstreams: []` is a valid configuration. It means bzlhub serves only
what is already in `mirror/`; pull-through is a no-op. This is useful
for fully-air-gapped staging where the operator pre-loads the mirror
some other way.

### 3.6 Pluggable snapshot store (addon)

Bzlhub MAY support exporting/restoring its index+mirror as a snapshot
artifact to/from a pluggable store. The snapshot interface MUST NOT
assume S3/R2. First backends:

- `http` — HTTP PUT/GET against an arbitrary URL, with optional auth.
- `artifactory` — Artifactory generic repo, treated as HTTP with
  Artifactory's specific path scheme.
- `s3` — only when the operator has S3-compatible storage available.

When the snapshot addon is **implemented AND enabled** AND volumes are
empty at boot, bzlhub MUST attempt a snapshot restore before falling
back to §3.2 rehydration. If snapshot restore fails (network error,
404, checksum mismatch), bzlhub MUST log the failure and fall back to
§3.2 — never crash on a snapshot miss.

### 3.7 Health and readiness

Bzlhub MUST expose:

| Endpoint | Semantics | Status |
|---|---|---|
| `GET /healthz` | 200 when the process is live. Never blocks on dependencies. | ✅ Implemented — `internal/server/server.go:217` |
| `GET /readyz` | 200 when the application loop is running. Does NOT block on rehydration. | ✅ Implemented 2026-05-28 — mirrors `/healthz` today; semantic divergence available for future tightening (e.g. block readiness during rehydration when §3.2 ships) |

A "still rehydrating" state is healthy AND ready.

Both endpoints return identical responses today (`200 "ok"`). The
distinction exists so operators can wire stricter readiness checks
later (e.g. block readiness during rehydration when §3.2 ships)
without changing the liveness contract — restarting on liveness
failure is destructive; failing readiness merely takes the pod out
of the Service rotation.

The Helm `readinessProbe` targets `/readyz`; `livenessProbe` and
the Dockerfile `HEALTHCHECK` target `/healthz`.

### 3.8 Single replica

Bzlhub is single-replica only. The Helm chart MUST deploy a
`StatefulSet` with `replicas: 1`. Horizontal scaling requires a shared
storage backend and is explicitly out of scope; the design is a
single-binary registry process per deployment.

Operational consequence: PVC `accessModes: [ReadWriteOnce]` is
acceptable (and is the cluster default in most managed K8s offerings).
However, RWO blocks pod rescheduling across nodes; if the underlying
node fails, the new pod cannot bind the PVC until the old node detaches
it. Operators sensitive to rescheduling time SHOULD use `ReadWriteOncePod`
(K8s 1.27+) or a storage class with multi-node attach + detach.

### 3.9 Security context

Bzlhub MUST run as a non-root UID. No part of the deployment requires
root inside the container.

The current `Dockerfile` runs as UID/GID `65532:65532` (the de-facto
`nonroot` convention used by distroless), with data dirs owned
`65532:0` (group root) and mode `g+rwX`. This explicitly supports
three identity models simultaneously:

- Vanilla Kubernetes with `podSecurityContext.runAsUser: 65532` and
  `fsGroup: 65532`.
- OpenShift SCC, which assigns a random UID per namespace and always
  runs containers with supplementary GID 0 — the `g+rwX` on
  data dirs is the OpenShift contract.
- Rootless Podman, which maps host UID into the container.

The group-writable bit on data dirs is NOT a security relaxation: GID
0 inside an unprivileged container is unrelated to host root.

- The container image MUST set `USER` to a non-zero numeric UID
  (recommended: `65532:65532`, the `nonroot` UID used by distroless).
- The Helm chart MUST set:
  ```yaml
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    capabilities: { drop: ["ALL"] }
  ```
- The container MUST tolerate `readOnlyRootFilesystem: true`. Writable
  state lives only in the mounted `mirror/`, `index/`, and a `/tmp`
  `emptyDir`.

### 3.10 TLS termination at the edge

Bzlhub MUST serve plain HTTP on a single port. TLS termination is
delegated to the edge layer (Caddy on a VPS host, an Ingress / Gateway
controller, or Cloudflare Tunnel).

For client identity, bzlhub honours `X-Forwarded-User`,
`X-Forwarded-Email`, and `X-Forwarded-Groups` from clients whose
source IP falls within a configured trust list.

**The trust gate is `CANOPY_TRUSTED_PROXY_CIDR`** — a comma-separated
list of CIDR blocks. Identity headers are honoured ONLY for requests
arriving from one of those CIDRs. Requests from anywhere else have
their `X-Forwarded-*` headers ignored. *(Verified —
`internal/server/auth_middleware.go`)*

This means the actual security model is stronger than "trust whatever
you see":

- The operator configures the list of trusted-source-IP CIDRs (e.g.
  the ingress controller's pod CIDR, or `127.0.0.1/32` for a
  same-host reverse proxy).
- Forged headers from any other source are silently dropped.
- A request reaching bzlhub directly from an untrusted source IP gets
  treated as anonymous, regardless of what headers it sets.

Defence-in-depth recommendations:

- On Kubernetes: a `NetworkPolicy` restricting ingress to the auth
  proxy / ingress controller pods. The CIDR trust list AND the
  network policy enforce the same boundary — belt and braces.
- On a single-host VPS: bzlhub binds to `127.0.0.1` (compose service
  port binding) AND `CANOPY_TRUSTED_PROXY_CIDR=127.0.0.1/32`.

Note: bzlhub deliberately does NOT honour `X-Forwarded-For` for
client-IP determination — only the source IP of the connection itself
is used for trust evaluation. This avoids the classic "trusted proxy
forwards a forged XFF" exploit.

For unauthenticated public deployments (bzlhub as a read-only mirror),
no trusted-edge configuration is needed; write endpoints are gated by
`CANOPY_INGEST_WRITE_ENABLED` (default: off).

---

## 4. Storage

### 4.1 What lives in each volume

| Volume | Contents | Default PVC size | Growth |
|---|---|---|---|
| `mirror/` | BCR-shape file tree: `modules/`, `blobs/`, JSON metadata | 10 GiB | Linear with corpus; full public BCR is currently a few GB (estimate, not measured). |
| `index/` | SQLite database (`bzlhub.db`) with module metadata, FTS5 search index, dependency edges | 1 GiB | Sub-linear with corpus; typically <1 GB even for full BCR (estimate). |

Sizes are starting points; operators should monitor and resize PVCs
as the corpus grows. Actual values will be measured on the bzlhub.com
demo instance and back-fed here.

`mirror/` and `index/` MUST be separate volume mounts. This lets
future addons address them independently (e.g. Litestream replicating
only `index/`, snapshot publisher tarballing both separately).

### 4.2 Persistence modes

| Mode | Behaviour | When to use |
|---|---|---|
| `persistence.enabled: true` *(default)* | PVC (K8s) or named volume (compose) backs `mirror/` and `index/`. Warm cache survives pod or container restart. | Standard deployments. |
| `persistence.enabled: false` | `emptyDir` (K8s) or anonymous volume (compose). State is lost on restart; rehydration runs every cold start. | True repaveable mode; dev clusters where PVCs are expensive; conformance test of the §3.1–§3.2 contract. |

### 4.3 What MUST NOT be in the volumes

- Static configuration (use ConfigMap / env vars).
- Secrets (use Secret / file-mounted compose secrets).
- Backup artifacts (a separate concern; the snapshot addon publishes
  *out* of the volume to an external store).

---

## 5. Networking

| Property | Value |
|---|---|
| Listen port | 8090 (default; overridable via `CANOPY_BIND`) |
| Protocol | plain HTTP/1.1 + HTTP/2 cleartext (`h2c`) on the same port |
| Host networking | MUST NOT be required |
| Multiple ports | MUST NOT be required |
| Service shape on K8s | `ClusterIP`; operator wires Ingress / Gateway / Tunnel themselves |

The single-port discipline keeps the Service / Ingress / Caddy block
trivial in both deployment shapes.

---

## 6. Addon policy

Every addon MUST be off by default in `values.yaml`. Enabling an
addon MUST be a single explicit opt-in flip (`addons.<name>.enabled:
true`) plus any addon-specific configuration.

A disabled or absent addon MUST NOT prevent bzlhub from booting or
serving traffic. "Absent" means: a value override that removes the
addon block entirely is equivalent to `addons.<name>.enabled: false`;
the chart never assumes a value block exists. The core app continues
to function with all addons disabled.

Currently planned addons (none enabled in defaults):

| Addon | Purpose | Triggered by |
|---|---|---|
| `snapshot` | Fast cold-start by restoring a pre-built index + mirror tarball from a pluggable store | Cold-start times become user-visible |
| `litestream` | Continuous backup of `index/` to an S3-compatible store | Canonical state lands AND operator has S3-compatible storage |
| `cronjobs.drift` | Scheduled drift detection vs upstream | Operator wants periodic drift reports |
| `cronjobs.ingest` | Scheduled re-ingest from upstreams | Operator wants periodic full refresh |
| `auth` | Trust headers from edge OIDC proxy; gate write endpoints | Any write endpoint exposed to untrusted clients |

---

## 7. Sidecar policy

The default Helm chart MUST render **zero** sidecar containers. A
default pod spec contains the `bzlhub` container and only the `bzlhub`
container.

A sidecar MAY be added to an addon's rendering iff all three hold:

1. The function is not application logic (it is an infrastructure
   concern).
2. The same image and configuration approach works in docker-compose
   without becoming a parallel implementation.
3. The sidecar earns its complexity vs alternatives (host cron, edge
   integration, in-process feature).

Currently approved sidecar use cases (still off in defaults):

| Sidecar | Used by addon | Justification |
|---|---|---|
| Litestream | `addons.litestream` | Continuous SQLite replication is a well-trodden pattern; same image works on compose and K8s. |

Explicitly NOT sidecars:

- **Auth proxies** (oauth2-proxy, Authelia) — edge concern. May be a
  separate Deployment in the same cluster, but NOT a sidecar inside
  the bzlhub pod. The bzlhub pod trusts headers from a configured
  trusted-edge per §3.10.
- **Log forwarders** (Promtail, Vector) — node-level / host-level
  infrastructure.
- **Metrics exporters** — add `/metrics` to bzlhub itself if needed.
- **Backup schedulers** (restic) — scheduled job, not co-resident.

---

## 8. `values.yaml` reference

> **Note (2026-05-28 audit)**: the actual `deploy/helm/bzlhub/values.yaml`
> is RICHER than the reference below — it has `image.registry` /
> `image.repository` / `image.digest` separated, `global.imageRegistry`
> for ArgoCD/Bitnami-style compatibility, per-volume `persistence.{mirror,index}`,
> `fsGroupChangePolicy: OnRootMismatch`, `extraVolumes` / `extraVolumeMounts`
> for corporate CAs, `extraManifests` for NetworkPolicy / PDB, and
> Gateway API alongside Ingress. The shape below is the minimum-viable
> reference; consult the chart's `values.yaml` for the full surface.

The canonical defaults shape (minimum viable; chart's actual
`values.yaml` extends this):

```yaml
image:
  # The chart's default repository is the upstream-published image.
  # Override for any deployment that doesn't pull from a public
  # registry — see docs/deployment/build-from-source.md for the
  # build-and-push-to-your-own-registry path.
  registry: ghcr.io                       # OCI registry hostname
  repository: albertocavalcante/bzlhub    # path within registry
  tag: ""                                 # defaults to chart appVersion
  digest: ""                              # sha256:… pin (takes precedence over tag)
  pullPolicy: IfNotPresent                # use Always for floating tags like :main
  pullSecrets: []                         # operator-supplied Secret names
  # Example for a private registry:
  #   pullSecrets:
  #     - name: myregistry-creds          # kubectl create secret docker-registry ...

global:
  imageRegistry: ""                       # umbrella-chart / ArgoCD override
  imagePullSecrets: []

# Zero upstreams = empty registry. Valid starting state.
upstreams: []
# Examples:
#   - type: bcr
#     url: https://bcr.bazel.build
#   - type: git
#     url: https://git.corp/bazel-modules
#     auth:
#       type: token
#       secretRef: { name: git-token, key: token }
#   - type: artifactory
#     url: https://artifactory.corp/api/storage/bazel-modules-generic
#     auth:
#       type: basic
#       secretRef: { name: artifactory-creds }

persistence:
  enabled: true                  # PVC. Set false for emptyDir (true ephemeral).
  size: 10Gi
  storageClass: ""               # cluster default
  # mirror/ and index/ each get a sub-volumeClaim derived from this

rehydration:
  onColdStart: true              # empty index → background ingest
  strategy: hybrid               # eager | pullthrough | hybrid
  schedule: ""                   # cron expression for periodic refresh; empty = none

addons:
  snapshot:
    enabled: false
    store:
      type: http                 # http | artifactory | s3
      url: ""
      auth: {}
    publishSchedule: "0 4 * * *"

  litestream:
    enabled: false
    replicaUrl: ""
    secretRef: {}

  cronjobs:
    drift:
      enabled: false
      schedule: "0 */6 * * *"
    ingest:
      enabled: false
      schedule: "0 3 * * *"

auth:
  enabled: false                 # writes denied when off; reads always open
  trustedHeader: "X-Forwarded-User"

resources:
  requests: { cpu: 100m, memory: 256Mi }
  limits:   { cpu: 2,    memory: 2Gi }

securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  readOnlyRootFilesystem: true
  capabilities: { drop: ["ALL"] }

service:
  type: ClusterIP
  port: 80                       # Service port
  targetPort: 8090               # Container port (matches CANOPY_BIND default)

ingress:
  enabled: false
  className: ""
  annotations: {}
  hosts: []
  tls: []

httpRoute:                       # Gateway API alternative to Ingress
  enabled: false

podDisruptionBudget:
  enabled: false                 # single-replica; PDB is meaningless

serviceAccount:
  create: true
  annotations: {}
```

---

## 9. Environment variable reference

| Variable | Purpose | Default | Required? |
|---|---|---|---|
Today's actual env vars (verified by reading `cmd/bzlhub/serve.go`):

| Variable | Purpose | Default | Required? |
|---|---|---|---|
| `CANOPY_BIND` | Listen address | `0.0.0.0:8090` | no |
| `CANOPY_ROOT` | Path to the `mirror/` tree | `/var/lib/bzlhub/mirror` | no |
| `CANOPY_DB` | Path to the SQLite index file | `/var/lib/bzlhub/index/bzlhub.db` | no |
| `CANOPY_MIRROR_BASE_URL` | If set, bzlhub advertises itself as a tarball mirror via `bazel_registry.json.mirrors` | unset | no |
| `CANOPY_UPSTREAMS` | Comma-separated list of upstream BCR-shape registry URLs (federation cascade) | unset → no federation | no |
| `CANOPY_UPSTREAM_CACHE_SIZE` | Federation response cache size; negative disables | `1000` | no |
| `CANOPY_UPSTREAM_PROBE_INTERVAL` | Background probe interval for upstream reachability | per code default | no |
| `CANOPY_DISABLE_SHADOW_DETECTION` | Disable the shadow-module detection during federation | unset | no |
| `CANOPY_PROMOTE_ON_SERVE` | If `true`, async-bump every upstream-won path into local mirror (greedy mode) | `false` | no |
| `CANOPY_TRUSTED_PROXY_CIDR` | Comma-separated CIDRs allowed to supply `X-Forwarded-*` identity headers | unset → no headers honoured | when running behind an auth proxy |
| `CANOPY_INGEST_WRITE_ENABLED` | Master switch for write endpoints (e.g. `POST /api/ingest-recursive`) | `false` | when write features are needed |
| `CANOPY_INGEST_ALLOW_CUSTOM_UPSTREAM` | Whether the ingest API may target arbitrary upstreams | `false` | rarely |
| `CANOPY_INGEST_RATE_LIMIT_PER_MIN` | Per-IP rate limit for ingest endpoint | per code default | no |
| `CANOPY_INGEST_MAX_CONCURRENT` | Concurrent-ingest cap | per code default | no |
| `CANOPY_INGEST_RATE_BYPASS_IPS` | IPs exempt from ingest rate-limit | unset | no |

Future / planned env vars (NOT implemented today; documented to
reserve the shape):

| Variable | Purpose | Status |
|---|---|---|
| `CANOPY_UPSTREAMS_<N>_TYPE/URL/AUTH_*` | Per-upstream typed config when git + Artifactory backends land | planned with those backends |
| `CANOPY_REHYDRATION_ON_COLD_START` | Enable §3.2 auto-ingest | planned with §3.2 |
| `CANOPY_REHYDRATION_STRATEGY` | `eager` / `pullthrough` / `hybrid` | planned with §3.2 |
| `CANOPY_SNAPSHOT_*` | Snapshot addon (see §3.6) | planned with §3.6 |
| `CANOPY_MCP_HTTP_ENABLED` and friends | MCP-over-HTTP addon (see plan 18) | planned with plan 18 |
| Feature flags (`CANOPY_FEAT_*`) | Per-feature kill-switches | partially shipped; per-flag naming varies today |

Secrets, when added, get an `*_FILE` companion (e.g.
`CANOPY_FOO_FILE=/run/secrets/foo`) per the §3.4 convention.

Secrets get an `*_FILE` companion (e.g. `CANOPY_UPSTREAMS_0_AUTH_TOKEN_FILE`)
pointing at a file-mounted path.

### Standard env vars honoured

In addition to the bzlhub-specific surface above, bzlhub MUST respect
the following standard environment variables when its central HTTP
client makes outbound requests. These exist on every well-behaved Go
or Linux service; bzlhub does not reinvent or override them.

| Env var | Honoured for | Mechanism |
|---|---|---|
| `HTTP_PROXY` / `http_proxy` | Plain-HTTP outbound proxy | Go's `http.ProxyFromEnvironment` |
| `HTTPS_PROXY` / `https_proxy` | HTTPS outbound proxy | Same |
| `NO_PROXY` / `no_proxy` | Comma-separated bypass list (hosts, `.domain.suffix`, CIDR, `*`) | Same |
| `SSL_CERT_FILE` | Path to a trusted CA bundle for outbound TLS | Standard Go behaviour |
| `SSL_CERT_DIR` | Directory of trusted CA bundles | Same |
| `TZ` | Container time zone (for log timestamps and cron eval) | Standard libc behaviour (`tzdata` is in the runtime image) |

Caveat: these env vars only take effect for outbound HTTP calls that go
through bzlhub's central `*http.Client` constructor. Subsystems that
construct their own `http.Client` literals will silently bypass them.
Eliminating ad-hoc client construction is the prerequisite refactor
named in `docs/plans/17-upstream-proxy.md` §4 (`R1`), and it benefits
bzlhub even outside the proxy use case (consistent timeouts, retries,
observability).

**Design note — array env vars**: the `CANOPY_UPSTREAMS_<N>_*` pattern
is a numbered-index convention; alternatives considered were
`CANOPY_UPSTREAMS=<json>` and a config file. Numbered envs were
chosen because they compose cleanly with K8s ConfigMaps (one key per
variable, easy to template) and Helm `range` over `upstreams[]`,
without needing a JSON serializer in the values pipeline. Operators
wanting fewer env vars can switch to a config file via the
`CANOPY_CONFIG_FILE` env var (still env-driven).

The env-var schema MUST stay backward-compatible within a major
version. Renames require a deprecation cycle (both names honoured for
one minor release with a log warning).

---

## 10. Resource budget guidance

| Workload | Recommended `requests` | Recommended `limits` |
|---|---|---|
| Default (read-mostly, ~few hundred modules indexed) | `cpu: 100m, memory: 256Mi` | `cpu: 2, memory: 2Gi` |
| Full BCR mirror + active ingest | `cpu: 500m, memory: 1Gi` | `cpu: 4, memory: 4Gi` |
| Heavy MCP query traffic (agents) | bump `cpu` limits to 4–8 | bump `memory` limits to 4–8 GiB |

These are guidance, not contract. Operators size to their own load.

The contract is: bzlhub MUST work within the **default** numbers above
for a freshly-deployed empty instance with one BCR upstream. If a
release breaks that, it's a regression.

---

## 11. Implementation status

The contract above describes the target. This section tracks which
items are verified, which are audit-pending, and which are
known-missing.

When an item moves between states, update the row with a date and a
link to the implementing PR.

Updated 2026-05-28 after a direct source audit. Status legend:
✅ verified, ⚠️ partial / different than spec, ❌ not implemented,
📋 known-missing (work tracked elsewhere).

| § | Contract item | Status | Notes |
|---|---|---|---|
| 3.1 | Boots cleanly with empty `mirror/` and empty `index/` | ✅ | `cascade.go:156` — "Upstreams may be empty — no-op wrapper" |
| 3.1 | UI degraded state ("rehydrating, X/Y") | ❌ | UX depends on §3.2 self-rehydration which isn't implemented; today UI likely renders as if registry happens to be empty |
| 3.1 | Cache misses trigger upstream pull-through write to `mirror/` | ⚠️ | Pull-through reads work; the *write* is gated by `CANOPY_PROMOTE_ON_SERVE=true` (off by default — "curated, not greedy") per `serve.go:176` |
| 3.2 | Self-rehydration goroutine triggers on empty index | ❌ | Not implemented; operator runs `bzlhub ingest` manually OR enables `cronjobs.ingest` Helm addon |
| 3.2 | `eager` / `pullthrough` / `hybrid` strategy selector | ❌ | Single mode today (federation pull-through + opt-in promote-on-serve) |
| 3.3 | No required external services | ✅ | No S3/Postgres/broker; SQLite-only |
| 3.4 | All config via env vars | ✅ | Every operational knob has a `CANOPY_*` env var |
| 3.4 | Secrets via `*_FILE` indirection | ❌ | Not yet — no secret-bearing features today; adopt convention before first one ships |
| 3.4 | Upstream env-var schema as documented (`CANOPY_UPSTREAMS_<N>_*`) | ⚠️ | Reality is `CANOPY_UPSTREAMS=url1,url2` flat comma-separated; no per-upstream type. The per-type schema becomes relevant when git/Artifactory backends land |
| 3.5 | `upstreams` empty is a valid configuration | ✅ | `serve.go:67` guards `if len(upstreams) > 0` |
| 3.6 | Pluggable snapshot store | 📋 | Not implemented; design in §3.6 |
| 3.7 | `/healthz` endpoint | ✅ | `internal/server/server.go:217` |
| 3.7 | `/readyz` endpoint | ✅ | Added 2026-05-28; mirrors `/healthz` today, room to harden later (`internal/server/server.go`). Helm default keeps both probes on `/healthz`; operators can switch `readinessProbe.httpGet.path` to `/readyz` when stricter semantics land. |
| 3.8 | Helm chart deploys as `StatefulSet` with `replicas: 1` | ✅ | `deploy/helm/bzlhub/templates/statefulset.yaml:11` |
| 3.9 | Non-root UID in image | ✅ | `Dockerfile` `USER bzlhub` (UID 65532) |
| 3.9 | Helm `securityContext` complete | ✅ | Reality is *richer* than the contract — `runAsNonRoot`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`, plus `fsGroupChangePolicy: OnRootMismatch` to avoid expensive recursive chowns |
| 3.10 | Serves plain HTTP only | ✅ | `CANOPY_BIND=0.0.0.0:8090` in Dockerfile |
| 3.10 | Trusted-edge X-Forwarded-* header handling | ✅ | Reality is *stronger* than the original framing — `CANOPY_TRUSTED_PROXY_CIDR` gates header trust by source IP CIDR. Auth middleware at `internal/server/auth_middleware.go` |
| 4.1 | `mirror/` and `index/` are separate volumes | ✅ | `values.yaml` has `persistence.mirror` and `persistence.index` as separate PVCs |
| 5 | Single port, no host networking | ✅ | `containerPort: 8090`, plain ClusterIP service |
| 6 | All addons off in `values.yaml` defaults | ✅ | `cronjobs.{ingest,drift}.enabled: false`, `ingress.enabled: false`, `gateway.enabled: false` |
| 7 | Zero sidecars in default chart render | ✅ | Only the `bzlhub` container in the StatefulSet spec |
| 9 | Env-var schema documented in code | ⚠️ | Rich flag help-text in `cmd/bzlhub/serve.go`; no central struct-tag-based docgen |

### Summary of follow-up implementation work

The items below are the only ones that need code changes to bring
reality up to the contract. They're small.

| Work item | Size | Priority | Where |
|---|---|---|---|
| Add `/readyz` HTTP handler | ~10 lines + test | high — K8s probe parity | `internal/server/server.go` |
| Adopt `*_FILE` env-var convention before first secret-bearing feature | ~20 lines of helper + docs | medium — applies whenever auth/snapshot/litestream lands | new `internal/config/file_env.go` |
| Implement cold-start self-rehydration with strategy selector | feature work | low until a deployment needs it | `cmd/bzlhub/serve.go` + new `internal/rehydrate/` |
| Implement snapshot addon (`bzlhub snapshot export`/`restore`) | feature work | low; needed only when fast cold-start matters | new CLI subcommand + `internal/snapshot/` |
| Expand upstream schema to per-upstream typing | feature work | scoped to when git/Artifactory backends land | `cmd/bzlhub/serve.go` env parsing |

Until those land, the contract sections above accurately distinguish
between "MUST" (verified today) and "SHOULD / contract goal"
(documented direction, not yet implemented).

---

## 12. Out of scope

This contract is intentionally narrow. It does NOT cover:

- What bzlhub *does* — see `README.md`.
- Feature roadmap — covered in the project's internal planning
  documents.
- Service-level objectives (latency, uptime, scaling) — none claimed.
- Security review of the bzlhub binary — covered by a separate
  corporate-security plan.
- Deployment instructions for any specific environment — covered by
  per-deployment runbooks alongside the relevant compose / Helm
  artifacts.
- Backup of canonical state — bzlhub holds no canonical state today.
  When it does, see `addons.litestream` and `addons.snapshot`.
- Multi-region / active-active — explicitly out of scope; single-replica.

Adjacent work areas that will eventually intersect this contract but
are tracked separately:

- **MCP-over-HTTP transport.** Today bzlhub exposes MCP via stdio only
  (the `bzlhub mcp` subcommand). An HTTP-served MCP surface is planned
  and will, when shipped, share the apex port with the rest of the
  HTTP surface (path-routed). The contract additions needed at that
  point: auth model for unauthenticated agents, rate limiting,
  per-tool telemetry. To be expanded here when the MCP-over-HTTP work
  starts.
- **Outbound HTTP proxy support with Kerberos auth.** For deployments
  in environments where egress is gated by an SPNEGO proxy. Will add
  a `proxy.*` block to `values.yaml`. Tracked separately as a planning
  document; no contract impact until implementation lands.

---

## 13. Change log

| Date | Change |
|---|---|
| 2026-05-28 | Initial draft. Architecture model, 13 contract items, addon/sidecar policy, values.yaml reference, env-var reference, implementation status table. |

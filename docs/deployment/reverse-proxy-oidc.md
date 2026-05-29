# Reverse proxy + OIDC deployment

How to run canopy behind an authenticating reverse proxy so requests
are pre-authenticated before they ever reach the canopy process.

The full security model lives in
[../plans/08-corporate-security.md](../plans/08-corporate-security.md).
This document is the concrete recipe for the most common corporate
shape: **OIDC at the edge, canopy reads identity headers from a
trusted CIDR**.

> **Why this and not "canopy speaks OIDC directly?"** Operators
> already run an SSO gateway (oauth2-proxy, Authelia, Cloudflare
> Access, Pomerium, AWS ALB+Cognito, GCP IAP). Adding a second OIDC
> client inside canopy would be a maintenance burden and a second
> place to leak tokens. Pre-authentication at the edge is the
> idiomatic shape for self-hosted internal tooling.

## Topology

```
  user ──HTTPS──▶ reverse proxy ──HTTP──▶ canopy
                  │                       (binds 127.0.0.1:8080)
                  ├── terminates TLS
                  ├── runs OIDC dance against your IdP
                  └── injects X-Forwarded-User / -Email / -Groups
```

Canopy never sees the user's IdP cookies or tokens. It trusts the
reverse-proxy's identity headers **only when the request originates
from a configured CIDR** (`CANOPY_TRUSTED_PROXY_CIDR`), which closes
the obvious header-spoofing hole.

## Required canopy environment

| Var | Required? | Example | Notes |
|-----|-----------|---------|-------|
| `CANOPY_TRUSTED_PROXY_CIDR` | **yes** | `10.0.0.0/8,127.0.0.1/32` | Comma-separated CIDRs your reverse-proxy will originate from. Anything else has its identity headers stripped. |
| `CANOPY_ALLOWED_HOSTS` | **strongly recommended** | `bcr.bazel.build,*.githubusercontent.com` | Host allowlist for registry JSON and source-archive fetches. Without this, ingest/bump may fetch any URL a `source.json` points at. |
| `CANOPY_INGEST_WRITE_ENABLED` | optional | `false` | Default off. Flip on only after auth is wired and you've decided who is allowed to ingest. |
| `CANOPY_DEMO_MODE` | optional | `false` | Set to `true` if this is a public demo; the UI footer renders a "demo instance" badge. |
| `GITHUB_TOKEN_FILE` | optional | `/run/secrets/github-token` | Path to a file containing the GitHub token used for source.json fallbacks. Prefer files over env vars. |

## Identity headers canopy reads

When the request comes from inside the trusted CIDR, canopy reads:

| Header | Maps to |
|--------|---------|
| `X-Forwarded-User` | `auth.Identity.User` (login / sub) |
| `X-Forwarded-Email` | `auth.Identity.Email` (preferred display name) |
| `X-Forwarded-Groups` | `auth.Identity.Groups` (comma-separated) |

Outside the trusted CIDR, those headers are **ignored** — the request
falls through to anonymous. This is the load-bearing guarantee; if you
expose canopy directly to the internet without a reverse proxy,
identity claims are off the table.

The display name (email if present, otherwise user) lands on every
audit row via `audit_events.user_id`.

## Recipe: oauth2-proxy + nginx

A minimal compose / k8s blueprint.

### 1. oauth2-proxy

```yaml
# oauth2-proxy config (snippet)
provider                  = "oidc"
oidc_issuer_url           = "https://idp.example.com"
client_id                 = "canopy"
client_secret_file        = "/run/secrets/oidc-client-secret"
email_domains             = ["example.com"]
cookie_secret_file        = "/run/secrets/oauth2-cookie-secret"
upstreams                 = ["http://nginx:80/"]
reverse_proxy             = true
pass_user_headers         = true
set_authorization_header  = false  # canopy doesn't read it
pass_access_token         = false
set_xauthrequest          = true   # exposes X-Auth-Request-User/Email
```

### 2. nginx (re-emits headers under the names canopy expects)

```nginx
location / {
    auth_request /oauth2/auth;
    error_page 401 = /oauth2/sign_in;

    # Read identity from oauth2-proxy's response, forward to canopy
    # under the names canopy's headerAuth middleware reads.
    auth_request_set $auth_user   $upstream_http_x_auth_request_user;
    auth_request_set $auth_email  $upstream_http_x_auth_request_email;
    auth_request_set $auth_groups $upstream_http_x_auth_request_groups;

    proxy_set_header X-Forwarded-User   $auth_user;
    proxy_set_header X-Forwarded-Email  $auth_email;
    proxy_set_header X-Forwarded-Groups $auth_groups;

    proxy_pass http://canopy:8080;
}
```

### 3. canopy (compose snippet)

```yaml
canopy:
  image: ghcr.io/albertocavalcante/canopy:latest
  environment:
    CANOPY_TRUSTED_PROXY_CIDR: "10.244.0.0/16"   # k8s pod cidr / docker net
    CANOPY_ALLOWED_HOSTS: "bcr.bazel.build,*.githubusercontent.com"
    CANOPY_INGEST_WRITE_ENABLED: "true"
    GITHUB_TOKEN_FILE: "/run/secrets/github-token"
  secrets:
    - github-token
```

## Verifying

Hit `/api/version` from inside the trusted network — the response
itself is unauthenticated, but the **audit row** for the request
should populate `user_id` with the email of the caller. Confirm with:

```sh
sqlite3 canopy.db \
  "SELECT ts, kind, source, user_id FROM audit_events ORDER BY ts DESC LIMIT 5"
```

If `user_id` is empty for requests that should be authenticated, the
trusted CIDR isn't matching. Check `r.RemoteAddr` server-side
(`docker logs canopy` will show the accesslog) and adjust
`CANOPY_TRUSTED_PROXY_CIDR` accordingly.

If `user_id` populates from a request that should NOT have been
authenticated, the trusted CIDR is too broad — narrow it.

## Hardening notes

- **Bind canopy to localhost only**, then proxy. Do not expose
  `:8080` on a public interface even temporarily; the identity
  headers are trusted by source-CIDR, not by transport.
- **Use `*_FILE` for every secret.** Compose `env` is visible in
  `docker inspect` and k8s `kubectl describe pod`; file mounts are
  not.
- **Rotate tokens via file replacement + SIGHUP**, not by restarting
  the canopy process. `LazyRead` callers pick up new values on the
  next call.
- **Default-deny registry/archive egress.** `CANOPY_ALLOWED_HOSTS` is
  empty by default, which means no enforcement for ingest/bump fetches.
  Set it before exposing the instance to anyone but yourself.
- **Ingest-write gates are layered:** even with auth in place, leave
  `CANOPY_INGEST_WRITE_ENABLED=false` until you have an
  authorization policy (group membership, header allowlist) that
  matches your intent.

## Roadmap

- **Bearer tokens** (Sprint 4): for CI / agents that can't ride OIDC
  cookies. Same `auth.Identity` shape; different source.
- **GitHub App installation tokens** (Sprint 4): short-lived,
  per-installation tokens for source.json fetches against private
  GitHub orgs. Replaces long-lived PATs in `GITHUB_TOKEN`.
- **OIDC token federation** (Sprint 6): for orgs that want canopy
  itself to act as an OIDC RP rather than relying on a reverse-proxy.
  Optional; the proxy path stays supported.

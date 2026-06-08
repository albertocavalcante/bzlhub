# Reverse proxy + OIDC deployment

How to run bzlhub behind an authenticating reverse proxy so requests
are pre-authenticated before they ever reach the bzlhub process.

The full security model lives in
[../plans/08-corporate-security.md](../plans/08-corporate-security.md).
This document is the concrete recipe for the most common corporate
shape: **OIDC at the edge, bzlhub reads identity headers from a
trusted CIDR**.

> **Why this and not "bzlhub speaks OIDC directly?"** Operators
> already run an SSO gateway (oauth2-proxy, Authelia, Cloudflare
> Access, Pomerium, AWS ALB+Cognito, GCP IAP). Adding a second OIDC
> client inside bzlhub would be a maintenance burden and a second
> place to leak tokens. Pre-authentication at the edge is the
> idiomatic shape for self-hosted internal tooling.

## Topology

```
  user ──HTTPS──▶ reverse proxy ──HTTP──▶ bzlhub
                  │                       (binds 127.0.0.1:8080)
                  ├── terminates TLS
                  ├── runs OIDC dance against your IdP
                  └── injects X-Forwarded-User / -Email / -Groups
```

Bzlhub never sees the user's IdP cookies or tokens. It trusts the
reverse-proxy's identity headers **only when the request originates
from a configured CIDR** (`CANOPY_TRUSTED_PROXY_CIDR`), which closes
the obvious header-spoofing hole.

## Required bzlhub environment

| Var | Required? | Example | Notes |
|-----|-----------|---------|-------|
| `CANOPY_TRUSTED_PROXY_CIDR` | **yes** | `10.0.0.0/8,127.0.0.1/32` | Comma-separated CIDRs your reverse-proxy will originate from. Anything else has its identity headers stripped. |
| `CANOPY_ALLOWED_HOSTS` | **strongly recommended** | `bcr.bazel.build,*.githubusercontent.com` | Host allowlist for registry JSON and source-archive fetches. Without this, ingest/bump may fetch any URL a `source.json` points at. |
| `CANOPY_INGEST_WRITE_ENABLED` | optional | `false` | Default off. Flip on only after auth is wired and you've decided who is allowed to ingest. |
| `CANOPY_DEMO_MODE` | optional | `false` | Set to `true` if this is a public demo; the UI footer renders a "demo instance" badge. |
| `GITHUB_TOKEN_FILE` | optional | `/run/secrets/github-token` | Path to a file containing the GitHub token used for source.json fallbacks. Prefer files over env vars. |

## Identity headers bzlhub reads

When the request comes from inside the trusted CIDR, bzlhub reads:

| Header | Maps to |
|--------|---------|
| `X-Forwarded-User` | `auth.Identity.User` (login / sub) |
| `X-Forwarded-Email` | `auth.Identity.Email` (preferred display name) |
| `X-Forwarded-Groups` | `auth.Identity.Groups` (comma-separated) |

Outside the trusted CIDR, those headers are **ignored** — the request
falls through to anonymous. This is the load-bearing guarantee; if you
expose bzlhub directly to the internet without a reverse proxy,
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
client_id                 = "bzlhub"
client_secret_file        = "/run/secrets/oidc-client-secret"
email_domains             = ["example.com"]
cookie_secret_file        = "/run/secrets/oauth2-cookie-secret"
upstreams                 = ["http://nginx:80/"]
reverse_proxy             = true
pass_user_headers         = true
set_authorization_header  = false  # bzlhub doesn't read it
pass_access_token         = false
set_xauthrequest          = true   # exposes X-Auth-Request-User/Email
```

### 2. nginx (re-emits headers under the names bzlhub expects)

```nginx
location / {
    auth_request /oauth2/auth;
    error_page 401 = /oauth2/sign_in;

    # Read identity from oauth2-proxy's response, forward to bzlhub
    # under the names bzlhub's headerAuth middleware reads.
    auth_request_set $auth_user   $upstream_http_x_auth_request_user;
    auth_request_set $auth_email  $upstream_http_x_auth_request_email;
    auth_request_set $auth_groups $upstream_http_x_auth_request_groups;

    proxy_set_header X-Forwarded-User   $auth_user;
    proxy_set_header X-Forwarded-Email  $auth_email;
    proxy_set_header X-Forwarded-Groups $auth_groups;

    proxy_pass http://bzlhub:8080;
}
```

### 3. bzlhub (compose snippet)

```yaml
bzlhub:
  image: ghcr.io/albertocavalcante/bzlhub:latest
  environment:
    CANOPY_TRUSTED_PROXY_CIDR: "10.244.0.0/16"   # k8s pod cidr / docker net
    CANOPY_ALLOWED_HOSTS: "bcr.bazel.build,*.githubusercontent.com"
    CANOPY_INGEST_WRITE_ENABLED: "true"
    GITHUB_TOKEN_FILE: "/run/secrets/github-token"
  secrets:
    - github-token
```

## Recipe: Envoy sidecar with native OAuth2 + JWT filters

If your environment already runs Envoy as a sidecar (k8s+istio,
explicit envoy-per-app, etc.) you can use Envoy's native filters
instead of standing up oauth2-proxy. The pattern is well-documented;
JPMorgan Chase's tech blog has a clear walkthrough:
[Protecting web applications via Envoy OAuth2 filter](https://www.jpmorgan.com/technology/technology-blog/protecting-web-applications-via-envoy-oauth2-filter).
Upstream Envoy docs:
[oauth2_filter](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/oauth2_filter.html)
+ [jwt_authn_filter](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/jwt_authn_filter).

### Topology

```
  user ──HTTPS──▶ Envoy ──HTTP──▶ bzlhub
                  │              (binds 127.0.0.1:8080)
                  ├── terminates TLS
                  ├── envoy.filters.http.oauth2 — handles OIDC dance
                  ├── envoy.filters.http.jwt_authn — validates JWT on each request
                  └── (option A) lua/wasm — translates JWT claims to X-Forwarded-* headers
                       (option B) forward_payload_header — gives bzlhub the decoded claims directly
```

Bzlhub's relationship to Envoy is the same as to oauth2-proxy:
identity arrives in headers from a trusted CIDR; bzlhub never sees
the IdP tokens.

### 1. Envoy config (sketch)

```yaml
http_filters:
  - name: envoy.filters.http.oauth2
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.oauth2.v3.OAuth2
      config:
        token_endpoint:
          cluster: oidc-idp
          uri: "${OIDC_TOKEN_ENDPOINT}"     # operator-supplied
          timeout: 5s
        authorization_endpoint: "${OIDC_AUTHORIZATION_ENDPOINT}"
        redirect_uri: "${CANOPY_PUBLIC_URL}/callback"
        redirect_path_matcher: { path: { exact: /callback } }
        signout_path: { path: { exact: /signout } }
        forward_bearer_token: true
        credentials:
          client_id: "${OIDC_CLIENT_ID}"
          token_secret: { name: oauth-token-secret }
          hmac_secret: { name: oauth-hmac-secret }
        auth_scopes: [openid, profile, email]
  - name: envoy.filters.http.jwt_authn
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.JwtAuthentication
      providers:
        oidc:
          remote_jwks:
            http_uri:
              uri: "${OIDC_JWKS_URI}"
              cluster: oidc-idp
              timeout: 5s
            cache_duration: 600s
          forward_payload_header: x-jwt-payload   # option B: bzlhub reads claims directly
      rules:
        - match: { prefix: / }
          requires: { provider_name: oidc }
  - name: envoy.filters.http.router
    typed_config: {}
```

All IdP-specific values (`OIDC_TOKEN_ENDPOINT`,
`OIDC_AUTHORIZATION_ENDPOINT`, `OIDC_JWKS_URI`, `OIDC_CLIENT_ID`)
come from environment / config — Envoy supports env substitution via
`bootstrap-version: 3` or you can template the yaml at deploy time.
**Never hardcode the IdP URLs**; ENV-substitute or use Envoy's
secrets file mechanism.

### 2. Translating JWT claims to bzlhub's identity headers

Two options. Pick whichever fits your operational comfort.

**Option (A) — Lua filter rewrites claims to X-Forwarded-\* headers**

Zero bzlhub code change. Add a Lua filter between jwt_authn and the
router that reads `x-jwt-payload` and emits the headers bzlhub
expects:

```yaml
- name: envoy.filters.http.lua
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.lua.v3.Lua
    default_source_code:
      inline_string: |
        function envoy_on_request(request_handle)
          local payload_b64 = request_handle:headers():get("x-jwt-payload")
          if payload_b64 then
            -- Decode base64url + parse JSON
            local payload = require("cjson").decode(decode_base64url(payload_b64))
            request_handle:headers():add("x-forwarded-user",   payload.sub  or "")
            request_handle:headers():add("x-forwarded-email",  payload.email or "")
            -- ADFS-style groups claim is often `roles`; OIDC standard is `groups`
            local groups = payload.groups or payload.roles or {}
            request_handle:headers():add("x-forwarded-groups", table.concat(groups, ","))
          end
        end
```

The exact claim names depend on your IdP — `email`/`groups` are OIDC
standard; ADFS may use `winaccountname`/`roles`; Azure AD has
`preferred_username`; Okta passes whatever you configure in the
authorization-server scope mapping. Adjust the Lua accordingly.
**The claim names are operator-config; never customer-specific in
bzlhub itself.**

**Option (B) — bzlhub reads `x-jwt-payload` directly (future)**

Cleaner long-term: bzlhub adds a small middleware that decodes the
`x-jwt-payload` header (base64-url + JSON) into the same
`auth.Identity` the existing `X-Forwarded-*` middleware produces.
Tracked as a follow-up; option (A) works today with no bzlhub
change.

### 3. bzlhub (compose snippet)

```yaml
bzlhub:
  image: ghcr.io/albertocavalcante/bzlhub:latest
  environment:
    CANOPY_TRUSTED_PROXY_CIDR: "10.244.0.0/16"   # network the Envoy sidecar comes from
    CANOPY_ALLOWED_HOSTS: "bcr.bazel.build,*.githubusercontent.com"
    CANOPY_INGEST_WRITE_ENABLED: "true"
  # bzlhub is identical to the oauth2-proxy recipe; only the reverse
  # proxy differs
```

### Why use Envoy over oauth2-proxy

- You already have Envoy in your stack (Istio, Linkerd, dedicated
  service mesh, k8s ingress) — adding a filter is configuration, not
  a new deployable
- You want JWT validation enforced at the proxy layer with no app
  involvement
- You want one OAuth2 implementation across many services regardless
  of language (the polyglot argument the Envoy filter pattern is
  designed for)
- You're already running zero-trust at L7 and OAuth2 fits the model

### Why use oauth2-proxy over Envoy

- You're not running Envoy elsewhere — adding it just for this is
  heavyweight
- oauth2-proxy's UX (HTML sign-in pages, brand customization) is
  more polished out of the box
- Smaller blast radius if mis-configured (oauth2-proxy is one binary
  vs Envoy's filter chain complexity)

## Verifying

Hit `/api/version` from inside the trusted network — the response
itself is unauthenticated, but the **audit row** for the request
should populate `user_id` with the email of the caller. Confirm with:

```sh
sqlite3 bzlhub.db \
  "SELECT ts, kind, source, user_id FROM audit_events ORDER BY ts DESC LIMIT 5"
```

If `user_id` is empty for requests that should be authenticated, the
trusted CIDR isn't matching. Check `r.RemoteAddr` server-side
(`docker logs bzlhub` will show the accesslog) and adjust
`CANOPY_TRUSTED_PROXY_CIDR` accordingly.

If `user_id` populates from a request that should NOT have been
authenticated, the trusted CIDR is too broad — narrow it.

## Procurement gates over header auth

When bzlhub's procurement state machine is wired (`CANOPY_POLICY_FILE`
set), every gated action consults `policy.auth.actions[<action>]`
against the resolved identity. Header-auth users get the same
treatment as bearer-token users — the gate doesn't care how the
identity arrived.

The interesting wiring is **groups**. A policy entry like:

```yaml
auth:
  actions:
    approve_request: group:approver
    deny_request:    group:approver
```

requires the request's resolved identity to carry `approver` in its
`Groups` slice. That comes from `X-Forwarded-Groups: approver` in
the reverse-proxy emission. Two concrete patterns:

### A. IdP group names match bzlhub gate names (simplest)

Configure your IdP (Okta, Entra, Google) with a group literally
named `approver`, assign reviewers to it, and emit it verbatim.
oauth2-proxy with the `groups` scope on most IdPs does this
unchanged — the group name flows through to
`X-Forwarded-Groups: approver`.

```ini
# oauth2-proxy.cfg (additional fields beyond §1 above)
scope = "openid email profile groups"
allowed_groups = ["approver", "eval-submitter"]   # restricts who reaches bzlhub at all
```

### B. IdP groups need renaming (operator translation)

When the IdP carries org-specific names (`bzlhub-reviewers`,
`procurement-team`) that you'd rather not leak into your
policy.yml, translate at the proxy:

- **nginx with a Lua map**: rewrite
  `$upstream_http_x_auth_request_groups` to swap `bzlhub-reviewers`
  → `approver` before re-emitting as `X-Forwarded-Groups`.
- **Envoy via Lua filter or header rewrite rule**: same idea,
  applied between the JWT-authn output and the upstream cluster.

Either way the bzlhub-facing header carries canonical names; the
policy.yml stays human-readable + tied to bzlhub's vocabulary.

### Verifying the gate path

```sh
# As a reviewer (their browser session has the OIDC cookie + group):
curl -i https://<your-host>/api/v1/policy/effective
# → 200 with {"actions":{"approve_request": true, ...}}

# As a non-reviewer:
curl -i https://<your-host>/api/v1/policy/effective
# → 200 with {"actions":{"approve_request": false, ...}}
```

The button-visibility logic in bzlhub's UI (`/admin/requests`)
reads this endpoint to decide whether to render Approve / Deny
buttons.

### Bearer + header coexistence

A request can carry both an `Authorization: Bearer <token>` AND
the `X-Forwarded-*` headers. Bearer wins per the precedence rule
documented in `docs/plans/72-...md` §CC3 — the WARN log emitted
at bzlhub's stdout asks the operator to investigate (most often
the cause is a reverse proxy that's both terminating OIDC and
passing through Authorization, which usually means an unfederated
service account is double-authing).

## Hardening notes

- **Bind bzlhub to localhost only**, then proxy. Do not expose
  `:8080` on a public interface even temporarily; the identity
  headers are trusted by source-CIDR, not by transport.
- **Use `*_FILE` for every secret.** Compose `env` is visible in
  `docker inspect` and k8s `kubectl describe pod`; file mounts are
  not.
- **Rotate tokens via file replacement + SIGHUP**, not by restarting
  the bzlhub process. `LazyRead` callers pick up new values on the
  next call.
- **Default-deny registry/archive egress.** `CANOPY_ALLOWED_HOSTS` is
  empty by default, which means no enforcement for ingest/bump fetches.
  Set it before exposing the instance to anyone but yourself.
- **Ingest-write gates are layered:** even with auth in place, leave
  `CANOPY_INGEST_WRITE_ENABLED=false` until you have an
  authorization policy (group membership, header allowlist) that
  matches your intent.

## Roadmap

- **Bearer tokens — shipped.** For CI / agents that can't ride OIDC
  cookies. Same `auth.Identity` shape; different source. See
  `self-host-quickstart.md` §Push credentials and §"Graduating to
  OIDC" for the parallel-deployment pattern (bearer for service
  accounts, OIDC for humans). Bearer wins on collision per §CC3
  precedence — described in the "Bearer + header coexistence"
  section above.
- **GitHub App installation tokens** (deferred): short-lived,
  per-installation tokens for source.json fetches against private
  GitHub orgs. Replaces long-lived PATs in `GITHUB_TOKEN`.
- **OIDC token federation** (deferred): for orgs that want bzlhub
  itself to act as an OIDC RP rather than relying on a reverse-proxy.
  Optional; the proxy path stays supported.

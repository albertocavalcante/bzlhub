# Self-host quickstart: bzlhub with procurement

Stand up a corp-bzlhub deployment from clean — UI + REST + BCR
serving anonymous browse, bearer-authed procurement, policy-gated
approvals.

This doc is for the operator deploying bzlhub on their own
infrastructure. The reference deployment is **corp.bzlhub.com** —
where this doc is concretely true. Substitute your hostnames,
forge, and identity provider as you go.

## What you'll have at the end

A bzlhub instance reachable at `https://<your-host>` with:

- **Anonymous browse**: anyone hits the UI + read APIs.
- **Authenticated submit**: callers presenting a bearer token can
  POST a procurement request. Strict profile gates `submit_request`
  to any authenticated user; open profile lets anonymous submit.
- **Group-gated approval**: tokens with `groups: ["approver"]`
  can POST `/approve` and `/deny`. Anyone else gets 403.
- **Preflight runner**: a goroutine pool inside bzlhub picks up
  pending requests, validates source URLs, and routes them to
  `needs_review` (default) or `denied` (policy violation).
- **Audit trail**: every state transition writes an
  `audit_events` row with the actor's identity.
- **SIGHUP token rotation**: edit `identity.json`, send SIGHUP,
  bzlhub re-reads in place. No restart.

## Prereqs

| Component | Why | Reference choice |
|---|---|---|
| Host | runs bzlhub in Docker | Hetzner CAX21, ~€6/mo |
| Git forge for the registry repo | stores policy.yml + admitted modules | Forgejo, Gitea, GitHub, anything with HTTPS |
| Reverse proxy / tunnel | TLS, hostname routing | Cloudflare Tunnel, Caddy, Traefik |
| Container runtime | Docker or Podman | Docker Compose v2 |
| Secrets store (off-box) | bearer-token plaintext lives here | 1Password, age, sops |

Not required for v0:
- OIDC / identity provider. Bearer tokens are sufficient for the
  small-team case. Upgrade path documented under
  [Graduating to OIDC](#graduating-to-oidc).
- Object storage. bzlhub's state is the SQLite index + the registry
  git repo working tree; both are tarball-backupable.

## 1. Bootstrap the registry repo

The registry repo is a BCR-shape directory tree (`bazel_registry.json`,
`modules/`, `blobs/`) plus a `.bzlhub/policy.yml`. bzlhub reads from
it; admitted modules will (in a later slice) be committed back into
it.

Create a private repo on your forge — `<your-org>/registry`. Seed
with three files:

`bazel_registry.json`:
```json
{
  "mirrors": [],
  "module_base_path": "modules"
}
```

`.bzlhub/policy.yml`:
```yaml
version: 1
profile: strict   # or: open, closed
auth:
  actions:
    submit_request:  authenticated
    approve_request: group:approver
    deny_request:    group:approver
admission:
  source:
    require_https: true
```

`.gitignore`:
```
.DS_Store
```

Commit + push.

## 2. Generate bearer tokens

For each human or service that needs an identity, mint 32 random
bytes off the deployment host (the box should never see plaintext):

```bash
TOKEN=$(openssl rand -hex 32)
echo "Plaintext (give to user; do NOT commit):"
echo "  $TOKEN"
echo "SHA-256 (commit to identity.json):"
printf '%s' "$TOKEN" | sha256sum | awk '{print $1}'
```

Save the plaintext in your secrets store. Only the hash goes on
the bzlhub box.

## 3. Assemble `identity.json`

```json
{
  "version": 1,
  "tokens": [
    {
      "token_sha256": "<alice-hash>",
      "identity": {
        "user":   "alice@example.com",
        "email":  "alice@example.com",
        "groups": ["approver"]
      }
    },
    {
      "token_sha256": "<bob-hash>",
      "identity": {
        "user":   "bob@example.com",
        "email":  "bob@example.com",
        "groups": ["eval-submitter"]
      }
    }
  ]
}
```

Place on the box at a path of your choice (the reference deployment
uses `/opt/bzlhub-demo/secrets/identity.json`) with mode `0600`.
bzlhub WARNs at boot if it finds the file group- or world-readable.

## 4. Wire the env

bzlhub reads two file paths and a handful of knobs from env:

| Env var | Purpose | Typical value |
|---|---|---|
| `CANOPY_IDENTITY_FILE` | path to identity.json | `/etc/bzlhub/identity.json` |
| `CANOPY_POLICY_FILE` | path to policy.yml | `/var/lib/bzlhub/registry/.bzlhub/policy.yml` |
| `CANOPY_ROOT` | BCR-shape registry tree | `/var/lib/bzlhub/registry` |
| `CANOPY_DB` | SQLite index path | `/var/lib/bzlhub/index/bzlhub.db` |
| `CANOPY_BIND` | listen address | `0.0.0.0:8091` |
| `CANOPY_TRUSTED_PROXY_CIDR` | reverse-proxy CIDRs | `172.16.0.0/12` (Docker bridge) |
| `CANOPY_PREFLIGHT_WORKERS` | preflight pool size | `2` (default) |
| `CANOPY_PREFLIGHT_POLL_EVERY` | preflight poll interval | `5s` (default) |
| `CANOPY_REGISTRY_WORKTREE` | git worktree for commit-back | unset → uses `CANOPY_ROOT` |
| `CANOPY_BOT_EMAIL` | committer identity for admit | `bzlhub-bot@localhost` |

Unset `CANOPY_POLICY_FILE` to disable policy gates entirely — the
procurement endpoints then don't register. Useful for the
read-only-public bzlhub shape (e.g., a public BCR mirror).

### Push credentials for commit-back

When the registry root IS a git working tree (`.git/` present),
the admit runner uses `publish.GitDirectPublisher` to commit
admitted artifacts and `git push` them back to the configured
remote. `git push` runs as a shell-out, so push credentials come
from anywhere git's standard credential helpers can find them.

Two recommended shapes:

**A. PAT embedded in remote URL** (simplest). Clone with the
token in the URL — git remembers it in `.git/config`:

```bash
git clone "https://bzlhub-bot:${PAT}@<forge>/<org>/registry.git" registry
```

The PAT lives in `registry/.git/config`. Rotation = re-clone, or
`git remote set-url origin https://...` with the new token.

**B. GIT_ASKPASS shim** (cleaner separation). Token in a file
bzlhub bind-mounts; a shim prints it on demand:

```bash
echo "$PAT" | sudo tee /etc/bzlhub/secrets/forgejo-token >/dev/null
sudo chmod 600 /etc/bzlhub/secrets/forgejo-token

sudo tee /etc/bzlhub/git-askpass.sh >/dev/null <<'SH'
#!/bin/sh
cat /etc/bzlhub/secrets/forgejo-token
SH
sudo chmod +x /etc/bzlhub/git-askpass.sh
```

Then in the container, set `GIT_ASKPASS=/etc/bzlhub/git-askpass.sh`
and bind-mount both files read-only. Rotation = rewrite the token
file; no re-clone needed.

When the registry root is NOT a git clone, bzlhub uses
`FilesystemPublisher` instead — admitted entries land on disk but
no commit / push happens. Useful for personal-bzlhub installs
that don't keep their registry in git.

## 5. Deploy

Compose template — adapt paths to your host:

```yaml
services:
  bzlhub:
    image: bzlhub:latest
    container_name: bzlhub
    restart: unless-stopped
    ports:
      - "127.0.0.1:8091:8091"
    environment:
      CANOPY_BIND: 0.0.0.0:8091
      CANOPY_ROOT: /var/lib/bzlhub/registry
      CANOPY_DB:   /var/lib/bzlhub/index/bzlhub.db
      CANOPY_IDENTITY_FILE: /etc/bzlhub/identity.json
      CANOPY_POLICY_FILE:   /var/lib/bzlhub/registry/.bzlhub/policy.yml
      CANOPY_TRUSTED_PROXY_CIDR: 172.16.0.0/12
      CANOPY_UPSTREAMS: https://bcr.bazel.build
    volumes:
      - /opt/bzlhub/registry:/var/lib/bzlhub/registry
      - /opt/bzlhub/secrets/identity.json:/etc/bzlhub/identity.json:ro
      - bzlhub_index:/var/lib/bzlhub/index
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O- http://127.0.0.1:8091/healthz >/dev/null 2>&1 || exit 1"]
      interval: 30s

volumes:
  bzlhub_index:
```

Clone the registry repo into `/opt/bzlhub/registry/`, place the
identity file, then `docker compose up -d`.

Reference deployment (corp.bzlhub.com) lives in
[`self-hosted/bzlhub-demo/`](https://github.com/albertocavalcante/bzlhub/tree/main/self-hosted)
of the bzlhub operator-blueprints repo — same shape, with
Cloudflare Tunnel ingress and a Terraform module wiring the
hostname.

## 6. Verify

```bash
# Liveness
curl -sf https://<your-host>/healthz
# → ok

# Anonymous policy view — confirm submit_request is gated.
curl -s https://<your-host>/api/v1/policy/effective | jq
# → {"profile":"strict","actions":{"submit_request":false,...}}

# Submit as a bearer-authed user
curl -s https://<your-host>/api/v1/requests \
     -H "Authorization: Bearer $TOKEN_ALICE" \
     -H "Content-Type: application/json" \
     -d '{"module":"rules_python","version":"1.5.0","source_url":"https://github.com/bazelbuild/rules_python/archive/1.5.0.tar.gz"}' \
  | jq
# → {"id":1,"state":"pending"}

# Preflight picks it up within ~5s
sleep 8
curl -s https://<your-host>/api/v1/requests | jq '.requests[0].state'
# → "needs_review"

# Approve (needs an approver-group token)
curl -s -X POST https://<your-host>/api/v1/requests/1/approve \
     -H "Authorization: Bearer $TOKEN_APPROVER" | jq
# → {"id":1,"state":"approved"}

# When the registry root is a git working tree, the admit runner
# picks up the approved request, fetches the source archive,
# materializes BCR-shape files, commits with bzlhub-bot, pushes,
# and transitions to indexed within ~5s.
sleep 8
curl -s https://<your-host>/api/v1/requests/1 | jq '.state, .committed_sha'
# → "indexed"
# → "<commit sha>"

# Bazel can now resolve it:
curl -s https://<your-host>/modules/rules_python/1.5.0/source.json | jq
# → {"url": "...", "integrity": "sha256-...", "strip_prefix": "..."}
```

## 7. Seed (optional but recommended)

A freshly-deployed bzlhub has an empty request queue. Populate
with a canonical set so visitors see real content:

```bash
docker compose exec bzlhub bzlhub seed \
    --db=/var/lib/bzlhub/index/bzlhub.db \
    --submitter=seed-bot@<your-host>
# → seed: inserted=12 skipped=0 total=12
```

Re-runs are no-ops. Override the default 12-rule set with
`--module=name@version` (repeatable).

## 8. Operate

### Rotate a token

Edit `identity.json` on the box — add new entries, remove old ones.
SIGHUP bzlhub:

```bash
docker compose kill -s HUP bzlhub
```

The boot log emits `SIGHUP: identity registry reloaded
path=... tokens=N` on success.

### Change policy

Edit `.bzlhub/policy.yml` in the registry repo (review as a normal
git PR), pull on the box, restart:

```bash
cd /opt/bzlhub/registry && git pull
docker compose restart bzlhub
```

Policy hot-reload (SIGHUP) is on the deferred list — restart is the
v0 path.

### Inspect the audit trail

```bash
curl -s 'https://<your-host>/api/v1/activity/history?kind=request_approved&limit=20' | jq
```

Each row carries `user_id` (the actor's email), `kind`, `module`,
`version`, `ts`, and a JSON `payload`.

### Back up

```bash
sudo tar czf bzlhub-$(date +%F).tar.gz \
    --exclude='/opt/bzlhub/registry/.git/objects' \
    /opt/bzlhub/registry/ \
    /opt/bzlhub/secrets/

docker run --rm -v bzlhub_index:/src -v "$PWD":/dst alpine \
    tar czf /dst/bzlhub-index-$(date +%F).tar.gz -C /src .
```

The `.git/objects/` exclude trims the tarball — the working tree
restores from the forge origin on a fresh deploy.

## Policy profiles

Three baselines ship embedded; operators override per-knob on top.

| Profile | submit_request | approve_request | view_modules | view_audit |
|---|---|---|---|---|
| `strict` (default) | authenticated | group:approver | any | any |
| `open` | any | group:approver | any | any |
| `closed` | group:engineers | group:procurement | authenticated | authenticated |

Pick the profile in `policy.yml`:

```yaml
profile: closed
```

Override a single gate while keeping the rest of the baseline:

```yaml
profile: strict
auth:
  actions:
    submit_request: group:reviewers   # tighter than the strict default
```

Full schema reference: see `internal/policy/policy.go` for the
struct fields, `internal/policy/profiles/*.yml` for the baselines.

## Graduating to OIDC

Bearer tokens are the v0 identity path. When the deployment grows
to where shared secrets become unmanageable:

1. Stand up an OIDC-terminating reverse proxy in front of bzlhub
   (oauth2-proxy, Pomerium, or your IdP's official sidecar).
2. Configure it to forward `X-Forwarded-User`, `X-Forwarded-Email`,
   `X-Forwarded-Groups` after successful auth.
3. Set `CANOPY_TRUSTED_PROXY_CIDR` to the proxy's source CIDR.
4. Optionally leave bearer auth wired (bzlhub accepts both —
   bearer wins on collision, useful for CI/MCP callers that
   can't speak OIDC).

No bzlhub code change is required for this transition.

See [`reverse-proxy-oidc.md`](./reverse-proxy-oidc.md) for concrete
recipes with nginx + oauth2-proxy.

## Hardening notes

- **`identity.json` mode**: must be `0600`. bzlhub WARNs if it's
  group/world-readable. The file is bearer-token credential material
  — treat it like an SSH private key.
- **Plaintext tokens never on the box**: the SHA-256 hash is what
  bzlhub stores; the plaintext lives in your secrets store and is
  distributed to users out-of-band.
- **Reverse proxy CIDR**: `CANOPY_TRUSTED_PROXY_CIDR` gates whether
  `X-Forwarded-*` headers are honored. Set it tight (the proxy's
  exact IP, not `0.0.0.0/0`) to defang header-spoofing attacks
  from outside the trusted layer.
- **Egress**: bzlhub fetches from `CANOPY_UPSTREAMS` (default
  bcr.bazel.build). If your environment requires an egress
  allowlist, the upstream URLs are the entries to whitelist.
- **Audit retention**: `audit.retain_days` in policy.yml. No
  automatic pruning yet — the column is read but not enforced.
  Operators wanting SOC2/SOX compliance windows should snapshot
  the SQLite DB on the retention boundary.

## Common failures

| Symptom | Cause | Fix |
|---|---|---|
| Boot: `CANOPY_IDENTITY_FILE=...: file not found` | env var set but file missing | Either create the file or unset the env var |
| Boot: `CANOPY_POLICY_FILE=...: unsupported version N` | policy.yml `version:` doesn't match bzlhub's supported version | Upgrade bzlhub, or downgrade policy.yml |
| POST /requests returns 403 | `submit_request` gate denies | Check token has the right groups; check policy.yml's `auth.actions.submit_request` |
| POST /approve returns 403 | reviewer's groups don't include `approver` (or whatever your gate is) | Update `identity.json` + SIGHUP |
| Submit succeeds but state stays `pending` | preflight runner isn't running | Verify bzlhub boot log says `preflight runner starting workers=N`; check `CANOPY_POLICY_FILE` is set (no policy → no runner) |
| Approved request stays `approved`, never `indexed` | admit runner can't reach the worktree, OR `CANOPY_ROOT` isn't a git clone (FilesystemPublisher wired — no push) | Check boot log: `admit publisher: git-direct` (good) vs `filesystem (no git push)` (admitted on disk only). Set `CANOPY_REGISTRY_WORKTREE` to a real git clone if needed. |
| `git push` fails on admit | wrong push credentials | The error is in `denial_reason` of the failed request. Reseat the PAT (re-clone, or rewrite the GIT_ASKPASS file) and re-submit. |
| Identity WARN: `world-readable` | file mode too loose | `chmod 600 identity.json` |

## Reference

- `docs/plans/67-procurement.md` — procurement state machine
- `docs/plans/70-user-identity-personalization-and-login-policy.md` — identity model
- `docs/plans/71-configurability-principle-and-policy-yml.md` — policy schema
- `internal/policy/profiles/*.yml` — baseline profile files

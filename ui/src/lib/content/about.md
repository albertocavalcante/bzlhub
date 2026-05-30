# About bzlhub

bzlhub mirrors the [Bazel Central Registry](https://bcr.bazel.build)
and adds an indexing layer: search, hermeticity classification per
module, drift detection against upstream, source-code navigation
across modules, and a Model Context Protocol endpoint for coding
agents to query.

bzlhub is not BCR. BCR is upstream and is the source of truth for
every module's bytes. bzlhub fetches those bytes from BCR, caches
them locally, indexes them, and serves them back over the BCR HTTP
protocol with the indexing layer rendered as HTML, JSON, and MCP
tools on top.

## Add it to your .bazelrc

```
common --registry=https://bzlhub.com
```

That's it. `bazel mod show_repo rules_go` now resolves through
bzlhub. Modules already in the local mirror serve from disk in
single-digit milliseconds; cold modules cascade through to upstream
BCR on the first request and serve from there.

You can switch back to upstream BCR at any time by removing the
line — bzlhub never modifies what BCR returns, and every byte it
serves comes from BCR.

## Browse the index

[bzlhub.com/modules](/modules) — every indexed module with version
count, hermeticity class, drift status against upstream, and a
source-nav link into the indexed `.bzl` files.

Example pages:
- [rules_go@0.50.1](/modules/rules_go/0.50.1) — 47 rules, 12 deps,
  classified `prebuilt-binaries-pinned`
- [bazel_skylib@1.7.1](/modules/bazel_skylib/1.7.1) — classified
  `pure-starlark`
- [/drift](/drift) — modules where bzlhub's mirror is behind upstream

## Query from a coding agent

bzlhub exposes a [Model Context Protocol](https://modelcontextprotocol.io)
endpoint at `https://bzlhub.com/mcp` using the Streamable HTTP
transport in stateless mode. Configure any MCP-capable agent
(Claude Code, Cursor, Codex) to point at that URL and the agent can
search the index, fetch per-module reports (rules, providers,
hermeticity, deps), navigate source, and check drift directly from
inside the session.

The same tools are also available over stdio via `canopy mcp` for
local-process agents.

## What bzlhub is NOT

- Not BCR. BCR is the source of truth; bzlhub mirrors it.
- Not a write surface. The public bzlhub.com instance does not
  accept module publishes. (Operators self-hosting canopy can
  configure write endpoints; the public instance doesn't expose
  them.)
- Not multi-tenant. One instance, one replica.
- Not SLA-backed. Free, public, best-effort. Live operational state
  at [/status](/status).

## Run your own

bzlhub is canopy running on a Hetzner VPS. Canopy is a single Go
binary with a SQLite index and a filesystem mirror. No S3, no
Postgres, no Kubernetes required. Build from source or pull a
prebuilt image.

- Source: [github.com/albertocavalcante/bzlhub](https://github.com/albertocavalcante/bzlhub)
- Docs: docs.bzlhub.com (scaffold pending)
- Self-hosting guide: [build-from-source.md](https://github.com/albertocavalcante/bzlhub/blob/main/docs/deployment/build-from-source.md)

## License

MIT. Built on [canopy](https://github.com/albertocavalcante/canopy).

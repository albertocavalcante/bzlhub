# Bzlhub roadmap

Last reconciled: 2026-07-26.

This is the current status tracker. The larger documents under `docs/plans/`
are design records, and `docs/plan.md` is the archived original phase plan.

## Implemented foundation

- BCR-compatible registry and filesystem mirror with recursive ingestion.
- SQLite search/index surfaces, module reports, diffs, drift, compatibility
  analysis, SCIP code navigation, and the SvelteKit UI.
- CLI, REST, stdio MCP, and opt-in Streamable HTTP MCP transports.
- Trusted-proxy and bearer identity, policy-backed MCP authorization,
  procurement workflows, request limits, and write-surface startup guards.
- Enforced deployment egress profiles (`default`, `mirror-only`,
  `sync-runner`) with allowlists and durable audit output.
- Pinned CI actions and container bases, Go module verification, UI tests,
  pinned `govulncheck`, UI dependency audit, and bundle-size budgets.
- Go 1.26.9 as the application and container-build baseline; module checksums
  are generated and reconciled by the Go toolchain.

The Go call-graph scan currently reports zero reachable vulnerabilities. The
Go vulnerability database still has a module-level notice for the unmaintained
`golang.org/x/crypto/openpgp` package. Bzlhub does not import that package, the
notice has no fixed version, and `govulncheck` reports it only in verbose
module results.

## Release blockers

These require external publication rather than more local source changes:

1. Publish the corrected sibling Go modules, replace the local development
   `replace` directives with immutable versions, run `go mod tidy`, and
   regenerate `vendor/`.
2. Build and publish the first `ghcr.io/albertocavalcante/bzlhub` image.
3. Set the Helm default image to the published immutable digest and verify an
   install against that image.

Do not remove `vendor/` before item 1: it is currently what makes a standalone
clone build while the module graph points at unpublished sibling revisions.

## Next product work

1. Run a real deployment soak with the closed policy profile, trusted proxy,
   egress audit file, SSE, and MCP clients.
2. Add an authenticated mechanism for the in-cluster ingest CronJob before
   treating that addon as suitable for a shared cluster.
3. Split remaining broad service interfaces at new consumer boundaries as
   features are touched; the MCP, sitemap, and head-tag consumers already use
   narrow capability interfaces.
4. Continue lazy-loading large UI feature areas if real-user performance data
   shows the current bundle budgets are insufficient.
5. Prioritize product features from the design archive only after attaching an
   owner, acceptance test, and deployment need here.

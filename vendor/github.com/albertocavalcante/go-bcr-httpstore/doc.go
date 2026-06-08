// Package httpstore is a pure-Go BCR-shape HTTP store client for
// any substrate that speaks HTTP (nginx, S3, R2, MinIO, GCS,
// Artifactory, GitHub raw, Forgejo raw).
//
// # Scope
//
// `httpstore` reads (and, in later versions, writes) the
// Bazel Central Registry on-disk shape over HTTP. A BCR-shape
// tree organises module metadata as:
//
//	<root>/modules/<name>/metadata.json
//	<root>/modules/<name>/<version>/MODULE.bazel
//	<root>/modules/<name>/<version>/source.json
//	<root>/modules/<name>/<version>/patches/<name>
//	<root>/blobs/sha256/<hex>                (optional, layout-dependent)
//
// The library is intentionally NOT canopy-specific — it knows
// nothing about canopy's internal types or features. canopy uses
// it as one of several backends; the library is equally usable
// by Bazel registry-mirroring CLIs, drift dashboards, supply-chain
// audit tools, or any service that wants BCR metadata from a
// static-file host.
//
// # Why this exists
//
// `go-bcr-mirror` is for git-backed registries (clone + sync).
// `httpstore` is for everything else: stores that don't have a
// git repo, only HTTP. The two libraries share the BCR on-disk
// shape contract but operate on different substrates.
//
// # The Layout problem
//
// Plain HTTP has no universal listing protocol. Different
// substrates expose "what modules are here?" through different
// means:
//
//   - nginx / Caddy autoindex: parse the HTML listing
//   - S3 / R2: list-bucket XML or a published index file
//   - Artifactory: vendor-specific JSON storage API
//
// The Layout interface lets callers choose. Default is
// `CanopyIndex` — read `_canopy_index.json` at the root. This is
// the most predictable shape and is what canopy auto-writes on
// publish.
//
// # Auth
//
// Auth is pluggable via the Auth interface. v0.0.1 ships only
// Anonymous (explicit "no auth" — operators must opt in so the
// audit log records the choice). Bearer, Basic, and CustomHeader
// land in later versions.
//
// # Stability
//
// Pre-v1.0 the public API is subject to change as the consumers
// (canopy first, then external callers) shake it out. Every break
// shows up in the CHANGELOG. Post-v1.0 follows semver.
//
// Full design: see canopy `docs/plans/43-go-bcr-httpstore-deep-design.md`.
package httpstore

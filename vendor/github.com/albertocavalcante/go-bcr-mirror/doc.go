// Package bcrmirror provides a pure-Go library for cloning, syncing,
// and reading a BCR-shape git registry mirror.
//
// The canonical upstream is github.com/bazelbuild/bazel-central-registry —
// a git repository where each module's metadata, MODULE.bazel,
// source.json, and patches live under modules/<n>/<v>/. This library
// wraps go-git/v5 to give consumers a typed, composition-friendly API
// for that repo shape.
//
// Use cases:
//   - Bazel registry servers that back metadata reads with a git clone
//     instead of HTTP polling (faster, atomic snapshots, full history).
//   - Drift detectors that compute "behind by N" via `git log` over
//     metadata.json files (cryptographically traceable; much faster than
//     per-module HTTP fetches).
//   - Airgap pipelines that periodically sync upstream into an internal
//     mirror, then serve from the internal mirror.
//
// All operations honour the caller's context.Context for cancellation
// and timeout. Operations that touch the network (Clone, Sync) return
// receipts with byte counts and durations so callers can emit their own
// audit-log entries.
//
// See README.md for a quick-start example. Full design at
// https://github.com/albertocavalcante/canopy/blob/main/docs/plans/31-m2-week5-and-library-extractions.md
package bcrmirror

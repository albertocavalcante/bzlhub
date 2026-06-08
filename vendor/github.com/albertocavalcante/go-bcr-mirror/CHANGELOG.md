# Changelog

All notable changes to `go-bcr-mirror` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.3] - 2026-05-31

### Added

- `Mirror.LastSyncReadErr()` accessor surfaces a non-fatal
  `LAST_SYNC` parse failure from `Mirror.Open`. Open still
  succeeds against a hand-edited or truncated file (seeds
  `lastSync` to zero); the accessor lets callers log a recovery
  warning out-of-band.

- Per-mirror advisory file lock at `<mirror>/.git/canopy.lock`
  guards concurrent `Clone` and `Sync` invocations. A second
  overlapping call returns the new `ErrLocked` sentinel rather
  than racing on the go-git worktree. Detection of an existing
  clone happens BEFORE lock acquisition so the idempotent
  `ErrAlreadyCloned` fast-path doesn't touch `.git/` mod times.

- PID-aware lock with orphan-holder takeover. The lock file
  stores the holder's PID; subsequent acquires probe the holder
  via POSIX `Signal(0)`. Dead-holder locks are atomically taken
  over (tmp file + rename + read-back race detection). Live-
  holder and unparseable-content locks return `ErrLocked`. A
  recovered takeover emits a `slog.Warn("bcrmirror: took over
  orphan lock", dead_pid, path)` so operators investigating the
  lock file's history get a forensic signal.

- `Mirror.Close()` releases the underlying `os.Root` file
  descriptor. Optional — Mirror works without Close (the fd
  lives until process exit), but long-running daemons opening
  many mirrors should call it.

### Changed

- Read-side filesystem operations in `read.go`
  (`ReadModuleMetadata`, `ReadSourceJSON`, `ReadModuleBazel`,
  `ReadPatch`, `ListModules`, `ListVersions`) now route through
  an `*os.Root` rooted at `Mirror.Path` (opened during
  `Mirror.Open`). Defense in depth against path-traversal: even
  if a future bug in `validateBCRSegment` misses a case, the
  kernel rejects opens that resolve outside the mirror
  directory. Pins down a class of attacks where a malicious
  upstream commit could check in a symlink to host-secret paths.

- `Mirror.Sync`'s up-to-date branch updates `lastSync` in
  addition to the advance branch. A quiet upstream that hasn't
  moved still counts as "operator confirmed upstream" for the
  staleness signal.

- Reduced internal duplication via Go 1.22's `cmp.Or` for option
  default-fallbacks (branch, clone timeout, sync timeout).
  `path.Join` (forward-slash) replaces `filepath.Join` in
  read-side internals since we operate against an `os.Root`
  with BCR's forward-slash convention. `slices.Sort` replaces
  `sort.Strings` in list output.

### Sentinel errors added

- `ErrLocked` — returned by `Clone` or `Sync` when the per-
  mirror advisory lock is held by another process (or a dead
  holder with unparseable content, see PID-aware takeover).

### Test infrastructure

- `Mirror.LastSync()` advancement tests converted to Go 1.24's
  `testing/synctest` — synthetic clock eliminates flaky
  `time.Sleep` reliance and runs ~15 simulated minutes of cadence
  testing in single-digit milliseconds of real time. 90/90 tests
  under `-race` after the round of additions.

## [0.1.2] - 2026-05-29

### Added

- `LAST_SYNC` persistence: `Mirror.Clone` and `Mirror.Sync`
  (advance and up-to-date branches) atomically write the lastSync
  time + HEAD SHA to `<mirror>/.git/LAST_SYNC`. `Mirror.Open`
  reads the file and seeds the in-memory state so `LastSync()`
  returns a meaningful value across process boundaries — closing
  the gap where a freshly-Open'd Mirror in a separate process
  would otherwise return zero even when the on-disk clone had
  been Sync'd recently.

  The file location (under `.git/`) follows git's own convention
  for state files (`HEAD`, `FETCH_HEAD`, etc.) so it doesn't
  pollute the working tree. JSON shape is stable for external
  consumers (ops tooling, federation staleness UI).

  Corrupt or missing `LAST_SYNC` is treated as "no record"; the
  Mirror behaves as if no prior sync happened. Defensive against
  hand-editing.

## [0.1.1] - 2026-05-29

### Added

- `Mirror.LastSync()` accessor returning the wall-clock time of the
  Mirror's most recent successful upstream contact. Drives downstream
  staleness signals (e.g. canopy's `DriftSummary.SyncedAt`).

### Fixed

- `Sync` now updates the internal `lastSync` timestamp on the
  up-to-date branch, not just on advance. An up-to-date probe is
  evidence the operator confirmed upstream; without this, a slow-
  moving upstream would leave `LastSync()` stuck at the most recent
  advance even with hourly invocations.

## [0.1.0] - 2026-05-29

Initial release.

### Added

- Public types, sentinel errors, and `Mirror` struct.
- Read API: `ReadModuleMetadata`, `ReadSourceJSON`, `ReadModuleBazel`,
  `ReadPatch`, `ListModules`, `ListVersions`.
- Sync API: `Clone`, `Sync`, `SnapshotSHA`, `IsClean`.
- Drift-aware reads: `LogChanges`, `MetadataAt`.
- `VerifyCommit` stub (full implementation targeted for v0.2.0).
- Synthetic testdata fixture (mini-registry with 3 modules).
- Name validation (`ErrInvalidName`) as a load-bearing path-traversal
  boundary at every public-API entry point that consumes a name as a
  path component.

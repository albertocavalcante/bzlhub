# Changelog

All notable changes to `go-bcr-mirror` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial scaffold of public types, sentinel errors, and Mirror struct.
- Read API: ReadModuleMetadata, ReadSourceJSON, ReadModuleBazel, ReadPatch, ListModules, ListVersions.
- Sync API: Clone, Sync, SnapshotSHA, IsClean.
- Drift-aware reads: LogChanges, MetadataAt.
- VerifyCommit stub (full implementation targeted for v0.2.0).
- Synthetic testdata fixture (mini-registry with 3 modules).

# Changelog

All notable changes to `go-bcr-bundle` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-06-02

**Ed25519 manifest signing — the reserved field from v0.0.1 is now
fully wired.** HQ signs at write; airgap verifies at Open. Operators
can pair with their existing key-management (HSM, KMS, on-disk PEM).

### Added

- **`Ed25519Signer{PrivateKey, KeyID}`** implements `Signer`.
  Pass to `WriteOptions.Signer`; the writer canonicalises the
  manifest with `signature=null`, calls `Sign`, stamps the
  resulting `Signature` onto the manifest, and re-canonicalises
  before emitting.

- **`Ed25519Verifier`** + `NewEd25519Verifier(map[string]ed25519.PublicKey)`
  implements `Verifier`. Multi-key map by design — operators rotate
  keys with overlap by adding the new pubkey + leaving the old in
  the verifier until all signed bundles drain. `KeyID` in the
  manifest signature dispatches to the matching pubkey.

- **`GenerateKey(name) (pub, priv, keyID, err)`** — convenience
  Ed25519 keypair generator. Derives a KeyID in
  `<name>+<8-hex-char-prefix>` format matching gosumdb's
  convention. Production deployments may prefer hardware-backed
  keys via a custom `Signer` implementation.

- **`Ed25519KeyID(name, pubKey) string`** — exported so operators
  computing KeyIDs externally (out-of-band key mint workflows) can
  match the same format.

- **`SignatureAlgorithmEd25519 = "ed25519"`** — the constant
  populated in `Signature.Algorithm` at write time + accepted by
  `Ed25519Verifier` at read time.

### Wired

- **`WriteOptions.Signer`** non-nil → manifest is signed. Was
  `ErrNotImplemented` at v0.0.1; now produces a signed bundle.

- **`OpenOptions.Verifier`** non-nil → manifest signature is
  verified. Was `ErrNotImplemented` at v0.0.1; now performs
  cryptographic verification.

### Behavior changes (read carefully)

- **Verifier configured + manifest.signature null** → returns
  `ErrSignatureInvalid`. Strip-signature-attack defense: an
  attacker who could mutate the manifest would otherwise be able
  to drop the signature and ship an "unsigned" bundle to a
  verifier-expecting consumer.

- **Verifier nil + manifest.signature populated** → bundle opens
  silently; signature is recorded in `Manifest()` but not
  validated. The library doesn't second-guess operator intent;
  if the operator didn't configure a Verifier, no verification
  happens.

- **Verification happens AFTER checksum verification** — a
  corrupt bundle fails on `ErrChecksumMismatch` first. Signing
  is an authenticity layer on top of integrity; both run.

### Failure modes (all surface as `ErrSignatureInvalid`)

- `Algorithm` not `"ed25519"` (unknown / unsupported algorithm)
- `KeyID` not in the verifier's keys map
- Cryptographic verification fails (key/signature/message mismatch)
- `Value` not valid base64

Errors wrap context (which KeyID, what algorithm, etc.) for log
lines; `errors.Is(err, ErrSignatureInvalid)` is the stable
predicate.

### Tests

15 new tests pushed total to 87 under `-race -count=1`:

- `GenerateKey` returns valid keypair + deterministic KeyID;
  empty name rejected.
- `Ed25519KeyID` deterministic from pubkey.
- `Ed25519Signer`: produces valid signature (verified via stdlib);
  rejects malformed key; rejects empty KeyID.
- `NewEd25519Verifier`: rejects empty map; rejects wrong-size keys.
- `Ed25519Verifier.Verify`: accepts valid signature; rejects
  tampered message; rejects unknown KeyID; rejects unknown
  algorithm; multi-key dispatch works for rotation overlap.
- End-to-end: `WriteBundle` with Signer → `Open` with Verifier
  round-trips; tampered signed bundle rejected.
- `Open` with Verifier on unsigned bundle → `ErrSignatureInvalid`
  (strip-signature defense).

### Migration

If you were relying on `WriteOptions.Signer` or
`OpenOptions.Verifier` rejection with `ErrNotImplemented`, your
code now reaches the actual signing path. If you were not
configuring those fields, nothing changes.

To start using signing:

```go
// HQ — once, at bootstrap:
pub, priv, keyID, _ := bundle.GenerateKey("hq-prod-2026")
// store priv securely; publish pub via side-channel (printed line,
// committed config file, etc).

// HQ — every bundle export:
bundle.WriteBundle(ctx, out, src, bundle.WriteOptions{
    Signer: bundle.Ed25519Signer{PrivateKey: priv, KeyID: keyID},
})

// Airgap — once, at bootstrap:
verifier, _ := bundle.NewEd25519Verifier(map[string]ed25519.PublicKey{
    keyID: pub,
})

// Airgap — every bundle import:
b, err := bundle.OpenWithOptions(f, bundle.OpenOptions{
    Verifier: verifier,
})
```

### Compatibility

| `go-bcr-bundle` | `go-bcr-httpstore` |
|---|---|
| v0.2.0 | ≥ v0.2.2 (unchanged) |

### Roadmap moved forward

The v0.0.1 design dossier's "deferred to v0.2.x" list (Ed25519
Signer + Verifier wiring) is now closed. Remaining roadmap:

- **v0.2.x** (potential): subset selection on writer; verbose
  Sign/Verify diagnostics for operator-facing tooling
- **v0.3.x**: CAS-native bundle v2 (canopy M6 alignment)
- **v1.0**: API stability + on-disk format `bcr-bundle/v1` frozen

## [0.1.0] - 2026-06-02

Adds **`BackendSource`** — bundle directly from a live
`httpstore.Backend` instead of a pre-synced filesystem mirror.

### Added

- **`BackendSource`** struct + **`NewBackendSource(*httpstore.Backend)`** constructor (nil backend → `ErrInvalidBundle`).
- **`BackendSource.Backend()`** accessor for diagnostics.
- **`BackendSource.List(ctx) ([]string, error)`** — walks the
  Backend's surface:
  - `bazel_registry.json` (always)
  - `modules/<m>/metadata.json` (per `Backend.ListModules`)
  - `modules/<m>/<v>/source.json` + `MODULE.bazel` (per `ListVersions`)
  - `modules/<m>/<v>/patches/<p>` (per source.json `patches` field)
  - `modules/<m>/<v>/overlay/<o>` (per source.json `overlay` field)
  - `blobs/<sha256-...>` (deduplicated across versions, derived from source.json `integrity` field)
  - Output is sorted.
- **`BackendSource.Open(ctx, relPath)`** — pattern-matches relPath
  to the appropriate typed `Backend.ReadX` method. Non-blob reads
  wrap the `[]byte` return in `io.NopCloser`; blobs return
  `Backend.ReadBlob`'s streaming `io.ReadCloser` unchanged.
  Returns `ErrNotFound` on a path that doesn't match any BCR-shape
  pattern.

### Behavior

- **BCR-aware Source.** Unlike `FilesystemSource` (which is
  format-agnostic and walks the filesystem), `BackendSource` knows
  BCR semantics: it parses `source.json` to extract patches /
  overlay / blob references. This is necessary because plain HTTP
  exposes no "list files under modules/<m>/<v>/" primitive.
- **Cache-friendly.** Reads route through the Backend's configured
  Cache (when set). Re-bundling immediately after a previous run
  is cheap on the upstream — configure a `httpstore.MemoryCache`
  on the Backend before bundling repeatedly.
- **Sequential.** v0.1.0 walks modules + versions sequentially.
  For a 1,143-module / 6,876-version real BCR mirror, that's
  ~7,000+ HTTP requests minimum on a cold cache. Parallelisation
  is a v0.1.x optimisation if profiling justifies it.
- **Malformed source.json fails loudly.** A `source.json` that
  doesn't parse surfaces as `ErrInvalidBundle` — callers can
  decide skip-and-warn vs fail-hard at a higher layer.
- **Unknown integrity algorithms skipped silently.** BCR's
  `source.json.integrity` is SRI-format
  (`<algorithm>-<base64>`). v0.1.0 recognises `sha256-` (the
  universal default); other algorithms produce no blob entry but
  don't fail the bundle. The manifest's per-file checksum layer
  covers integrity at the bundle level regardless.

### Tests

18 new tests pushed total to 72:

- Constructor: nil backend → `ErrInvalidBundle`; Backend accessor.
- List: includes bazel_registry.json; includes all modules + versions; derives patches from source.json; derives overlay; deduplicates blobs from integrity fields; returns sorted; rejects malformed source.json with `ErrInvalidBundle`.
- Open: bazel_registry.json, metadata.json, source.json,
  MODULE.bazel, patch, overlay, blob — each routes to the right
  Backend method; unrecognised path returns `ErrNotFound`.
- End-to-end: BackendSource → WriteBundle → Open → verify all
  modules / versions / blobs survived the round trip; spot-check
  Read content + ReadBlob streaming.

### Why now

`FilesystemSource` (v0.0.1) requires the operator to pre-sync the
mirror to local disk. For HQ workflows that don't already have a
disk-backed mirror, that's an extra step.
`BackendSource` lets canopy bundle directly from upstream (or any
configured BCR-shape HTTP backend) in one motion. The two sources
are complementary: pick whichever fits the operator's existing
state.

### Compatibility

| `go-bcr-bundle` | `go-bcr-httpstore` |
|---|---|
| v0.1.0 | ≥ v0.2.2 (unchanged from v0.0.1) |

## [0.0.2] - 2026-06-02

Corrections from a fresh-eyes pass over the v0.0.1 implementation
— before any consumer (canopy) actually invoked it. Three real
issues plus a small style cleanup.

### Fixed

- **Writer no longer buffers file bodies in memory.** v0.0.1's
  write pass did `io.ReadAll` on every file to learn its size
  before emitting the tar header. For a 500 MB source tarball
  blob that meant 500 MB of resident memory per file — fine for
  test BCR trees, catastrophic for a real mirror. v0.0.2 records
  sizes during the existing hash pass (which already streams via
  SHA-256) and streams bodies directly through `io.Copy` on the
  write pass. Memory footprint is now bounded by `io.Copy`'s
  32 KiB buffer regardless of file size.

- **`TestOpen_DetectsChecksumMismatch` implemented.** v0.0.1
  deliberately skipped this test, deferring the hand-built
  corrupt-bundle fixture. v0.0.2 builds the fixture by:
  (1) writing a normal bundle, (2) extracting it to a working
  dir, (3) tampering one file's bytes, (4) re-archiving the
  working dir into a new tar.gz that keeps the original
  (now-stale) manifest. `Open` correctly rejects with
  `ErrChecksumMismatch`. Defense-in-depth on integrity confirmed.

- **Dropped deprecated `tar.TypeRegA` reference.** The reader's
  non-regular-entry-skip check tested both `tar.TypeReg` and the
  historical `tar.TypeRegA` alias. `TypeRegA` was deprecated in
  Go 1.11 and isn't emitted by `archive/tar.Writer` or any tar
  implementation written in this decade. Dropped for clarity;
  no behavior change.

### Added

- **`TestWriteBundle_StreamsLargeBlobsWithoutBuffering`** — new
  test that injects a 1 MB synthetic blob into the sample BCR
  tree and asserts the writer's largest single `Read` call on
  it is ≤ 64 KiB (catching the buffer regression if it ever
  returns). The full 1 MB still transfers; just in 32 KiB
  chunks via `io.Copy`.

### Tests

54 tests under `-race -count=1`, all green, **zero skipped**.
The v0.0.1 deliberately-skipped slot is now closed.

### Why now and not later

Fresh-eyes review caught these before they'd hit canopy. The
streaming fix is particularly load-bearing: a 1.4 GB BCR mirror
(real number from `reference_canopy_bcrmirror_clone_cost.md`)
would have peaked at multi-GB RSS during bundling — easily
enough to OOM a small VPS. Catching this pre-canopy-integration
beats discovering it via a 3am page from the airgap export
cron job.

## [0.0.1] - 2026-06-02

First release. Airgap transport library for the canopy portfolio.

### Added

#### Format

- `bcr-bundle/v1` apiVersion: tar.gz archive containing
  `manifest.json` (first entry), `bazel_registry.json`,
  `modules/<m>/...`, and `blobs/<key>` content-addressable
  files.

- **`Manifest`** struct with `APIVersion`, `CreatedAt`,
  `CreatedBy`, `SourceURL`, `SourceCommit`, `Modules`, `Blobs`,
  `Checksums`, and optional `Signature` fields.

- **`EncodeManifest(m) ([]byte, error)`** — canonical JSON
  encoder: sorted map keys + sorted version slices + sorted blob
  array + no trailing newline. Determinism is required so the
  v0.2.x signature is stable across re-encodings.

- **`DecodeManifest(data) (Manifest, error)`** — validates
  apiVersion + schema, returns typed sentinels on failure.

#### Reader

- **`Bundle`** struct + **`Open(r io.Reader)`** constructor.
  Extracts to a tempdir; caller MUST `Close()` to clean up.
  `Open` reads `r` until EOF but does NOT close it — caller
  retains ownership.

- **`OpenOptions{SkipChecksums, Verifier}`** — `SkipChecksums`
  disables Open-time SHA-256 verification (default off; security-
  first). `Verifier` is reserved for v0.2.x; non-nil at v0.0.1
  returns `ErrNotImplemented`.

- **`Bundle.Read(ctx, relPath)`** — returns bytes at `relPath`.
  Returns `ErrNotFound` when absent.

- **`Bundle.ReadBlob(ctx, key)`** — streaming `io.ReadCloser`
  for blobs (typically hundreds of MB). Rejects keys containing
  path separators. Returns `ErrBlobNotFound`.

- **`Bundle.Layout()`** — returns `httpstore.Layout` backed by
  the manifest. Pair with `Bundle.Read` at a canopy-side adapter
  to get a complete BCR backend.

- **`Bundle.Manifest()`** — defensive copy of the parsed manifest.

- **`Bundle.ExtractedDir()`** — path to the tempdir for advanced
  consumers (e.g. `http.FileServer`). Contract: callers MUST NOT
  mutate the directory.

- **`Bundle.Close()`** — idempotent tempdir cleanup.

#### Writer

- **`Source`** interface — `List(ctx) ([]string, error)` +
  `Open(ctx, relPath) (io.ReadCloser, error)`. Narrow contract;
  no BCR-format knowledge in Source. The writer pattern-matches
  the path list to build manifest.modules + manifest.blobs.

- **`FilesystemSource`** — implements Source against a BCR-shape
  directory. Validates `bazel_registry.json` + `modules/` exist
  before returning. Skips hidden files (`.DS_Store`, etc.) and
  non-regular files (symlinks, devices).

- **`WriteBundle(ctx, w, src, opts)`** — assembles a bundle from
  Source and streams it to `w`. Two-pass: first computes SHA-256
  checksums + classifies paths into modules/blobs sections;
  second emits the tar.gz with manifest.json first, then content
  files.

- **`WriteOptions{SourceInfo, CreatedBy, Signer, Progress, CreatedAt}`** —
  `Signer` is reserved for v0.2.x; non-nil at v0.0.1 returns
  `ErrNotImplemented`. `Progress` callback fires per file.

#### Errors

7 sentinel errors:

- `ErrInvalidBundle` — corrupt archive, missing manifest, schema violation
- `ErrUnsupportedBundle` — apiVersion not recognised
- `ErrSignatureInvalid` — signature verification failed (v0.2.x)
- `ErrChecksumMismatch` — file content doesn't match manifest checksum
- `ErrNotFound` — relPath absent (Read)
- `ErrBlobNotFound` — blob key absent (ReadBlob)
- `ErrNotImplemented` — feature reserved for a future version

### Security

- SHA-256 per-file integrity verified at Open (default on;
  `SkipChecksums` opt-out for trusted flows).
- Loud-fail on `OpenOptions.Verifier` / `WriteOptions.Signer`
  non-nil at v0.0.1 — `ErrNotImplemented`.
- Tar extraction: skips non-regular entries (symlinks, devices)
  + path-traversal defense (entries that resolve outside dest
  rejected).

### Tests

52 tests under `-race -count=1`, all green. 1 deliberately
skipped (checksum-mismatch detection: full hand-built-tar
fixture deferred to v0.0.2). Coverage:

- Manifest: round-trip encode/decode, sorted keys + versions +
  blobs, no trailing newline, null/populated signature
  marshalling, apiVersion validation, schema violations.
- Source: constructor validation (empty / relative / missing
  dir / file-not-dir / missing bazel_registry.json / missing
  modules), `List` returns BCR paths only (hidden + non-BCR
  filtered), context cancellation, `Open` returns content +
  `ErrNotFound` on absent.
- Writer: rejects non-nil Signer with `ErrNotImplemented`,
  produces valid gzip + tar, manifest is first entry, manifest
  fields populated, modules + blobs reflect source, checksums
  match actual content, default CreatedBy when empty, Progress
  callback per file.
- Reader: rejects non-nil Verifier, round-trip integration,
  rejects invalid gzip, does NOT close reader, SkipChecksums
  opt-out wiring, Read returns content + ErrNotFound, Read on
  closed bundle errors, ReadBlob streams + ErrBlobNotFound +
  rejects path-sep keys, ExtractedDir valid, Close idempotent
  + removes tempdir.
- Layout: ListModules sorted, ListVersions sorted, unknown
  module errors.
- Concurrent Reads safe under `-race`.

### Compatibility

| `go-bcr-bundle` | `go-bcr-httpstore` |
|---|---|
| v0.0.1 | ≥ v0.2.2 |

### Roadmap (deferred)

- v0.0.2: corrections from first real use
- v0.1.x: BackendSource (live `httpstore.Backend`); subset selection
- v0.2.x: Ed25519 signing + Verifier dispatch
- v0.3.x: CAS-native bundle v2 (canopy M6 alignment)
- v1.0: API stability + format frozen forever

### Design dossier

Full architectural rationale at
`~/dev/md/2026-06-02-go-bcr-bundle-design/`. Re-review deltas
captured in `05-rereview-2026-06-02.md`.

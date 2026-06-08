# go-bcr-bundle

[![go.mod](https://img.shields.io/badge/go-1.26-blue)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/albertocavalcante/go-bcr-bundle/actions/workflows/ci.yml/badge.svg)](https://github.com/albertocavalcante/go-bcr-bundle/actions)

Airgap transport library for the canopy portfolio. Package a Bazel Central Registry (BCR) shape directory into a self-contained tar.gz bundle at HQ; physically transport to an airgapped facility; serve from the bundle at the airgap side.

Mode-A library #4 in the canopy portfolio.

## What it does

Bundles BCR-shape registries into a single transportable file with integrity guarantees, then opens them back at the airgap side.

```
HQ:  canopy mirror sync                          # pull BCR locally
     bundle.WriteBundle(out, src, opts)          # write bundle
     ── physical transfer ──→
Airgap: bundle.Open(f)                           # extract + verify
        bundle.Read(ctx, "modules/.../source.json")
        bundle.Layout() → httpstore.Layout
```

## Install

```bash
go get github.com/albertocavalcante/go-bcr-bundle@v0.0.1
```

## Quickstart

### HQ-side (export)

```go
src, err := bundle.NewFilesystemSource("/var/lib/canopy/mirror")
if err != nil {
    // ErrInvalidBundle if the directory isn't BCR-shape
    panic(err)
}
out, _ := os.Create("bcr-mirror-2026-06-02.tar.gz")
defer out.Close()

if err := bundle.WriteBundle(ctx, out, src, bundle.WriteOptions{
    CreatedBy:  "canopy v0.X.Y",
    SourceInfo: bundle.SourceInfo{URL: "https://bcr.bazel.build"},
}); err != nil {
    panic(err)
}
```

### Airgap-side (import + read)

```go
f, _ := os.Open("bcr-mirror-2026-06-02.tar.gz")
defer f.Close()

b, err := bundle.Open(f)  // extracts to tempdir, verifies checksums
if err != nil {
    panic(err)
}
defer b.Close()           // removes tempdir

// Read individual files
body, _ := b.Read(ctx, "modules/bazel_skylib/metadata.json")

// Stream blobs
rc, _ := b.ReadBlob(ctx, "sha256-abc123...")
defer rc.Close()
io.Copy(os.Stdout, rc)

// Or use as a httpstore.Layout for enumeration
layout := b.Layout()
modules, _ := layout.ListModules(ctx, nil)
```

## Bundle format

A bundle is a tar.gz archive with this layout:

```
<bundle>.tar.gz
├── manifest.json                    # bundle metadata + content index
├── bazel_registry.json              # BCR root marker
├── modules/                         # BCR-shape tree
│   ├── bazel_skylib/
│   │   ├── metadata.json
│   │   ├── 1.6.0/
│   │   │   ├── source.json
│   │   │   ├── MODULE.bazel
│   │   │   └── patches/...
│   │   └── 1.7.0/...
│   └── platforms/...
└── blobs/                           # content-addressable archives
    ├── sha256-abc123...
    └── sha256-def456...
```

`manifest.json` is the **first entry** in the tar — `tar -tzf bundle.tar.gz | head` shows it. Format:

```json
{
  "apiVersion": "bcr-bundle/v1",
  "createdAt":  "2026-06-02T12:00:00Z",
  "createdBy":  "canopy v0.X.Y",
  "sourceURL":  "https://bcr.bazel.build",
  "modules": {
    "bazel_skylib": ["1.6.0", "1.7.0"]
  },
  "blobs": [
    {"key": "sha256-abc...", "size": 12345}
  ],
  "checksums": {
    "bazel_registry.json":                              "sha256-...",
    "modules/bazel_skylib/metadata.json":               "sha256-...",
    "blobs/sha256-abc...":                              "sha256-..."
  },
  "signature": null
}
```

## Security model

| Layer | Status |
|---|---|
| SHA-256 per-file integrity (`Open` verifies all checksums) | **Shipped at v0.0.1** |
| Ed25519 manifest signature (authenticity, KeyID dispatch) | Reserved at v0.0.1 (manifest schema includes the field); fully wired at v0.2.x |

**Loud-fail on unimplemented security features:** passing a non-nil `OpenOptions.Verifier` or `WriteOptions.Signer` at v0.0.1 returns `ErrNotImplemented`. The library refuses to silently no-op security-relevant configuration.

See `~/dev/md/2026-06-02-go-bcr-bundle-design/03-security-integrity.md` for the full threat model and chain-of-custody story.

## Compatibility

| `go-bcr-bundle` | `go-bcr-httpstore` |
|---|---|
| v0.0.1 | ≥ v0.2.2 |

The library uses `httpstore.Layout` and `httpstore.ErrModuleNotFound` for the `Bundle.Layout()` integration.

## Status

**v0.0.1.** Pre-1.0 — the public API will shake out as canopy (the first consumer) drives it. Every break is recorded in [`CHANGELOG.md`](CHANGELOG.md). Post-v1.0 follows semver; the on-disk format `bcr-bundle/v1` is stable across all v0.x and v1.x releases.

## Roadmap

| Version | Scope |
|---|---|
| v0.0.1 | Reader + FilesystemSource writer + SHA-256 integrity ✅ |
| v0.0.2 | Corrections from first real use |
| v0.1.x | BackendSource (bundle from a live `httpstore.Backend`); subset selection |
| v0.2.x | Ed25519 signing + Verifier dispatch |
| v0.3.x | CAS-native bundle v2 (canopy M6 alignment) |
| v1.0 | API stability + format `bcr-bundle/v1` frozen forever |

## Design dossier

Full architectural rationale at `~/dev/md/2026-06-02-go-bcr-bundle-design/`:

- `README.md` — index + open questions
- `01-format-and-workflows.md` — bundle format, on-disk layout, manifest schema
- `02-public-api.md` — Bundle/Read/Layout/WriteBundle types and methods
- `03-security-integrity.md` — SHA-256 (v0.0.1) + Ed25519 (v0.2.x) + chain-of-custody
- `04-implementation-plan.md` — slice-by-slice plan
- `05-rereview-2026-06-02.md` — re-review deltas (smaller Source interface, drop verify-on-Read, loud-fail Verifier/Signer)

## License

[MIT](LICENSE) © 2026 Alberto Cavalcante

// Package bundle implements the airgap transport format for the
// canopy portfolio. A bundle is a self-contained tar.gz archive of
// a BCR-shape registry with a manifest providing integrity (and,
// at v0.2.x, authenticity) guarantees.
//
// # Workflow
//
// HQ-side (export):
//
//	srcDir := "/var/lib/canopy/mirror"  // synced via canopy mirror sync
//	src, err := bundle.NewFilesystemSource(srcDir)
//	if err != nil { /* handle */ }
//	out, _ := os.Create("bcr-mirror-2026-06-02.tar.gz")
//	defer out.Close()
//	if err := bundle.WriteBundle(ctx, out, src, bundle.WriteOptions{
//	    CreatedBy:  "canopy v0.X.Y",
//	    SourceInfo: bundle.SourceInfo{URL: "https://bcr.bazel.build"},
//	}); err != nil { /* handle */ }
//
// Operator transfers the file to the airgap (USB stick / approved
// DTL / sneakernet).
//
// Airgap-side (import + serve):
//
//	f, _ := os.Open("bcr-mirror-2026-06-02.tar.gz")
//	defer f.Close()
//	b, err := bundle.Open(f)
//	if err != nil { /* handle */ }
//	defer b.Close()  // removes tempdir
//
//	// Read content
//	body, _ := b.Read(ctx, "modules/bazel_skylib/metadata.json")
//
//	// Or use as an httpstore.Layout for enumeration
//	layout := b.Layout()
//	modules, _ := layout.ListModules(ctx, nil)
//
// # Format
//
// A bundle is a tar.gz with the following layout:
//
//	manifest.json                            (always first)
//	bazel_registry.json
//	modules/<module>/metadata.json
//	modules/<module>/<version>/source.json
//	modules/<module>/<version>/MODULE.bazel
//	modules/<module>/<version>/patches/<name>
//	modules/<module>/<version>/overlay/<path>
//	blobs/<sha256-key>
//
// The manifest declares the apiVersion ("bcr-bundle/v1"), the
// modules + versions map, the blob index with sizes, and a
// SHA-256 checksum for every other file. v0.2.x adds an optional
// Ed25519 signature.
//
// # Security model
//
// v0.0.1 ships integrity (SHA-256 checksums verified on Open).
// v0.2.x will add authenticity (Ed25519 signature on the manifest;
// verifier dispatched by KeyID for multi-key environments).
//
// At v0.0.1, passing a non-nil OpenOptions.Verifier or
// WriteOptions.Signer returns ErrNotImplemented — loud-fail
// beats silent-no-op for security-relevant fields.
//
// # Relationship to go-bcr-httpstore
//
// Bundle.Layout() returns a httpstore.Layout that enumerates
// modules and versions backed by the manifest. Content reads
// stay on Bundle.Read / Bundle.ReadBlob (Bundle does not
// implement httpstore.Reader — that interface is too narrow).
//
// canopy's bundle adapter is the consumer-side abstraction that
// unifies *Bundle and *httpstore.Backend behind canopy's
// internal Backend interface.
//
// # Design dossier
//
// Full design rationale at
// ~/dev/md/2026-06-02-go-bcr-bundle-design/.
package bundle

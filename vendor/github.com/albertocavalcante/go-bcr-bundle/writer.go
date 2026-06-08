package bundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// SourceInfo is metadata the Source can optionally provide for
// the manifest's audit trail. Zero value is valid.
type SourceInfo struct {
	URL    string // upstream the bundle was sourced from (e.g. https://bcr.bazel.build)
	Commit string // commit SHA when the source has a commit identity
}

// Signer signs the canonicalised manifest bytes for Ed25519
// authenticity (v0.2.x). Reserved at v0.0.1 — passing a non-nil
// Signer to WriteOptions returns ErrNotImplemented.
type Signer interface {
	Sign(canonicalManifest []byte) (Signature, error)
}

// ProgressFunc is the callback shape for WriteOptions.Progress.
// relPath is the path inside the bundle the writer just emitted;
// bytes is its size. Called sequentially during WriteBundle.
type ProgressFunc func(relPath string, bytes int64)

// WriteOptions configures bundle writing. Zero value is valid;
// CreatedBy defaults to "go-bcr-bundle v0.0.1" when empty.
type WriteOptions struct {
	// SourceInfo populates the manifest's sourceURL + sourceCommit.
	SourceInfo SourceInfo

	// CreatedBy populates manifest.createdBy. Operators typically
	// set this to "<tool> <version>" (e.g. "canopy v0.X.Y").
	CreatedBy string

	// Signer, when non-nil, signs the manifest with Ed25519
	// (v0.2.x). v0.0.1 rejects non-nil Signer with
	// ErrNotImplemented — loud-fail beats silent-no-op for
	// security-relevant fields.
	Signer Signer

	// Progress, when non-nil, receives per-file callbacks as the
	// writer emits each file. Useful for operator UIs (canopy's
	// "bundle export" command wires a progress bar against this).
	Progress ProgressFunc

	// CreatedAt overrides the default (now-UTC) on the manifest.
	// Useful for deterministic tests and for backdating bundles
	// produced from snapshots taken earlier.
	CreatedAt time.Time
}

const defaultCreatedBy = "go-bcr-bundle v0.0.1"

// WriteBundle assembles a bundle from src and writes it to w.
// The bundle contains:
//
//   - manifest.json (emitted first for inspection convenience)
//   - bazel_registry.json
//   - modules/* (per src.List)
//   - blobs/* (per src.List)
//
// Streaming: the writer doesn't buffer file contents in memory;
// memory footprint is bounded by the largest single file, not
// total bundle size.
//
// When opts.Signer is non-nil (v0.2.x+), the manifest is signed
// after the checksums table is populated: the writer canonicalises
// the manifest with signature=null, asks Signer.Sign for an
// Ed25519 signature over those bytes, stamps the result onto
// manifest.signature, and re-canonicalises before emitting.
// Operators at the airgap pair with an OpenOptions.Verifier
// configured with the corresponding public key.
func WriteBundle(ctx context.Context, w io.Writer, src Source, opts WriteOptions) error {
	paths, err := src.List(ctx)
	if err != nil {
		return fmt.Errorf("bundle: list source: %w", err)
	}
	sort.Strings(paths)

	// First pass: assemble Manifest from the path list. This walks
	// the source twice (once to list + checksum, once to write) —
	// acceptable for v0.0.1 since checksum computation requires a
	// full read of every file anyway.
	manifest := Manifest{
		APIVersion:   APIVersion,
		CreatedAt:    opts.createdAt(),
		CreatedBy:    opts.createdBy(),
		SourceURL:    opts.SourceInfo.URL,
		SourceCommit: opts.SourceInfo.Commit,
		Modules:      map[string][]string{},
		Blobs:        []BlobEntry{},
		Checksums:    map[string]string{},
	}

	// Streaming checksum + manifest population. Each file is read
	// twice (once here for the checksum, once below for writing
	// into the tar) — necessary because the manifest must be
	// emitted FIRST into the archive, and checksums must be known
	// at manifest-write time.
	//
	// Sizes computed here are stored in `sizes` so the write pass
	// can set tar headers without buffering bodies in memory (a
	// large blob would otherwise eat hundreds of MB during write).
	sizes := make(map[string]int64, len(paths))
	for _, rel := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		size, sum, err := hashSourceFile(ctx, src, rel)
		if err != nil {
			return fmt.Errorf("bundle: hash %s: %w", rel, err)
		}
		sizes[rel] = size
		manifest.Checksums[rel] = sum
		classify(&manifest, rel, size)
	}

	manifestBytes, err := EncodeManifest(manifest)
	if err != nil {
		return err
	}

	// If a Signer is configured, sign the canonical-with-null-
	// signature manifest bytes, stamp the signature onto the
	// manifest, and re-encode. Order matters: signing happens
	// after all checksums + modules + blobs are populated so the
	// signature covers the full manifest content.
	if opts.Signer != nil {
		sig, err := opts.Signer.Sign(manifestBytes)
		if err != nil {
			return fmt.Errorf("bundle: sign manifest: %w", err)
		}
		manifest.Signature = &sig
		manifestBytes, err = EncodeManifest(manifest)
		if err != nil {
			return fmt.Errorf("bundle: re-encode signed manifest: %w", err)
		}
	}

	// Now write the tar.gz. manifest.json first, then every
	// content file in sorted order.
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// 1. manifest.json
	if err := writeTarFile(tw, "manifest.json", int64(len(manifestBytes)), strings.NewReader(string(manifestBytes)), manifest.CreatedAt); err != nil {
		return fmt.Errorf("bundle: write manifest entry: %w", err)
	}
	if opts.Progress != nil {
		opts.Progress("manifest.json", int64(len(manifestBytes)))
	}

	// 2. every content file.
	//
	// Streaming: size is known from the hash pass, so we set the
	// tar header up front and io.Copy the body straight from
	// src.Open's reader into the tar writer. Memory footprint is
	// bounded by io.Copy's 32 KiB buffer, NOT by total file size.
	// A 500 MB source tarball blob streams through in 32 KiB
	// chunks; never buffered whole.
	for _, rel := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		size := sizes[rel]
		rc, err := src.Open(ctx, rel)
		if err != nil {
			return fmt.Errorf("bundle: open %s: %w", rel, err)
		}
		if err := writeTarFile(tw, rel, size, rc, manifest.CreatedAt); err != nil {
			_ = rc.Close()
			return fmt.Errorf("bundle: write %s: %w", rel, err)
		}
		_ = rc.Close()
		if opts.Progress != nil {
			opts.Progress(rel, size)
		}
	}

	// archive/tar's Close flushes the trailing 1024-byte zero block.
	if err := tw.Close(); err != nil {
		return fmt.Errorf("bundle: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("bundle: close gzip: %w", err)
	}
	return nil
}

// createdAt returns opts.CreatedAt if non-zero, else time.Now() UTC.
func (o WriteOptions) createdAt() time.Time {
	if !o.CreatedAt.IsZero() {
		return o.CreatedAt.UTC()
	}
	return time.Now().UTC()
}

// createdBy returns opts.CreatedBy if non-empty, else the library's
// default identification string.
func (o WriteOptions) createdBy() string {
	if o.CreatedBy != "" {
		return o.CreatedBy
	}
	return defaultCreatedBy
}

// hashSourceFile streams a file from src through SHA-256 to compute
// its checksum + byte count without buffering the whole file in
// memory.
func hashSourceFile(ctx context.Context, src Source, relPath string) (int64, string, error) {
	rc, err := src.Open(ctx, relPath)
	if err != nil {
		return 0, "", err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return 0, "", err
	}
	return n, "sha256-" + hex.EncodeToString(h.Sum(nil)), nil
}

// classify inspects a BCR-shape relative path and updates the
// manifest's modules + blobs sections accordingly.
//
// Path patterns:
//
//	bazel_registry.json                  → no manifest section update
//	modules/<m>/metadata.json            → register module m
//	modules/<m>/<v>/<anything>           → register module m + version v
//	blobs/<key>                          → register blob with size
func classify(m *Manifest, relPath string, size int64) {
	switch {
	case relPath == "bazel_registry.json":
		// Manifest doesn't list bazel_registry.json explicitly; it's
		// always at the root. Only the checksum entry covers it.
		return
	case strings.HasPrefix(relPath, "modules/"):
		// Strip the "modules/" prefix and split into segments.
		rest := strings.TrimPrefix(relPath, "modules/")
		segs := strings.SplitN(rest, "/", 3)
		if len(segs) == 0 || segs[0] == "" {
			return
		}
		moduleName := segs[0]
		if _, ok := m.Modules[moduleName]; !ok {
			m.Modules[moduleName] = []string{}
		}
		if len(segs) >= 2 && segs[1] != "" && segs[1] != "metadata.json" {
			// segs[1] is the version directory. Register it on
			// the module's version list (deduped).
			version := segs[1]
			versions := m.Modules[moduleName]
			if !containsStr(versions, version) {
				m.Modules[moduleName] = append(versions, version)
			}
		}
	case strings.HasPrefix(relPath, "blobs/"):
		key := strings.TrimPrefix(relPath, "blobs/")
		if key == "" {
			return
		}
		m.Blobs = append(m.Blobs, BlobEntry{Key: key, Size: size})
	}
}

// writeTarFile emits one file into the tar stream.
func writeTarFile(tw *tar.Writer, name string, size int64, body io.Reader, mtime time.Time) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    size,
		ModTime: mtime,
		Format:  tar.FormatPAX, // Pax for clean UTF-8 filename handling
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := io.Copy(tw, body); err != nil {
		return err
	}
	return nil
}

// containsStr reports whether haystack contains needle.
func containsStr(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

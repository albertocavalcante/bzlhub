package bundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/albertocavalcante/go-bcr-httpstore"
)

// Bundle is an opened BCR bundle. The zero value is invalid;
// construct via Open. Bundle is safe for concurrent use by
// multiple goroutines after Open returns (except for Close,
// which is not safe to call concurrent with active Reads).
//
// Open extracts the bundle's contents to a tempdir. Close removes
// the tempdir; callers MUST Close when done to avoid leaking
// disk space.
type Bundle struct {
	extractDir string
	manifest   Manifest

	mu     sync.Mutex
	closed bool
}

// Verifier validates Manifest.Signature against the canonicalised
// manifest bytes (v0.2.x). At v0.0.1, passing a non-nil Verifier
// to OpenOptions returns ErrNotImplemented.
type Verifier interface {
	Verify(canonicalManifest []byte, sig Signature) error
}

// OpenOptions configures Open. Zero value is valid.
type OpenOptions struct {
	// SkipChecksums disables the Open-time SHA-256 verification
	// pass. Designed for trusted internal flows where verification
	// overhead matters (large bundles + tight reopen loops).
	// Default false — verification is on for safety.
	SkipChecksums bool

	// Verifier, when non-nil, validates Manifest.Signature
	// (v0.2.x). v0.0.1 rejects non-nil Verifier with
	// ErrNotImplemented — loud-fail beats silent-no-op for
	// security-relevant configuration.
	Verifier Verifier
}

// Open reads the bundle from r, extracts to a tempdir, parses
// the manifest, and verifies all checksums. Returns *Bundle on
// success; the caller MUST Close() when done.
//
// r is read until EOF on Open. Open does NOT close r — the
// caller retains ownership and is responsible for closing it
// after Open returns (typically *os.File closed via defer).
//
// Errors:
//   - ErrInvalidBundle: corrupt archive, missing manifest, schema
//     violation in manifest, checksum mismatch
//   - ErrUnsupportedBundle: manifest.apiVersion isn't recognised
//   - ErrNotImplemented: OpenOptions.Verifier is non-nil at v0.0.1
func Open(r io.Reader) (*Bundle, error) {
	return OpenWithOptions(r, OpenOptions{})
}

// OpenWithOptions is the explicit form. Open(r) is equivalent to
// OpenWithOptions(r, OpenOptions{}).
func OpenWithOptions(r io.Reader, opts OpenOptions) (*Bundle, error) {
	extractDir, err := os.MkdirTemp("", "go-bcr-bundle-*")
	if err != nil {
		return nil, fmt.Errorf("bundle: create tempdir: %w", err)
	}
	// Ensure tempdir is cleaned up on any error path before we
	// return a *Bundle that the caller would otherwise Close.
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(extractDir)
		}
	}()

	if err := extractTarGz(r, extractDir); err != nil {
		return nil, err
	}

	manifestBytes, err := os.ReadFile(filepath.Join(extractDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: read manifest.json: %v", ErrInvalidBundle, err)
	}
	manifest, err := DecodeManifest(manifestBytes)
	if err != nil {
		return nil, err
	}

	if !opts.SkipChecksums {
		if err := verifyChecksums(extractDir, manifest); err != nil {
			return nil, err
		}
	}

	// Signature verification — runs after integrity verification
	// because a bundle that fails integrity is corrupt regardless
	// of signature, and the integrity check is the cheaper failure
	// for early-exit on corruption.
	//
	// Verifier configured + manifest.signature populated → verify.
	// Verifier configured + manifest.signature nil → REJECT
	// (an attacker could otherwise strip the signature and submit
	// an unsigned bundle to a verifier-expecting consumer).
	// Verifier nil → no check (bundle's signature, if any, is
	// recorded in the manifest but not validated).
	if opts.Verifier != nil {
		if manifest.Signature == nil {
			return nil, fmt.Errorf("%w: Verifier configured but manifest.signature is null (bundle is unsigned)",
				ErrSignatureInvalid)
		}
		// Canonicalise the manifest with signature=nil so the
		// signature covers the same byte sequence the Signer
		// originally signed. The signature field's own bytes
		// CAN'T be in the signed input — that would be a chicken-
		// and-egg.
		mc := manifest
		mc.Signature = nil
		canonical, err := EncodeManifest(mc)
		if err != nil {
			return nil, fmt.Errorf("bundle: re-canonicalise for verification: %w", err)
		}
		if err := opts.Verifier.Verify(canonical, *manifest.Signature); err != nil {
			return nil, err
		}
	}

	success = true
	return &Bundle{extractDir: extractDir, manifest: manifest}, nil
}

// Close releases the bundle's extraction tempdir. Safe to call
// multiple times (subsequent calls return nil).
func (b *Bundle) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if err := os.RemoveAll(b.extractDir); err != nil {
		return fmt.Errorf("bundle: remove tempdir: %w", err)
	}
	return nil
}

// Manifest returns a copy of the parsed manifest. Defensive copy
// of the map fields so callers can't mutate the bundle's internal
// state.
func (b *Bundle) Manifest() Manifest {
	cp := b.manifest
	if b.manifest.Modules != nil {
		cp.Modules = make(map[string][]string, len(b.manifest.Modules))
		for k, vs := range b.manifest.Modules {
			cp.Modules[k] = append([]string(nil), vs...)
		}
	}
	if b.manifest.Blobs != nil {
		cp.Blobs = append([]BlobEntry(nil), b.manifest.Blobs...)
	}
	if b.manifest.Checksums != nil {
		cp.Checksums = make(map[string]string, len(b.manifest.Checksums))
		for k, v := range b.manifest.Checksums {
			cp.Checksums[k] = v
		}
	}
	return cp
}

// ExtractedDir returns the tempdir where bundle contents live.
// Exposed for advanced consumers that want to read directly via
// os.Open (e.g. an http.FileServer for performance-critical
// serving). Most callers should use Read / ReadBlob instead.
//
// CONTRACT: callers MUST NOT mutate the directory or its
// contents. Open verifies integrity once; subsequent Reads trust
// the on-disk state.
func (b *Bundle) ExtractedDir() string { return b.extractDir }

// Read returns the bytes of relPath within the bundle. relPath
// is in BCR-shape (e.g. "modules/bazel_skylib/metadata.json",
// "bazel_registry.json"). Use ReadBlob for streaming reads of
// content-addressable blobs.
//
// Returns ErrNotFound when relPath isn't present.
func (b *Bundle) Read(ctx context.Context, relPath string) ([]byte, error) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("bundle: Read on closed Bundle")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	body, err := os.ReadFile(filepath.Join(b.extractDir, filepath.FromSlash(relPath)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, relPath)
		}
		return nil, fmt.Errorf("bundle: read %s: %w", relPath, err)
	}
	return body, nil
}

// ReadBlob streams a blob by key from <bundle>/blobs/<key>.
// Mirrors httpstore.Backend.ReadBlob — caller MUST Close the
// returned ReadCloser. Streaming because blobs can be hundreds
// of MB.
//
// Returns ErrBlobNotFound on unknown key.
func (b *Bundle) ReadBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("bundle: ReadBlob on closed Bundle")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if strings.ContainsAny(key, `/\`) {
		// Same defensive check as httpstore.Backend.ReadBlob —
		// blob keys are opaque content-addresses; embedded path
		// separators are always wrong.
		return nil, fmt.Errorf("%w: %s (key contains path separators)", ErrBlobNotFound, key)
	}
	f, err := os.Open(filepath.Join(b.extractDir, "blobs", key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, key)
		}
		return nil, fmt.Errorf("bundle: open blob %s: %w", key, err)
	}
	return f, nil
}

// Layout returns a httpstore.Layout that enumerates this bundle's
// modules and versions backed by Manifest. Pair with Bundle.Read
// at a canopy-side adapter to get a complete BCR backend.
//
// The Layout's ListModules / ListVersions read from the manifest
// directly — no I/O, no upstream call.
func (b *Bundle) Layout() httpstore.Layout {
	return bundleLayout{manifest: b.Manifest()}
}

// bundleLayout implements httpstore.Layout against an in-memory
// Manifest. Safe for concurrent use (manifest is immutable after
// Open).
type bundleLayout struct {
	manifest Manifest
}

// Compile-time guard.
var _ httpstore.Layout = bundleLayout{}

// ListModules returns module names sorted.
func (l bundleLayout) ListModules(_ context.Context, _ httpstore.Reader) ([]string, error) {
	names := make([]string, 0, len(l.manifest.Modules))
	for name := range l.manifest.Modules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// ListVersions returns sorted version names for a module. Returns
// httpstore.ErrModuleNotFound when the module is absent.
func (l bundleLayout) ListVersions(_ context.Context, _ httpstore.Reader, module string) ([]string, error) {
	versions, ok := l.manifest.Modules[module]
	if !ok {
		return nil, fmt.Errorf("%w: %s", httpstore.ErrModuleNotFound, module)
	}
	out := append([]string(nil), versions...)
	sort.Strings(out)
	return out, nil
}

// ContentPathPrefix returns "" — bundles serve BCR-shape content
// directly under BaseURL when fronted by the bundle reader, no
// vendor-specific prefix needed. Added in httpstore v0.3 / bundle
// v0.2.1.
func (bundleLayout) ContentPathPrefix() string { return "" }

// ---- internal helpers --------------------------------------------

// extractTarGz reads a gzipped tar from r and writes every file
// to dest. Skips directory entries (we create parents on demand
// from file paths). Skips non-regular entries (defense against
// tar-bomb / symlink-attack surfaces).
//
// Performs path-traversal defense: any entry whose normalised
// path escapes dest is rejected.
func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("%w: gzip reader: %v", ErrInvalidBundle, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("bundle: abs dest: %w", err)
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: tar.Next: %v", ErrInvalidBundle, err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		// Skip non-regular entries (symlinks, devices) — defense
		// against tar-bomb escapes. TypeReg covers regular files;
		// the historical TypeRegA alias was deprecated in Go 1.11
		// and isn't emitted by archive/tar.Writer (or by any tar
		// implementation written in this decade).
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Path-traversal defense: every entry must resolve under
		// destAbs after Clean.
		clean := filepath.Clean(filepath.Join(destAbs, filepath.FromSlash(hdr.Name)))
		if !strings.HasPrefix(clean+string(filepath.Separator), destAbs+string(filepath.Separator)) && clean != destAbs {
			return fmt.Errorf("%w: tar entry %q escapes destination", ErrInvalidBundle, hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			return fmt.Errorf("bundle: mkdir for %s: %w", hdr.Name, err)
		}
		out, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("bundle: create %s: %w", hdr.Name, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return fmt.Errorf("bundle: write %s: %w", hdr.Name, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("bundle: close %s: %w", hdr.Name, err)
		}
	}
	return nil
}

// verifyChecksums hashes every file listed in manifest.Checksums
// and compares to the expected value. Returns
// ErrChecksumMismatch on first divergence (wrapped with the
// offending path).
func verifyChecksums(extractDir string, m Manifest) error {
	for relPath, want := range m.Checksums {
		full := filepath.Join(extractDir, filepath.FromSlash(relPath))
		f, err := os.Open(full)
		if err != nil {
			return fmt.Errorf("%w: open %s: %v", ErrInvalidBundle, relPath, err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return fmt.Errorf("%w: hash %s: %v", ErrInvalidBundle, relPath, err)
		}
		_ = f.Close()
		got := "sha256-" + hex.EncodeToString(h.Sum(nil))
		if got != want {
			return fmt.Errorf("%w: %s (got %s, want %s)",
				ErrChecksumMismatch, relPath, got, want)
		}
	}
	return nil
}

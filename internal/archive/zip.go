package archive

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractZip materializes a zip archive into destDir with the same
// semantics as ExtractTarGz: stripPrefix is removed from entry paths,
// out-of-tree paths are rejected, symlinks are skipped, returns total
// bytes written. maxBytes caps the cumulative write; <= 0 disables
// the cap (test fixtures only). See archive.MaxExtractBytes for the
// production default + threat-model rationale.
//
// Unlike tar.gz, zip requires random access. We buffer the entire
// archive into memory first so we can hand archive/zip an io.ReaderAt.
// BCR source archives are typically <100MB, so the cost is acceptable
// for canopy's batch use cases (ingest, what-if diff, closure diff).
// The input-read cap (maxBytes) doubles as a buffering bound: a
// pathological zip declaring TB-class uncompressed sizes from a
// small compressed blob (zip bomb) still can't slip past since the
// per-entry LimitReader rejects mid-write.
func ExtractZip(r io.Reader, destDir, stripPrefix string, maxBytes int64) (int64, error) {
	// Cap the input read too — a 10TB zip would otherwise exhaust
	// memory before extraction even starts.
	if maxBytes > 0 {
		r = io.LimitReader(r, maxBytes+1)
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("buffer zip: %w", err)
	}
	if maxBytes > 0 && int64(len(buf)) > maxBytes {
		return 0, fmt.Errorf("%w: compressed zip exceeded %d bytes", ErrExtractTooLarge, maxBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return 0, fmt.Errorf("open zip: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir dest: %w", err)
	}
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return 0, err
	}

	var total int64
	for _, f := range zr.File {
		name := stripped(f.Name, stripPrefix)
		if name == "" {
			continue
		}
		// Same pre-Join hardening as ExtractTarGz: absolute paths
		// would be silently confined by filepath.Join; parent-dir
		// segments would escape. Reject both explicitly so unsafe
		// archives surface a clear error.
		if strings.HasPrefix(name, "/") {
			return total, fmt.Errorf("entry %q is absolute", f.Name)
		}
		for seg := range strings.SplitSeq(name, "/") {
			if seg == ".." {
				return total, fmt.Errorf("entry %q has parent-dir segment", f.Name)
			}
		}
		outPath := filepath.Join(destAbs, name)
		if !strings.HasPrefix(outPath, destAbs+string(os.PathSeparator)) && outPath != destAbs {
			return total, fmt.Errorf("entry %q escapes dest dir", f.Name)
		}

		// Zip entries can declare directories with a trailing slash or
		// just leave them implicit. Either way, MkdirAll the parent
		// before writing — entries can arrive in any order.
		mode := f.Mode()
		if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(outPath, 0o755); err != nil {
				return total, fmt.Errorf("mkdir %s: %w", outPath, err)
			}
			continue
		}
		if mode&os.ModeSymlink != 0 {
			// Same risk/benefit calculus as tar: skip symlinks.
			continue
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return total, fmt.Errorf("mkdir parent of %s: %w", outPath, err)
		}
		rc, err := f.Open()
		if err != nil {
			return total, fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		// Per-entry budget — same shape as the tar path. A single
		// entry declaring a huge UncompressedSize64 (zip bomb) still
		// bails at the cap instead of writing 10TB.
		src := io.Reader(rc)
		var capped bool
		if maxBytes > 0 {
			remaining := maxBytes - total
			if remaining <= 0 {
				_ = rc.Close()
				return total, fmt.Errorf("%w: budget exhausted at entry %q", ErrExtractTooLarge, f.Name)
			}
			src = io.LimitReader(rc, remaining+1)
			capped = true
		}
		n, werr := writeFile(outPath, src, zipFileMode(mode))
		_ = rc.Close()
		total += n
		if werr != nil {
			return total, werr
		}
		if capped && total > maxBytes {
			return total, fmt.Errorf("%w: %d > %d at entry %q", ErrExtractTooLarge, total, maxBytes, f.Name)
		}
	}
	return total, nil
}

// zipFileMode normalizes a zip-declared file mode to something sensible.
// Many zip producers leave mode bits empty (0); fall back to 0644 in
// that case so we don't write unreadable files.
func zipFileMode(m os.FileMode) os.FileMode {
	perm := m.Perm()
	if perm == 0 {
		perm = 0o644
	}
	return perm
}

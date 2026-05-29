// Package archive extracts source archives (tar.gz primarily) into a
// destination directory while honoring Bazel's strip_prefix convention and
// rejecting path traversal.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxExtractBytes caps total bytes written across one ExtractTarGz /
// ExtractZip call. Bazel-module tarballs in practice top out under
// 100MB (bazel itself is ~50MB; rules_jvm_external_test_data and a
// few other corpus outliers sit near that ceiling). 500MB is the
// guard rail against decompression bombs — a compromised upstream
// serving an archive that matches the integrity hash on a small
// header but declares a 1TB body would otherwise fill disk before
// resolve's post-extract Verify rejects.
//
// Callers may pass 0 to disable the cap; only test fixtures should.
const MaxExtractBytes = 500 * 1024 * 1024

// ErrExtractTooLarge is returned when an extraction exceeds the
// caller-supplied max-bytes budget. Sentinel so callers can
// errors.Is and distinguish "archive bomb" from generic IO failure.
var ErrExtractTooLarge = errors.New("archive: extraction exceeded max bytes")

// ExtractTarGz materializes the tar.gz stream into destDir. stripPrefix is
// trimmed from each entry's path (GitHub auto-tarballs put everything under
// "<name>-<ref>/", which BCR's source.json strip_prefix points at).
//
// All output paths are confined to destDir via filepath.Clean + a HasPrefix
// check. Symlinks pointing outside destDir are skipped silently. The
// cumulative byte count is capped at maxBytes; the function aborts with
// ErrExtractTooLarge as soon as the budget is exceeded so a malicious
// archive can't blow past the limit even within one large entry.
// maxBytes <= 0 disables the cap (test fixtures only).
//
// Returns the number of bytes written across all extracted files.
func ExtractTarGz(r io.Reader, destDir, stripPrefix string, maxBytes int64) (int64, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	return extractTar(gz, destDir, stripPrefix, maxBytes)
}

func extractTar(r io.Reader, destDir, stripPrefix string, maxBytes int64) (int64, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir dest: %w", err)
	}
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(r)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, fmt.Errorf("tar header: %w", err)
		}

		name := stripped(hdr.Name, stripPrefix)
		if name == "" {
			continue // entry was the prefix dir itself or below the strip
		}
		// Reject obviously unsafe paths before filepath.Join collapses
		// them. Leading "/" makes Join drop the destination prefix on
		// some platforms; ".." segments could escape the dest dir.
		// Catching both here surfaces a clear error instead of
		// silently writing $dest/etc/shadow when the tarball
		// declared /etc/shadow.
		if strings.HasPrefix(name, "/") {
			return total, fmt.Errorf("entry %q is absolute", hdr.Name)
		}
		for seg := range strings.SplitSeq(name, "/") {
			if seg == ".." {
				return total, fmt.Errorf("entry %q has parent-dir segment", hdr.Name)
			}
		}
		// Defense in depth: clean + ensure result stays under destAbs.
		outPath := filepath.Join(destAbs, name)
		if !strings.HasPrefix(outPath, destAbs+string(os.PathSeparator)) && outPath != destAbs {
			return total, fmt.Errorf("entry %q escapes dest dir", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(outPath, 0o755); err != nil {
				return total, fmt.Errorf("mkdir %s: %w", outPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return total, fmt.Errorf("mkdir parent of %s: %w", outPath, err)
			}
			// Per-entry budget: cap io.Copy at remaining=maxBytes-total.
			// LimitReader returns short on overflow; writeFile sees EOF;
			// we then check whether the cap was hit and surface
			// ErrExtractTooLarge. A header declaring a 10TB single file
			// can write at most (maxBytes-total) bytes before bailing.
			src := io.Reader(tr)
			var capped bool
			if maxBytes > 0 {
				remaining := maxBytes - total
				if remaining <= 0 {
					return total, fmt.Errorf("%w: budget exhausted at entry %q", ErrExtractTooLarge, hdr.Name)
				}
				src = io.LimitReader(tr, remaining+1) // +1 so overflow is detectable
				capped = true
			}
			n, err := writeFile(outPath, src, modeOf(hdr))
			total += n
			if err != nil {
				return total, err
			}
			if capped && total > maxBytes {
				return total, fmt.Errorf("%w: %d > %d at entry %q", ErrExtractTooLarge, total, maxBytes, hdr.Name)
			}
		case tar.TypeSymlink, tar.TypeLink:
			// Skip — security risk for the small benefit. Bazel modules
			// rarely depend on symlinks in the source archive itself.
			continue
		default:
			// Skip block/char/fifo etc. — never relevant for source archives.
			continue
		}
	}
	return total, nil
}

// writeFile creates outPath and copies content from r into it. Returns the
// number of bytes written.
func writeFile(outPath string, r io.Reader, mode os.FileMode) (int64, error) {
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", outPath, err)
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return n, fmt.Errorf("write %s: %w", outPath, err)
	}
	return n, nil
}

// stripped removes a leading "<prefix>/" from name if present, returning ""
// when the entry IS the prefix (a no-op directory). Empty prefix is a no-op.
func stripped(name, prefix string) string {
	name = filepath.ToSlash(name)
	if prefix == "" {
		return name
	}
	prefix = strings.TrimSuffix(prefix, "/")
	switch {
	case name == prefix, name == prefix+"/":
		return ""
	case strings.HasPrefix(name, prefix+"/"):
		return strings.TrimPrefix(name, prefix+"/")
	default:
		// Entry doesn't sit under the expected prefix. Keep as-is rather
		// than dropping silently — caller may want to see the entry.
		return name
	}
}

func modeOf(hdr *tar.Header) os.FileMode {
	m := os.FileMode(hdr.Mode).Perm()
	if m == 0 {
		m = 0o644
	}
	return m
}

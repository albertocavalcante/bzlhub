// Package mirror writes BCR-shape module entries to a local destination tree,
// turning canopy into a self-contained Bazel-module mirror as a side effect
// of ingestion.
//
// Wire shape produced:
//
//	<destRoot>/
//	  bazel_registry.json       (created with "{}" if absent)
//	  modules/
//	    <name>/
//	      metadata.json         (created or merged)
//	      <version>/
//	        MODULE.bazel        (verbatim from upstream)
//	        source.json         (verbatim from upstream)
//	  blobs/
//	    <basename>              (the source archive, written through a hashing reader)
//
// All file writes go via a write-temp-then-rename pattern so partial files
// never leak into the served tree on crash.
package mirror

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
)

// Writer materializes module entries into a destination root.
type Writer struct {
	Root string

	// metaMu serializes metadata.json read-modify-writes per module name.
	// Concurrent ingest of different versions of the same module would
	// otherwise race on the merge step (read → modify → atomic-rename can
	// silently drop a sibling's version when both run interleaved).
	metaMu sync.Mutex
	metaLocks map[string]*sync.Mutex
}

// New returns a writer rooted at root. The directory is created if absent.
func New(root string) (*Writer, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir mirror root: %w", err)
	}
	return &Writer{Root: root, metaLocks: make(map[string]*sync.Mutex)}, nil
}

// moduleLock returns (and lazily creates) a mutex for one module name.
// Holding it during MergeMetadata serializes concurrent version writes
// for the same module without blocking writes for unrelated modules.
func (w *Writer) moduleLock(name string) *sync.Mutex {
	w.metaMu.Lock()
	defer w.metaMu.Unlock()
	mu, ok := w.metaLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		w.metaLocks[name] = mu
	}
	return mu
}

// EnsureRegistryJSON writes an empty bazel_registry.json if one doesn't
// already exist. Bazel needs this file present at the registry root.
func (w *Writer) EnsureRegistryJSON() error {
	p := filepath.Join(w.Root, "bazel_registry.json")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return atomicWrite(p, []byte("{}\n"))
}

// WriteSource writes source.json (verbatim) under modules/<name>/<version>/.
func (w *Writer) WriteSource(name, version string, b []byte) error {
	p := filepath.Join(w.Root, "modules", name, version, "source.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicWrite(p, b)
}

// WriteModuleBazel writes MODULE.bazel (verbatim) under modules/<name>/<version>/.
func (w *Writer) WriteModuleBazel(name, version string, b []byte) error {
	p := filepath.Join(w.Root, "modules", name, version, "MODULE.bazel")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicWrite(p, b)
}

// BlobWriter returns a streaming sink for one archive. Bytes flow into a
// temp file under blobs/ while the SHA256 is computed in-flight. On Close,
// the temp is renamed to its **content address** — blobs/<sha256-hex>.
//
// Content addressing eliminates basename collisions: two modules whose
// upstream URLs share the same basename (e.g., both "v1.14.0.tar.gz") now
// land in distinct files. Two modules referencing the same upstream URL
// (or any same-bytes archive) dedupe to the same blob automatically.
//
// The srcURL argument is kept in the signature for future telemetry/
// observability; it does NOT affect the final on-disk filename.
func (w *Writer) BlobWriter(srcURL string) (*BlobSink, error) {
	if err := os.MkdirAll(filepath.Join(w.Root, "blobs"), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Join(w.Root, "blobs"), ".tmp-blob-*")
	if err != nil {
		return nil, err
	}
	return &BlobSink{
		tmp:     tmp,
		tmpPath: tmp.Name(),
		dir:     filepath.Join(w.Root, "blobs"),
		hasher:  sha256.New(),
	}, nil
}

// BlobSink writes archive bytes to a temp file under blobs/ while computing
// a SHA256. Close() renames the temp to blobs/<sha256-hex>.
type BlobSink struct {
	tmp     *os.File
	tmpPath string
	dir     string
	hasher  interface {
		io.Writer
		Sum(b []byte) []byte
	}
	bytes int64
}

// Write streams data to the temp blob file and into the hasher.
func (b *BlobSink) Write(p []byte) (int, error) {
	n, err := b.tmp.Write(p)
	if n > 0 {
		_, _ = b.hasher.Write(p[:n])
		b.bytes += int64(n)
	}
	return n, err
}

// Close finalizes the blob: closes the temp, renames into the content-
// addressed final path blobs/<sha256-hex>, and returns the final path,
// the SRI integrity string, and bytes-written.
//
// If a blob with the same content already exists (e.g., a previous ingest
// fetched the same upstream bytes via a different module), Close removes
// the redundant temp file and returns the existing final path — true
// content-addressed dedup.
func (b *BlobSink) Close() (path, integrity string, n int64, err error) {
	// Sync before rename so the bytes are durable on disk before any
	// other process (or canopy itself) starts serving the content-
	// addressed path. Without this, a power loss between rename and
	// the next checkpoint can leave a zero-byte blob exposed.
	if err = b.tmp.Sync(); err != nil {
		_ = b.tmp.Close()
		_ = os.Remove(b.tmpPath)
		return "", "", b.bytes, err
	}
	if err = b.tmp.Close(); err != nil {
		_ = os.Remove(b.tmpPath)
		return "", "", b.bytes, err
	}
	sum := b.hasher.Sum(nil)
	hex := hex.EncodeToString(sum)
	final := filepath.Join(b.dir, hex)
	integrity = "sha256-" + base64.StdEncoding.EncodeToString(sum)

	if _, statErr := os.Stat(final); statErr == nil {
		// Dedup: the same bytes are already on disk under the same hex
		// name. Drop the temp; the existing blob is canonical.
		_ = os.Remove(b.tmpPath)
		return final, integrity, b.bytes, nil
	}
	if err = os.Rename(b.tmpPath, final); err != nil {
		_ = os.Remove(b.tmpPath)
		return "", "", b.bytes, err
	}
	return final, integrity, b.bytes, nil
}

// Abort discards the temp blob without renaming. Safe to call after Close.
func (b *BlobSink) Abort() {
	if b.tmp != nil {
		_ = b.tmp.Close()
	}
	if b.tmpPath != "" {
		_ = os.Remove(b.tmpPath)
		b.tmpPath = ""
	}
}

// MergeMetadata reads metadata.json under modules/<name>/, appends the
// version if absent (keeping sorted), and writes back atomically. Other
// fields (yanked_versions, maintainers, homepage, repository) are preserved
// verbatim, including unknown keys via json.RawMessage round-trip.
//
// Concurrent calls for the same name are serialized via a per-name mutex.
// Calls for different names run in parallel.
func (w *Writer) MergeMetadata(name, version string) error {
	return w.MergeMetadataWithUpstream(name, version, nil)
}

// upstreamMetadataLiftedFields are the keys we copy from the upstream
// metadata.json (when supplied) into our local one. Keeping them
// explicit means new BCR fields don't sneak in silently — we'd
// rather know about the addition and decide whether to surface it.
//
// "versions" is intentionally NOT in this set: the local mirror is
// authoritative on what we've bumped, and the canopy registry serves
// only those versions over its BCR endpoints.
var upstreamMetadataLiftedFields = []string{
	"homepage",
	"maintainers",
	"repository",
	"yanked_versions",
}

// MergeMetadataWithUpstream is MergeMetadata plus a pass that lifts
// registry-level fields (homepage, maintainers, repository,
// yanked_versions) from the raw upstream metadata.json bytes into
// the local file. Empty upstreamBytes is equivalent to MergeMetadata
// — the lift pass is skipped.
//
// Local fields the upstream lift would overwrite are replaced
// wholesale: the upstream is the source of truth for those fields,
// so a stale local copy shouldn't linger after a new upstream
// metadata fetch. Unknown keys from BOTH sides survive the round-
// trip via the map[string]json.RawMessage shape.
func (w *Writer) MergeMetadataWithUpstream(name, version string, upstreamBytes []byte) error {
	mu := w.moduleLock(name)
	mu.Lock()
	defer mu.Unlock()
	p := filepath.Join(w.Root, "modules", name, "metadata.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}

	// Preserve unknown keys round-trip.
	m := make(map[string]json.RawMessage)
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("parse existing metadata.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	// Lift selected fields from the upstream metadata.json — done
	// BEFORE the local versions merge so the local versions field
	// (canopy-authoritative) always wins, even if upstream included
	// its own.
	if len(upstreamBytes) > 0 {
		var upstream map[string]json.RawMessage
		if err := json.Unmarshal(upstreamBytes, &upstream); err != nil {
			return fmt.Errorf("parse upstream metadata.json: %w", err)
		}
		for _, k := range upstreamMetadataLiftedFields {
			if raw, ok := upstream[k]; ok {
				m[k] = raw
			}
		}
	}

	var versions []string
	if raw, ok := m["versions"]; ok {
		if err := json.Unmarshal(raw, &versions); err != nil {
			return fmt.Errorf("parse versions: %w", err)
		}
	}
	if !slices.Contains(versions, version) {
		versions = append(versions, version)
		sort.Strings(versions)
	}
	verRaw, err := json.Marshal(versions)
	if err != nil {
		return err
	}
	m["versions"] = verRaw

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return atomicWrite(p, out)
}

// atomicWrite writes data to a temp file in the same directory then renames
// over the target. Crash-safe against partial writes.
func atomicWrite(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// Sync before close+rename so the file's contents are flushed to
	// disk before the rename publishes the inode. A crash between
	// rename and the next sync would otherwise leave the target
	// pointing at zero bytes.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}


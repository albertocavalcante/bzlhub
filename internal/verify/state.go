package verify

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/albertocavalcante/bzlhub/internal/fetch"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// state is the shared input snapshot all checks read from. It is built
// once at the top of Verify() — one filesystem walk + one DB dump — so
// individual checks cost nothing beyond an in-memory traversal.
//
// All fields are intentionally exported within the package only.
// Checks live in sibling files of the same package.
type state struct {
	mirrorRoot string
	store      *store.Store // may be nil when --db is unset

	// modules maps (name, version) → on-disk module state. Populated by
	// walking <root>/modules/<name>/<version>/.
	modules map[moduleKey]*moduleState

	// blobs maps sha256-hex blob filename → on-disk blob state. Populated
	// by listing <root>/blobs/.
	blobs map[string]*blobState

	// indexed lists (name, version) pairs present in the SQLite index.
	// Empty (not nil) when store is nil.
	indexed map[moduleKey]bool

	// scipIndexed lists (name, version) pairs that have a stored SCIP
	// blob in module_scip. Empty (not nil) when store is nil. Used by
	// the scip_present check to surface "module was ingested before
	// scip-bazel wiring landed" cases.
	scipIndexed map[moduleKey]bool

	// referencedBlobs is the set of blob hex strings referenced by any
	// source.json's integrity field. Built during the walk so the
	// orphan-blobs check is a cheap set-difference at run time.
	referencedBlobs map[string]bool
}

// moduleKey is the (name, version) tuple used as a map key for both
// on-disk module trees and SQLite index rows. Comparable directly.
type moduleKey struct {
	name, version string
}

// moduleState captures what we found on disk under
// modules/<name>/<version>/. Errors during read are recorded as
// per-field error strings so individual checks can produce specific
// findings without re-running I/O.
type moduleState struct {
	key            moduleKey
	dir            string
	sourceJSONPath string
	sourceJSONRaw  []byte
	sourceJSONErr  string // file-level read or parse error message
	source         *fetch.SourceJSON

	moduleBazelPath string
	moduleBazelRaw  []byte
	moduleBazelErr  string

	// expectedBlobHex is the sha256 hex (lowercase) decoded from the
	// source.json integrity field. Empty when integrity is missing or
	// malformed — the schema check picks those up.
	expectedBlobHex string
}

// blobState records what we found at blobs/<hex>. We don't read the
// bytes here — the blob_integrity check streams them to keep memory
// flat regardless of mirror size.
type blobState struct {
	hex  string
	path string
	size int64
}

// hexBlobRE matches the content-addressed blob filenames written by
// internal/mirror: lowercase sha256 hex, exactly 64 chars, no
// extension. Anything else is ignored on listing (and reported as an
// orphan/anomaly by the orphan_blobs check via a separate path).
var hexBlobRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// buildState walks the mirror tree once and snapshots the DB index.
// Errors here are tool-level (the root doesn't exist, the DB can't be
// read) and bubble up to Verify(); per-file problems are recorded into
// the state itself for checks to surface as findings.
func buildState(ctx context.Context, root string, st *store.Store) (*state, error) {
	s := &state{
		mirrorRoot:      root,
		store:           st,
		modules:         map[moduleKey]*moduleState{},
		blobs:           map[string]*blobState{},
		indexed:         map[moduleKey]bool{},
		scipIndexed:     map[moduleKey]bool{},
		referencedBlobs: map[string]bool{},
	}

	// modules/<name>/<version>/ — walk two levels deep. Anything that
	// doesn't fit the shape is silently ignored; we don't want this to
	// fight the operator who has, say, a stray .DS_Store from macOS.
	modulesDir := filepath.Join(root, "modules")
	if err := walkModules(modulesDir, s); err != nil {
		// Missing modules/ dir on an empty mirror is OK — verify still
		// runs with zero modules; checks return empty findings. Only a
		// hard read error aborts.
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("walk modules: %w", err)
		}
	}

	// blobs/ — same forgiving stance.
	blobsDir := filepath.Join(root, "blobs")
	if err := listBlobs(blobsDir, s); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("list blobs: %w", err)
	}

	// DB index — list all (name, version) pairs.
	if st != nil {
		if err := loadIndex(ctx, st, s); err != nil {
			return nil, fmt.Errorf("load index: %w", err)
		}
	}
	return s, nil
}

func walkModules(modulesDir string, s *state) error {
	names, err := os.ReadDir(modulesDir)
	if err != nil {
		return err
	}
	for _, nameEnt := range names {
		if !nameEnt.IsDir() {
			continue
		}
		name := nameEnt.Name()
		moduleDir := filepath.Join(modulesDir, name)
		versions, err := os.ReadDir(moduleDir)
		if err != nil {
			// Skip unreadable individual module dirs rather than aborting
			// the whole walk — one bad dir shouldn't blind the rest.
			continue
		}
		for _, vEnt := range versions {
			if !vEnt.IsDir() {
				continue
			}
			version := vEnt.Name()
			ms := loadModuleState(name, version, filepath.Join(moduleDir, version))
			s.modules[ms.key] = ms
			if ms.expectedBlobHex != "" {
				s.referencedBlobs[ms.expectedBlobHex] = true
			}
		}
	}
	return nil
}

// loadModuleState reads the per-version files (source.json,
// MODULE.bazel) eagerly. Both are tiny (KB range) so caching them in
// memory pays for itself by avoiding re-reads across checks.
func loadModuleState(name, version, dir string) *moduleState {
	ms := &moduleState{
		key:             moduleKey{name: name, version: version},
		dir:             dir,
		sourceJSONPath:  filepath.Join(dir, "source.json"),
		moduleBazelPath: filepath.Join(dir, "MODULE.bazel"),
	}

	if raw, err := os.ReadFile(ms.sourceJSONPath); err != nil {
		ms.sourceJSONErr = err.Error()
	} else {
		ms.sourceJSONRaw = raw
		if src, perr := fetch.ParseSourceJSON(raw); perr != nil {
			ms.sourceJSONErr = perr.Error()
		} else {
			ms.source = src
			if hex, ok := sriToHex(src.Integrity); ok {
				ms.expectedBlobHex = hex
			}
		}
	}

	if raw, err := os.ReadFile(ms.moduleBazelPath); err != nil {
		ms.moduleBazelErr = err.Error()
	} else {
		ms.moduleBazelRaw = raw
	}
	return ms
}

func listBlobs(blobsDir string, s *state) error {
	ents, err := os.ReadDir(blobsDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !hexBlobRE.MatchString(name) {
			// Anything that's not a canonical content-addressed blob is
			// considered an orphan/anomaly. We surface it via the
			// orphan_blobs check (separate code path); skip from the
			// referenced-blob comparison here.
			continue
		}
		info, infoErr := e.Info()
		var size int64
		if infoErr == nil {
			size = info.Size()
		}
		s.blobs[name] = &blobState{
			hex:  name,
			path: filepath.Join(blobsDir, name),
			size: size,
		}
	}
	return nil
}

func loadIndex(ctx context.Context, st *store.Store, s *state) error {
	// store doesn't currently expose a single "list all (name, version)"
	// call (the existing API is ListVersions(name) — keyed by name).
	// We use a small raw query on the package's *Store via the
	// purpose-built listAll helper added below in the same package
	// boundary, but to stay within store's public surface we instead
	// derive the set of names via ListAllVersions, a new helper we add
	// next to ListVersions. See store/store.go.
	rows, err := st.ListAllVersions(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		s.indexed[moduleKey{name: r.Module, version: r.Version}] = true
	}
	// SCIP blob index is a strict subset of the versions index (FK
	// cascade ensures it). Load it the same way for the scip_present
	// check.
	scipRows, err := st.ListScipVersions(ctx)
	if err != nil {
		return fmt.Errorf("list scip versions: %w", err)
	}
	for _, r := range scipRows {
		s.scipIndexed[moduleKey{name: r.Module, version: r.Version}] = true
	}
	return nil
}

// sriToHex decodes a "sha256-<base64>" SRI string to its lowercase hex
// representation, matching the content-addressed blob filename written
// by internal/mirror. Returns ("", false) for empty or non-sha256 input
// — the source_json_schema check surfaces those.
func sriToHex(sri string) (string, bool) {
	const prefix = "sha256-"
	if len(sri) <= len(prefix) || sri[:len(prefix)] != prefix {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(sri[len(prefix):])
	if err != nil || len(b) != 32 {
		return "", false
	}
	const hexdigits = "0123456789abcdef"
	buf := make([]byte, 64)
	for i, x := range b {
		buf[i*2] = hexdigits[x>>4]
		buf[i*2+1] = hexdigits[x&0x0f]
	}
	return string(buf), true
}

// sortedModuleKeys returns the modules-map keys in deterministic order.
// Findings need stable wire output for diffable test golden values; the
// individual checks call this when iterating.
func sortedModuleKeys(m map[moduleKey]*moduleState) []moduleKey {
	out := make([]moduleKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return out[i].version < out[j].version
	})
	return out
}

// sortedBlobHexes returns the blobs-map keys in deterministic order.
func sortedBlobHexes(m map[string]*blobState) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package bcrmirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadModuleMetadata reads modules/<module>/metadata.json from the
// mirror's working tree at HEAD.
//
// Returns ErrModuleNotFound when the directory modules/<module>/
// doesn't exist. Returns a wrapped error for I/O failures (permissions,
// disk error, etc.).
//
// The bytes are returned verbatim; the caller decodes JSON as needed.
// This deliberate raw-bytes shape lets callers (canopy ingest, drift
// detectors) re-use the same bytes for both display and hashing without
// re-marshalling.
//
// Mirror must be Open()ed before this is called; otherwise returns
// ErrNoMirror.
func (m *Mirror) ReadModuleMetadata(ctx context.Context, module string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := m.requireOpenRepo(); err != nil {
		return nil, err
	}
	if err := validateModuleName(module); err != nil {
		return nil, err
	}

	moduleDir := filepath.Join(m.Path, "modules", module)
	if _, err := os.Stat(moduleDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.ReadModuleMetadata: stat %s: %w", moduleDir, err)
	}

	path := filepath.Join(moduleDir, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Module dir exists but metadata.json doesn't — treat as
			// module-not-found at the contract level; canopy callers
			// can't usefully distinguish "no module" from "no metadata
			// for that module."
			return nil, fmt.Errorf("%w: %s (metadata.json missing)", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.ReadModuleMetadata: read %s: %w", path, err)
	}
	return data, nil
}

// ReadSourceJSON reads modules/<module>/<version>/source.json.
//
// Returns ErrVersionNotFound when the version directory doesn't exist
// (regardless of whether the module exists). Returns ErrModuleNotFound
// when the module directory itself is missing.
//
// Mirror must be Open()ed; otherwise returns ErrNoMirror.
func (m *Mirror) ReadSourceJSON(ctx context.Context, module, version string) ([]byte, error) {
	return m.readVersionFile(ctx, module, version, "source.json")
}

// ReadModuleBazel reads modules/<module>/<version>/MODULE.bazel.
// Same error semantics as ReadSourceJSON.
func (m *Mirror) ReadModuleBazel(ctx context.Context, module, version string) ([]byte, error) {
	return m.readVersionFile(ctx, module, version, "MODULE.bazel")
}

// ReadPatch reads modules/<module>/<version>/patches/<patchName>.
//
// Returns ErrPatchNotFound when the specific patch file is absent.
// Returns ErrVersionNotFound when the version directory itself is
// missing.
func (m *Mirror) ReadPatch(ctx context.Context, module, version, patchName string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := m.requireOpenRepo(); err != nil {
		return nil, err
	}
	if err := validateModuleName(module); err != nil {
		return nil, err
	}
	if err := validateVersion(version); err != nil {
		return nil, err
	}
	if err := validatePatchName(patchName); err != nil {
		return nil, err
	}

	versionDir := filepath.Join(m.Path, "modules", module, version)
	if _, err := os.Stat(versionDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s", ErrVersionNotFound, module, version)
		}
		return nil, fmt.Errorf("bcrmirror.ReadPatch: stat %s: %w", versionDir, err)
	}

	path := filepath.Join(versionDir, "patches", patchName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s/%s", ErrPatchNotFound, module, version, patchName)
		}
		return nil, fmt.Errorf("bcrmirror.ReadPatch: read %s: %w", path, err)
	}
	return data, nil
}

// ListModules returns every module name under modules/, sorted
// lexically. Hidden entries (names starting with ".") and non-
// directories are excluded.
//
// Returns an empty (non-nil) slice when modules/ exists but is empty;
// returns an empty slice with no error when modules/ itself doesn't
// exist (a freshly-cloned but empty registry).
//
// Mirror must be Open()ed; otherwise returns ErrNoMirror.
func (m *Mirror) ListModules(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := m.requireOpenRepo(); err != nil {
		return nil, err
	}

	modulesDir := filepath.Join(m.Path, "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("bcrmirror.ListModules: read %s: %w", modulesDir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// ListVersions returns every version directory under
// modules/<module>/. Hidden entries and non-directories are excluded.
//
// Sorting is LEXICAL — NOT version-aware. Callers that need semver
// or 4-component-Bazel-version comparison must sort the result
// themselves. This library stays string-shaped to avoid pulling in a
// version comparator dependency.
//
// Returns ErrModuleNotFound when modules/<module>/ doesn't exist.
//
// Returns an empty (non-nil) slice when the module directory exists
// but contains no version subdirectories.
func (m *Mirror) ListVersions(ctx context.Context, module string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := m.requireOpenRepo(); err != nil {
		return nil, err
	}
	if err := validateModuleName(module); err != nil {
		return nil, err
	}

	moduleDir := filepath.Join(m.Path, "modules", module)
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.ListVersions: read %s: %w", moduleDir, err)
	}

	versions := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		versions = append(versions, e.Name())
	}
	sort.Strings(versions)
	return versions, nil
}

// readVersionFile is the shared body for ReadSourceJSON +
// ReadModuleBazel. Returns ErrModuleNotFound when the module dir is
// missing; ErrVersionNotFound when the version dir is missing within
// an existing module; wraps I/O errors otherwise.
func (m *Mirror) readVersionFile(ctx context.Context, module, version, filename string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := m.requireOpenRepo(); err != nil {
		return nil, err
	}
	if err := validateModuleName(module); err != nil {
		return nil, err
	}
	if err := validateVersion(version); err != nil {
		return nil, err
	}

	moduleDir := filepath.Join(m.Path, "modules", module)
	if _, err := os.Stat(moduleDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.read: stat %s: %w", moduleDir, err)
	}

	versionDir := filepath.Join(moduleDir, version)
	if _, err := os.Stat(versionDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s", ErrVersionNotFound, module, version)
		}
		return nil, fmt.Errorf("bcrmirror.read: stat %s: %w", versionDir, err)
	}

	path := filepath.Join(versionDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s (%s missing)", ErrVersionNotFound, module, version, filename)
		}
		return nil, fmt.Errorf("bcrmirror.read: read %s: %w", path, err)
	}
	return data, nil
}

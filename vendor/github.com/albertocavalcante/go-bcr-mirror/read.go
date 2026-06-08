package bcrmirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
)

// ReadModuleMetadata reads modules/<module>/metadata.json from the
// mirror's working tree at HEAD.
//
// Returns ErrModuleNotFound when the directory modules/<module>/
// doesn't exist. Returns a wrapped error for I/O failures
// (permissions, disk error, etc.).
//
// The bytes are returned verbatim; the caller decodes JSON as
// needed. Mirror must be Open()ed before this is called; otherwise
// returns ErrNoMirror.
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

	moduleDir := path.Join("modules", module)
	if _, err := m.rootStat(moduleDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.ReadModuleMetadata: stat %s: %w", moduleDir, err)
	}

	rel := path.Join(moduleDir, "metadata.json")
	data, err := m.rootReadFile(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s (metadata.json missing)", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.ReadModuleMetadata: read %s: %w", rel, err)
	}
	return data, nil
}

// ReadSourceJSON reads modules/<module>/<version>/source.json.
//
// Returns ErrVersionNotFound when the version directory doesn't
// exist (regardless of whether the module exists). Returns
// ErrModuleNotFound when the module directory itself is missing.
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

	versionDir := path.Join("modules", module, version)
	if _, err := m.rootStat(versionDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s", ErrVersionNotFound, module, version)
		}
		return nil, fmt.Errorf("bcrmirror.ReadPatch: stat %s: %w", versionDir, err)
	}

	rel := path.Join(versionDir, "patches", patchName)
	data, err := m.rootReadFile(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s/%s", ErrPatchNotFound, module, version, patchName)
		}
		return nil, fmt.Errorf("bcrmirror.ReadPatch: read %s: %w", rel, err)
	}
	return data, nil
}

// ListModules returns every module name under modules/, sorted
// lexically. Hidden entries and non-directories are excluded.
//
// Returns an empty (non-nil) slice when modules/ exists but is
// empty; returns an empty slice with no error when modules/ itself
// doesn't exist (a freshly-cloned but empty registry).
func (m *Mirror) ListModules(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := m.requireOpenRepo(); err != nil {
		return nil, err
	}

	entries, err := m.rootReadDir("modules")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("bcrmirror.ListModules: read modules/: %w", err)
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
	slices.Sort(names)
	return names, nil
}

// ListVersions returns every version directory under
// modules/<module>/. Hidden entries and non-directories are excluded.
//
// Sorting is LEXICAL — NOT version-aware. Callers needing semver
// or 4-component-Bazel-version comparison sort the result themselves.
//
// Returns ErrModuleNotFound when modules/<module>/ doesn't exist.
// Returns an empty (non-nil) slice when the module dir exists but
// holds no version subdirectories.
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

	rel := path.Join("modules", module)
	entries, err := m.rootReadDir(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.ListVersions: read %s: %w", rel, err)
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
	slices.Sort(versions)
	return versions, nil
}

// readVersionFile is the shared body for ReadSourceJSON +
// ReadModuleBazel.
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

	moduleDir := path.Join("modules", module)
	if _, err := m.rootStat(moduleDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, fmt.Errorf("bcrmirror.read: stat %s: %w", moduleDir, err)
	}

	versionDir := path.Join(moduleDir, version)
	if _, err := m.rootStat(versionDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s", ErrVersionNotFound, module, version)
		}
		return nil, fmt.Errorf("bcrmirror.read: stat %s: %w", versionDir, err)
	}

	rel := path.Join(versionDir, filename)
	data, err := m.rootReadFile(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s@%s (%s missing)", ErrVersionNotFound, module, version, filename)
		}
		return nil, fmt.Errorf("bcrmirror.read: read %s: %w", rel, err)
	}
	return data, nil
}

// rootStat / rootReadFile / rootReadDir are thin wrappers that read
// the Mirror's os.Root under stateMu, then perform the operation.
// Centralising the lock means concurrent readers don't race against
// Close.
func (m *Mirror) rootStat(rel string) (os.FileInfo, error) {
	m.stateMu.RLock()
	r := m.root
	m.stateMu.RUnlock()
	if r == nil {
		return nil, fmt.Errorf("%w: Mirror not Open", ErrNoMirror)
	}
	return r.Stat(rel)
}

func (m *Mirror) rootReadFile(rel string) ([]byte, error) {
	m.stateMu.RLock()
	r := m.root
	m.stateMu.RUnlock()
	if r == nil {
		return nil, fmt.Errorf("%w: Mirror not Open", ErrNoMirror)
	}
	return r.ReadFile(rel)
}

func (m *Mirror) rootReadDir(rel string) ([]os.DirEntry, error) {
	m.stateMu.RLock()
	r := m.root
	m.stateMu.RUnlock()
	if r == nil {
		return nil, fmt.Errorf("%w: Mirror not Open", ErrNoMirror)
	}
	d, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(-1)
}

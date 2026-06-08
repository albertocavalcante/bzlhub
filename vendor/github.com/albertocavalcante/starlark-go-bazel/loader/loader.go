// Package loader provides file loading abstractions for Starlark files.
//
// This package implements .bzl file loading following Bazel's semantics:
// - Label resolution: "//pkg:foo.bzl" resolves to files in the repository
// - Repository mapping: "@repo//pkg:foo.bzl" resolves via repo mapping
// - Module caching: loaded modules are cached by their canonical label
// - Cycle detection: circular load dependencies are detected and reported
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/skyframe/BzlLoadFunction.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/skyframe/BzlLoadValue.java
package loader

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Thread context keys for storing loader state.
const (
	// ThreadKeyLoader is the key for storing the BzlLoader in the thread.
	ThreadKeyLoader = "starlark-go-bazel:loader"

	// ThreadKeyCurrentPkg is the key for the current package path (for relative loads).
	ThreadKeyCurrentPkg = "starlark-go-bazel:current_package"

	// ThreadKeyCurrentRepo is the key for the current repository name.
	ThreadKeyCurrentRepo = "starlark-go-bazel:current_repo"

	// ThreadKeyLoadStack is the key for cycle detection stack.
	ThreadKeyLoadStack = "starlark-go-bazel:load_stack"
)

// FileSystem abstracts file system operations.
type FileSystem interface {
	// ReadFile reads the contents of the file at path.
	ReadFile(path string) ([]byte, error)
	// Stat returns file info for the given path.
	Stat(path string) (fs.FileInfo, error)
	// Join joins path elements.
	Join(elem ...string) string
	// Abs returns the absolute path.
	Abs(path string) (string, error)
}

// Loader loads Starlark files by path.
type Loader interface {
	// Load loads a Starlark file by path.
	Load(path string) ([]byte, error)
}

// BzlLoader resolves and loads .bzl files with caching and cycle detection.
//
// This interface abstracts the loading of Starlark modules, enabling
// different implementations for different environments (filesystem, WASM, etc.).
//
// The Load method follows Bazel's load() semantics:
// - load("//pkg:foo.bzl", "a", b="c") imports 'a' as 'a' and 'c' as 'b'
// - load() statements are evaluated before any other statements
// - Circular loads are detected and reported as errors
type BzlLoader interface {
	// Load loads a .bzl file and returns its exported symbols.
	// The module string is a label like "//pkg:foo.bzl" or "@repo//pkg:foo.bzl".
	// The thread provides context such as the current package for relative labels.
	Load(thread *starlark.Thread, module string) (starlark.StringDict, error)
}

// OSFileSystem implements FileSystem using the operating system.
type OSFileSystem struct {
	root string
}

// NewOSFileSystem creates a new OSFileSystem rooted at the given path.
func NewOSFileSystem(root string) *OSFileSystem {
	if root == "" {
		root = "."
	}
	return &OSFileSystem{root: root}
}

// ReadFile reads the contents of the file at path.
func (f *OSFileSystem) ReadFile(path string) ([]byte, error) {
	fullPath := path
	if !filepath.IsAbs(path) {
		fullPath = filepath.Join(f.root, path)
	}
	return os.ReadFile(fullPath) //nolint:gosec // G304: path is supplied by the caller by design (this is the OSFileSystem loader).
}

// Stat returns file info for the given path.
func (f *OSFileSystem) Stat(path string) (os.FileInfo, error) {
	fullPath := path
	if !filepath.IsAbs(path) {
		fullPath = filepath.Join(f.root, path)
	}
	return os.Stat(fullPath)
}

// Join joins path elements.
func (f *OSFileSystem) Join(elem ...string) string {
	return filepath.Join(elem...)
}

// Abs returns the absolute path.
func (f *OSFileSystem) Abs(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Abs(filepath.Join(f.root, path))
}

// Root returns the root directory.
func (f *OSFileSystem) Root() string {
	return f.root
}

// FileSystemLoader implements Loader using a FileSystem.
type FileSystemLoader struct {
	fs FileSystem
}

// NewFileSystemLoader creates a new FileSystemLoader.
func NewFileSystemLoader(filesys FileSystem) *FileSystemLoader {
	return &FileSystemLoader{fs: filesys}
}

// Load loads a Starlark file by path.
func (l *FileSystemLoader) Load(path string) ([]byte, error) {
	return l.fs.ReadFile(path)
}

// FileSystem returns the underlying file system.
func (l *FileSystemLoader) FileSystem() FileSystem {
	return l.fs
}

// SetBzlLoader stores a BzlLoader in the thread for use by load() statements.
func SetBzlLoader(thread *starlark.Thread, l BzlLoader) {
	thread.SetLocal(ThreadKeyLoader, l)
}

// GetBzlLoader retrieves the BzlLoader from the thread.
func GetBzlLoader(thread *starlark.Thread) BzlLoader {
	if l := thread.Local(ThreadKeyLoader); l != nil {
		return l.(BzlLoader)
	}
	return nil
}

// SetCurrentPackage sets the current package for relative label resolution.
func SetCurrentPackage(thread *starlark.Thread, pkg string) {
	thread.SetLocal(ThreadKeyCurrentPkg, pkg)
}

// GetCurrentPackage gets the current package from the thread.
func GetCurrentPackage(thread *starlark.Thread) string {
	if pkg := thread.Local(ThreadKeyCurrentPkg); pkg != nil {
		return pkg.(string)
	}
	return ""
}

// SetCurrentRepo sets the current repository name.
func SetCurrentRepo(thread *starlark.Thread, repo string) {
	thread.SetLocal(ThreadKeyCurrentRepo, repo)
}

// GetCurrentRepo gets the current repository from the thread.
func GetCurrentRepo(thread *starlark.Thread) string {
	if repo := thread.Local(ThreadKeyCurrentRepo); repo != nil {
		return repo.(string)
	}
	return ""
}

// LoadResult contains the result of loading a .bzl module.
// Inspired by BzlLoadValue in Bazel.
type LoadResult struct {
	// Globals contains the exported symbols from the module.
	Globals starlark.StringDict

	// TransitiveDigest is a hash of the module and its transitive dependencies.
	// Used for caching and change detection.
	TransitiveDigest []byte
}

// BzlFileLoader loads .bzl files from a filesystem with caching and cycle detection.
//
// It implements caching (modules are loaded only once) and cycle detection.
// The cache key is the fully-resolved label string.
//
// Reference: BzlLoadFunction's caching mechanism using CachedBzlLoadData.
type BzlFileLoader struct {
	fs       FileSystem
	repoRoot string

	// Predeclared symbols available to all .bzl files.
	// These are the "predeclared environment" in Bazel terminology.
	predeclared starlark.StringDict

	// Repository mapping for external repository resolution.
	// Maps apparent repository names to canonical repository roots.
	repoMapping map[string]string

	// Cache of loaded modules, keyed by canonical label.
	// This matches Bazel's approach of caching BzlLoadValues.
	mu    sync.Mutex
	cache map[string]*loadEntry

	// lenient turns unresolvable loads (unknown repo, missing file)
	// into a soft no-op: the call returns an empty StringDict and
	// nil error. See WithLenientLoad for the contract.
	lenient bool
}

// loadEntry represents a cached module or a module being loaded.
// The ready channel is used to wait for in-progress loads.
type loadEntry struct {
	globals starlark.StringDict
	err     error
	ready   chan struct{} // closed when load completes
}

// BzlFileLoaderOption configures a BzlFileLoader.
type BzlFileLoaderOption func(*BzlFileLoader)

// WithPredeclared sets the predeclared symbols for .bzl files.
func WithPredeclared(predeclared starlark.StringDict) BzlFileLoaderOption {
	return func(l *BzlFileLoader) {
		l.predeclared = predeclared
	}
}

// WithRepoMapping sets the repository mapping for external repos.
func WithRepoMapping(mapping map[string]string) BzlFileLoaderOption {
	return func(l *BzlFileLoader) {
		l.repoMapping = mapping
	}
}

// WithLenientLoad turns load() errors for unknown external repos and
// missing files into a soft no-op: the call returns an empty
// StringDict and a nil error, so evaluation of the importing file
// continues. Symbols that the missing load would have provided are
// simply undefined in the importer's scope, and only fail if and when
// the importer's code actually references them.
//
// This is the "introspect-only" mode used by callers that want to
// extract structural information (rule attrs, provider fields, etc.)
// from .bzl files without materializing their entire external
// dependency closure. Faithful execution mode (the default) still
// errors on unresolvable loads.
func WithLenientLoad(enabled bool) BzlFileLoaderOption {
	return func(l *BzlFileLoader) {
		l.lenient = enabled
	}
}

// NewBzlFileLoader creates a new loader that reads from the given filesystem.
// The repoRoot is the path to the workspace root (main repository).
func NewBzlFileLoader(filesys FileSystem, repoRoot string, opts ...BzlFileLoaderOption) *BzlFileLoader {
	l := &BzlFileLoader{
		fs:          filesys,
		repoRoot:    repoRoot,
		predeclared: make(starlark.StringDict),
		repoMapping: make(map[string]string),
		cache:       make(map[string]*loadEntry),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Load implements the BzlLoader interface.
//
// Module resolution follows Bazel semantics:
// - "//pkg:foo.bzl" loads from the main repository
// - "@repo//pkg:foo.bzl" loads from an external repository
// - ":foo.bzl" is relative to current package (resolved using thread context)
//
// Cycle detection: The function tracks which modules are currently being loaded
// in a stack stored in the thread context. If a module appears in its own
// load stack, a cycle error is reported.
//
// Reference: BzlLoadFunction.computeInternal() and InliningState.beginLoad()/finishLoad()
func (l *BzlFileLoader) Load(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	// Resolve the module string to a canonical label and filesystem path.
	label, path, err := l.resolveModule(thread, module)
	if err != nil {
		// Lenient mode: treat unresolvable external repos as
		// empty-import-set. Importing file keeps evaluating; any
		// symbol the load was supposed to provide is just undefined.
		if l.lenient {
			return starlark.StringDict{}, nil
		}
		return nil, fmt.Errorf("load(%q): %w", module, err)
	}

	// Check for load cycle.
	// Bazel uses a LinkedHashSet<BzlLoadValue.Key> for cycle detection.
	loadStack := l.getLoadStack(thread)
	for _, entry := range loadStack {
		if entry == label {
			return nil, &CycleError{
				Module: module,
				Stack:  append(loadStack, label),
			}
		}
	}

	// Check cache first.
	l.mu.Lock()
	entry, ok := l.cache[label]
	if !ok {
		// Start a new load.
		entry = &loadEntry{ready: make(chan struct{})}
		l.cache[label] = entry
		l.mu.Unlock()

		// Perform the load (outside the lock).
		globals, loadErr := l.loadFile(thread, label, path, loadStack)
		entry.globals = globals
		entry.err = loadErr
		close(entry.ready)
	} else {
		l.mu.Unlock()
		// Wait for in-progress load to complete.
		<-entry.ready
	}

	if entry.err != nil {
		// Lenient mode also swallows downstream load errors (missing
		// files, parse errors in transitively-loaded files). We cache
		// the success-with-empty-globals path explicitly so a later
		// load of the same label doesn't re-attempt the failing read.
		if l.lenient {
			return starlark.StringDict{}, nil
		}
		return nil, entry.err
	}
	return entry.globals, nil
}

// resolveModule resolves a module string to a canonical label and filesystem path.
//
// Label resolution rules (from BzlLoadFunction.getLoadLabels()):
// 1. "@repo//pkg:file.bzl" - External repository label
// 2. "//pkg:file.bzl" - Main repository label
// 3. ":file.bzl" - Relative to current package
// 4. "file.bzl" - Relative to current package (legacy, discouraged)
//
// The returned label is the canonical form (e.g., "@main//pkg:file.bzl" for main repo).
// The returned path is the filesystem path to read.
func (l *BzlFileLoader) resolveModule(thread *starlark.Thread, module string) (label, path string, err error) {
	currentPkg := GetCurrentPackage(thread)
	currentRepo := GetCurrentRepo(thread)

	var repo, pkg, file string

	switch {
	case strings.HasPrefix(module, "@"):
		// External repository: @repo//pkg:file.bzl
		idx := strings.Index(module, "//")
		if idx == -1 {
			return "", "", fmt.Errorf("invalid label %q: missing //", module)
		}
		repo = module[1:idx]
		rest := module[idx+2:]
		pkg, file, err = splitPkgTarget(rest)
		if err != nil {
			return "", "", err
		}

	case strings.HasPrefix(module, "//"):
		// Main repository: //pkg:file.bzl
		repo = currentRepo // Stay in current repo context
		rest := module[2:]
		pkg, file, err = splitPkgTarget(rest)
		if err != nil {
			return "", "", err
		}

	case strings.HasPrefix(module, ":"):
		// Relative to current package: :file.bzl
		repo = currentRepo
		pkg = currentPkg
		file = module[1:]

	default:
		// Legacy relative path (discouraged but supported)
		repo = currentRepo
		pkg = currentPkg
		file = module
	}

	// Validate .bzl extension.
	// Reference: BzlLoadFunction.checkValidLoadLabel()
	if !strings.HasSuffix(file, ".bzl") && !strings.HasSuffix(file, ".scl") {
		return "", "", fmt.Errorf("file must have .bzl or .scl extension, got %q", file)
	}

	// Build canonical label.
	if repo != "" {
		label = fmt.Sprintf("@%s//%s:%s", repo, pkg, file)
	} else {
		label = fmt.Sprintf("//%s:%s", pkg, file)
	}

	// Resolve filesystem path.
	repoRoot := l.repoRoot
	if repo != "" {
		if mappedRoot, ok := l.repoMapping[repo]; ok {
			repoRoot = mappedRoot
		} else if repo != "main" {
			return "", "", fmt.Errorf("unknown repository %q", repo)
		}
	}

	path = l.fs.Join(repoRoot, pkg, file)
	return label, path, nil
}

// splitPkgTarget splits "pkg:target" or "pkg/subpkg:target" into package and target.
func splitPkgTarget(s string) (pkg, target string, err error) {
	idx := strings.LastIndex(s, ":")
	if idx == -1 {
		return "", "", fmt.Errorf("invalid label: missing colon in %q", s)
	}
	return s[:idx], s[idx+1:], nil
}

// loadFile actually loads and executes a .bzl file.
//
// This mirrors the execution phase in BzlLoadFunction.executeBzlFile().
// Steps:
// 1. Read the source file
// 2. Parse and compile
// 3. Execute with a child thread that has updated load stack
// 4. Extract exported globals
func (l *BzlFileLoader) loadFile(thread *starlark.Thread, label, path string, parentStack []string) (starlark.StringDict, error) {
	// Read source.
	source, err := l.fs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Create a child thread for this module's execution.
	// The child thread:
	// - Has the same loader
	// - Has updated current package context
	// - Has the extended load stack for cycle detection
	childThread := &starlark.Thread{
		Name:  label,
		Print: thread.Print,
		Load:  l.Load, // Recursive loads use this loader
	}

	// Extract package from label for context.
	pkg := ""
	if idx := strings.Index(label, "//"); idx != -1 {
		rest := label[idx+2:]
		if colonIdx := strings.LastIndex(rest, ":"); colonIdx != -1 {
			pkg = rest[:colonIdx]
		}
	}

	// Extract repo from label.
	repo := ""
	if strings.HasPrefix(label, "@") {
		if idx := strings.Index(label, "//"); idx != -1 {
			repo = label[1:idx]
		}
	}

	// Set up thread context.
	SetBzlLoader(childThread, l)
	SetCurrentPackage(childThread, pkg)
	SetCurrentRepo(childThread, repo)
	l.setLoadStack(childThread, append(parentStack, label))

	// Execute the module.
	// Reference: Starlark.execFileProgram() called from BzlLoadFunction.executeBzlFile()
	globals, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		childThread,
		path,
		source,
		l.predeclared,
	)
	if err != nil {
		return nil, fmt.Errorf("executing %s: %w", label, err)
	}

	return globals, nil
}

// getLoadStack retrieves the current load stack from the thread.
func (l *BzlFileLoader) getLoadStack(thread *starlark.Thread) []string {
	if stack := thread.Local(ThreadKeyLoadStack); stack != nil {
		return stack.([]string)
	}
	return nil
}

// setLoadStack stores the load stack in the thread.
func (l *BzlFileLoader) setLoadStack(thread *starlark.Thread, stack []string) {
	thread.SetLocal(ThreadKeyLoadStack, stack)
}

// ClearCache removes all cached modules.
// Useful for testing or when source files have changed.
func (l *BzlFileLoader) ClearCache() {
	l.mu.Lock()
	l.cache = make(map[string]*loadEntry)
	l.mu.Unlock()
}

// CycleError is returned when a circular load dependency is detected.
//
// Reference: BzlLoadFunction.InliningState.beginLoad() throws this as:
// "Starlark load cycle: [list of labels]"
type CycleError struct {
	Module string
	Stack  []string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("Starlark load cycle: %v", e.Stack)
}

// MakeLoadFunc creates a Load function for starlark.Thread that uses the given BzlLoader.
//
// This is a convenience function for setting up the thread's Load callback.
// The returned function follows Bazel's load() semantics:
// - load("//pkg:foo.bzl", "a", b="c") imports 'a' as 'a' and 'c' as 'b'
// - load() can only appear at top of file (enforced by parser)
// - load() statements are evaluated before any other statements
func MakeLoadFunc(loader BzlLoader) func(*starlark.Thread, string) (starlark.StringDict, error) {
	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		return loader.Load(thread, module)
	}
}

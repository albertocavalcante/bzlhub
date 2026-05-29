package gobzlmod

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/albertocavalcante/go-bzlmod/registry"
)

// localRegistry provides module data from a local file system path.
// This enables airgap/offline workflows where modules are pre-downloaded
// or vendored into a local directory.
//
// The directory should follow the standard registry layout:
//
//	{root}/modules/{name}/{version}/MODULE.bazel
//	{root}/modules/{name}/metadata.json
//
// Create with file:// URLs:
//
//	// Unix
//	reg := Registry("file:///path/to/registry")
//
//	// Windows
//	reg := Registry("file:///C:/path/to/registry")
//
// Or use newLocalRegistry directly with a native path:
//
//	reg := newLocalRegistry("/path/to/registry")      // Unix
//	reg := newLocalRegistry("C:\\path\\to\\registry") // Windows
type localRegistry struct {
	rootPath      string
	cache         sync.Map // map[string]*ModuleInfo keyed by "name@version"
	metadataCache sync.Map // map[string]*registry.Metadata keyed by module name
}

// newLocalRegistry creates a registry client for a local directory.
//
// The path should be an absolute path to a directory with the standard
// registry layout. Use file:// URLs with Registry() for a cleaner API.
// The path can use either forward slashes or the native OS separator.
func newLocalRegistry(rootPath string) *localRegistry {
	return &localRegistry{
		rootPath: filepath.Clean(rootPath),
	}
}

// parseFileURL extracts the path from a file:// URL.
// Handles both Unix (file:///path) and Windows (file:///C:/path) formats.
//
// Examples:
//
//	Unix:    file:///tmp/registry      -> /tmp/registry
//	Windows: file:///C:/Users/registry -> C:/Users/registry
//	Windows: file:///c:/Users/registry -> c:/Users/registry
func parseFileURL(url string) (string, error) {
	if !strings.HasPrefix(url, "file://") {
		return "", fmt.Errorf("not a file:// URL: %s", url)
	}

	// Remove file:// prefix
	path := strings.TrimPrefix(url, "file://")

	// Handle Windows paths: file:///C:/path or file:///c:/path -> C:/path
	// Check for drive letter pattern: /X:/ where X is a letter
	if len(path) >= 3 && path[0] == '/' && isWindowsDriveLetter(path[1]) && path[2] == ':' {
		path = path[1:] // Remove leading /
	}

	// Use filepath.Clean to normalize to OS-native separators
	return filepath.Clean(path), nil
}

// isWindowsDriveLetter returns true if c is a valid Windows drive letter (A-Z, a-z).
func isWindowsDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// BaseURL returns the file:// URL for this registry.
// The URL uses forward slashes regardless of OS, per RFC 8089.
func (r *localRegistry) BaseURL() string {
	// Convert to forward slashes for URL (required by file:// URL spec)
	urlPath := filepath.ToSlash(r.rootPath)

	// On Windows, ensure we have the leading slash before drive letter
	// C:/path -> /C:/path for file:///C:/path
	if runtime.GOOS == "windows" && len(urlPath) >= 2 && isWindowsDriveLetter(urlPath[0]) && urlPath[1] == ':' {
		urlPath = "/" + urlPath
	}

	return "file://" + urlPath
}

// GetModuleFile reads a MODULE.bazel file from the local registry.
func (r *localRegistry) GetModuleFile(ctx context.Context, moduleName, version string) (*ModuleInfo, error) {
	cacheKey := moduleName + "@" + version
	if cached, ok := r.cache.Load(cacheKey); ok {
		return cached.(*ModuleInfo), nil
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	modulePath := filepath.Join(r.rootPath, "modules", moduleName, version, "MODULE.bazel")
	data, err := os.ReadFile(modulePath) // #nosec G304 -- path is constructed from registry root, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &RegistryError{
				StatusCode: 404,
				ModuleName: moduleName,
				Version:    version,
				URL:        pathToFileURL(modulePath),
			}
		}
		return nil, fmt.Errorf("read local module file %s: %w", modulePath, err)
	}

	moduleInfo, err := ParseModuleContent(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse local module file %s: %w", modulePath, err)
	}

	r.cache.Store(cacheKey, moduleInfo)
	return moduleInfo, nil
}

// GetModuleSource reads source.json from the local registry.
func (r *localRegistry) GetModuleSource(ctx context.Context, moduleName, version string) (*registry.Source, error) {
	cacheKey := moduleName + "@" + version + ":source"
	if cached, ok := r.cache.Load(cacheKey); ok {
		return cached.(*registry.Source), nil
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	sourcePath := filepath.Join(r.rootPath, "modules", moduleName, version, "source.json")
	data, err := os.ReadFile(sourcePath) // #nosec G304 -- path is constructed from registry root, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &RegistryError{
				StatusCode: 404,
				ModuleName: moduleName,
				Version:    version,
				URL:        pathToFileURL(sourcePath),
			}
		}
		return nil, fmt.Errorf("read local source file %s: %w", sourcePath, err)
	}

	var source registry.Source
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("parse local source file %s: %w", sourcePath, err)
	}

	r.cache.Store(cacheKey, &source)
	return &source, nil
}

// GetModuleMetadata reads metadata.json from the local registry.
func (r *localRegistry) GetModuleMetadata(ctx context.Context, moduleName string) (*registry.Metadata, error) {
	if cached, ok := r.metadataCache.Load(moduleName); ok {
		return cached.(*registry.Metadata), nil
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	metadataPath := filepath.Join(r.rootPath, "modules", moduleName, "metadata.json")
	data, err := os.ReadFile(metadataPath) // #nosec G304 -- path is constructed from registry root, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &RegistryError{
				StatusCode: 404,
				ModuleName: moduleName,
				URL:        pathToFileURL(metadataPath),
			}
		}
		return nil, fmt.Errorf("read local metadata %s: %w", metadataPath, err)
	}

	var metadata registry.Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parse local metadata %s: %w", metadataPath, err)
	}

	r.metadataCache.Store(moduleName, &metadata)
	return &metadata, nil
}

// pathToFileURL converts a native file path to a file:// URL.
// Uses forward slashes and handles Windows drive letters correctly.
func pathToFileURL(path string) string {
	// Convert to forward slashes for URL
	urlPath := filepath.ToSlash(path)

	// On Windows, add leading slash before drive letter
	if len(urlPath) >= 2 && isWindowsDriveLetter(urlPath[0]) && urlPath[1] == ':' {
		urlPath = "/" + urlPath
	}

	return "file://" + urlPath
}

// Verify localRegistry implements Registry
var _ Registry = (*localRegistry)(nil)

// isFileURL checks if a URL is a file:// URL.
func isFileURL(url string) bool {
	return strings.HasPrefix(url, "file://")
}

// createRegistryClientWithAllOptions creates a registry client with all optional parameters including logger.
// Handles file:// URLs for local registries and http(s):// for remote.
// If client is nil, creates a default client with connection pooling.
// If cache is nil, no external caching is used (only applies to remote registries).
// If timeout is positive, it overrides the client's timeout (for remote registries).
// If logger is nil, logging is disabled.
func createRegistryClientWithAllOptions(url string, client *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger) (Registry, error) {
	return createRegistryClientWithAllOptionsAndTrace(url, client, cache, timeout, logger, nil)
}

func createRegistryClientWithAllOptionsAndTrace(url string, client *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger, trace *registryFileTrace) (Registry, error) {
	if isFileURL(url) {
		path, err := parseFileURL(url)
		if err != nil {
			return nil, fmt.Errorf("parse file URL %s: %w", url, err)
		}
		// Verify path exists
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("local registry path does not exist: %s", path)
			}
			return nil, fmt.Errorf("cannot access local registry path %s: %w", path, err)
		}
		// Local registries don't use external cache (they're already local)
		return newLocalRegistry(path), nil
	}

	// Remote registry
	return newRegistryClientWithAllOptionsAndTrace(url, client, cache, timeout, logger, trace), nil
}

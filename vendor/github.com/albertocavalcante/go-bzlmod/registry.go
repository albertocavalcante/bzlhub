package gobzlmod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/albertocavalcante/go-bzlmod/registry"
)

// DefaultRegistry is the Bazel Central Registry (BCR) URL.
// This matches Bazel's DEFAULT_REGISTRIES in BazelRepositoryModule.java.
const DefaultRegistry = "https://bcr.bazel.build"

// DefaultRegistryMirror is the GitHub-hosted mirror of BCR.
// This serves as a fallback when bcr.bazel.build is unavailable.
// The BCR has experienced outages due to certificate expiration.
// Using raw.githubusercontent.com provides resilience against such incidents.
//
// Reference: https://github.com/bazelbuild/bazel/issues/28101
const DefaultRegistryMirror = "https://raw.githubusercontent.com/bazelbuild/bazel-central-registry/main"

// DefaultRegistries is the default list of registries with fallback.
// BCR is tried first, with the GitHub mirror as backup.
var DefaultRegistries = []string{
	DefaultRegistry,
	DefaultRegistryMirror,
}

// HTTP client configuration constants.
const (
	defaultMaxIdleConns        = 50
	defaultMaxIdleConnsPerHost = 20
	defaultIdleConnTimeout     = 90 * time.Second
	defaultRequestTimeout      = 15 * time.Second
)

// RegistryError provides status details for registry HTTP failures.
type RegistryError struct {
	StatusCode int
	ModuleName string
	Version    string
	URL        string
	Retryable  bool
}

func (e *RegistryError) Error() string {
	if e.ModuleName != "" && e.Version != "" {
		return fmt.Sprintf("registry returned status %d for module %s@%s", e.StatusCode, e.ModuleName, e.Version)
	}
	if e.URL != "" {
		return fmt.Sprintf("registry returned status %d for %s", e.StatusCode, e.URL)
	}
	return fmt.Sprintf("registry returned status %d", e.StatusCode)
}

// Is implements errors.Is by mapping HTTP status codes to sentinel errors.
func (e *RegistryError) Is(target error) bool {
	switch e.StatusCode {
	case 404:
		if e.Version == "" {
			return target == ErrModuleNotFound
		}
		return target == ErrVersionNotFound
	case 429:
		return target == ErrRateLimited
	case 401, 403:
		return target == ErrUnauthorized
	}
	return false
}

// registryClient fetches Bazel module metadata from a registry (typically BCR).
//
// The client is optimized for high-throughput concurrent access with:
//   - Connection pooling (up to 50 idle connections, 20 per host)
//   - Thread-safe in-memory caching (module files are cached by name@version)
//   - Optional external caching (user-provided ModuleCache implementation)
//   - Configurable timeouts (15 second default)
//
// The in-memory cache is unbounded and lives for the lifetime of the client.
// For long-running processes, consider creating a new client periodically
// to clear the cache, or use an external cache with TTL/eviction policies.
type registryClient struct {
	baseURL       string
	client        *http.Client
	cache         sync.Map    // map[string]*ModuleInfo keyed by "name@version" (in-memory)
	metadataCache sync.Map    // map[string]*registry.Metadata keyed by module name
	externalCache ModuleCache // optional external cache for persistence across resolutions
	logger        *slog.Logger
	trace         *registryFileTrace

	// Mirror configuration (fetched lazily from bazel_registry.json)
	mirrors        []string
	moduleBasePath string
	mirrorsMu      sync.RWMutex
	mirrorsOnce    sync.Once
	mirrorsErr     error
}

// log returns the configured logger, or a no-op logger if none was set.
func (r *registryClient) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.New(discardHandler{})
}

// BaseURL returns the base URL of the registry (e.g., "https://bcr.bazel.build").
func (r *registryClient) BaseURL() string {
	return r.baseURL
}

func (r *registryClient) registryFileHashesSnapshot() map[string]*string {
	return r.trace.snapshot()
}

func (r *registryClient) registryFileTrace() *registryFileTrace {
	return r.trace
}

// loadMirrors fetches and caches the bazel_registry.json configuration.
// This is called once lazily on first use.
func (r *registryClient) loadMirrors(ctx context.Context) {
	r.mirrorsOnce.Do(func() {
		url := fmt.Sprintf("%s/bazel_registry.json", r.baseURL)
		logger := r.log()
		logger.Debug("fetching registry configuration", "url", url)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			r.mirrorsErr = err
			return
		}

		resp, err := r.client.Do(req)
		if err != nil {
			// Not an error - registry config is optional
			logger.Debug("registry config not available", "error", err)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			// Not an error - registry config is optional
			logger.Debug("registry config not available", "status", resp.StatusCode)
			if resp.StatusCode == http.StatusNotFound {
				r.trace.recordMissing(url)
			}
			return
		}

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Debug("failed to read registry config", "error", err)
			return
		}

		var config registry.RegistryConfig
		if err := json.Unmarshal(data, &config); err != nil {
			logger.Debug("failed to parse registry config", "error", err)
			return
		}

		r.trace.record(url, data)

		moduleBasePath := config.ModuleBasePath
		if moduleBasePath == "" {
			moduleBasePath = "modules"
		}

		r.mirrorsMu.Lock()
		r.mirrors = append([]string(nil), config.Mirrors...)
		r.moduleBasePath = moduleBasePath
		r.mirrorsMu.Unlock()

		logger.Debug("loaded registry config", "mirrors", len(config.Mirrors), "module_base_path", moduleBasePath)
	})
}

// getModuleBasePath returns the module base path (defaults to "modules").
func (r *registryClient) getModuleBasePath(ctx context.Context) string {
	r.loadMirrors(ctx)
	return r.currentModuleBasePath()
}

func (r *registryClient) currentModuleBasePath() string {
	r.mirrorsMu.RLock()
	moduleBasePath := r.moduleBasePath
	r.mirrorsMu.RUnlock()

	if moduleBasePath != "" {
		return moduleBasePath
	}
	return "modules"
}

// getMirrors returns the list of mirror URLs.
func (r *registryClient) getMirrors(ctx context.Context) []string {
	r.loadMirrors(ctx)
	r.mirrorsMu.RLock()
	defer r.mirrorsMu.RUnlock()
	return append([]string(nil), r.mirrors...)
}

// fetchWithMirrors tries to fetch a path from the primary registry and falls back to mirrors.
// Returns the response body data or an error if all attempts fail.
func (r *registryClient) fetchWithMirrors(ctx context.Context, path, moduleName, version string) ([]byte, error) {
	logger := r.log()

	// Build list of URLs to try: primary first, then mirrors
	urls := []string{fmt.Sprintf("%s/%s", r.baseURL, path)}
	for _, mirror := range r.getMirrors(ctx) {
		urls = append(urls, fmt.Sprintf("%s/%s", strings.TrimSuffix(mirror, "/"), path))
	}

	var lastErr error
	for i, url := range urls {
		if i > 0 {
			logger.Debug("trying mirror", "url", url, "attempt", i+1)
		} else {
			logger.Debug("fetching from registry", "url", url)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			lastErr = err
			continue
		}

		resp, err := r.client.Do(req)
		if err != nil {
			logger.Debug("request failed", "url", url, "error", err)
			lastErr = fmt.Errorf("fetch %s@%s from %s: %w", moduleName, version, url, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			logger.Debug("registry returned error status", "url", url, "status", resp.StatusCode)
			lastErr = &RegistryError{
				StatusCode: resp.StatusCode,
				ModuleName: moduleName,
				Version:    version,
				URL:        url,
				Retryable:  resp.StatusCode == 429 || resp.StatusCode == 503 || resp.StatusCode == 504,
			}
			// Don't try mirrors for 404 - the module doesn't exist
			if resp.StatusCode == 404 {
				return nil, lastErr
			}
			continue
		}

		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read %s@%s response from %s: %w", moduleName, version, url, err)
			continue
		}

		if i > 0 {
			logger.Debug("mirror fetch succeeded", "url", url)
		}
		return data, nil
	}

	return nil, lastErr
}

// RegistryOption configures a Registry.
type RegistryOption func(*registryConfig)

type registryConfig struct {
	httpClient *http.Client
	cache      ModuleCache
	timeout    time.Duration
	logger     *slog.Logger
}

// WithRegistryHTTPClient sets a custom HTTP client for registry requests.
func WithRegistryHTTPClient(c *http.Client) RegistryOption {
	return func(cfg *registryConfig) {
		cfg.httpClient = c
	}
}

// WithRegistryCache sets an external cache for MODULE.bazel files.
func WithRegistryCache(c ModuleCache) RegistryOption {
	return func(cfg *registryConfig) {
		cfg.cache = c
	}
}

// WithRegistryTimeout sets the HTTP request timeout.
func WithRegistryTimeout(d time.Duration) RegistryOption {
	return func(cfg *registryConfig) {
		cfg.timeout = d
	}
}

// WithRegistryLogger sets a structured logger for registry diagnostics.
func WithRegistryLogger(l *slog.Logger) RegistryOption {
	return func(cfg *registryConfig) {
		cfg.logger = l
	}
}

// NewRegistry creates a Registry that queries the given URLs in order.
// If multiple URLs are provided, the registry will try each in order until
// a module is found (registry chain behavior).
//
// Example:
//
//	reg, err := NewRegistry([]string{"https://bcr.bazel.build"})
//	reg, err := NewRegistry(DefaultRegistries, WithRegistryTimeout(30*time.Second))
func NewRegistry(urls []string, opts ...RegistryOption) (Registry, error) {
	cfg := &registryConfig{
		timeout: 15 * time.Second, // default
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if len(urls) == 0 {
		return nil, errors.New("at least one registry URL is required")
	}

	return newRegistryChainWithAllOptions(urls, cfg.httpClient, cfg.cache, cfg.timeout, cfg.logger)
}

// RegistryClient creates a registry client for dependency resolution.
//
// Deprecated: Use NewRegistry instead.
//
// With no arguments, uses BCR with GitHub mirror fallback for resilience.
// With one URL, creates a single registry client (no fallback).
// With multiple URLs, creates a registry chain that tries each in order.
//
// Supports both remote and local registries:
//   - https:// or http:// - Remote registry
//   - file:// - Local filesystem registry (for airgap/offline)
//
// When using multiple registries, modules are looked up in order and the first
// registry where a module is found is used for ALL versions of that module.
// This matches Bazel's --registry flag behavior.
//
// The default configuration provides resilience against BCR outages.
// See: https://github.com/bazelbuild/bazel/issues/28101
//
// Panics if no valid registries could be created from the provided URLs.
// This should only happen with invalid URLs; the default registries are
// guaranteed to be valid.
//
// Examples:
//
//	// Use BCR with GitHub mirror fallback (recommended)
//	reg := RegistryClient()
//
//	// Use a private registry only (no fallback)
//	reg := RegistryClient("https://registry.example.com")
//
//	// Private registry with BCR fallback
//	reg := RegistryClient("https://registry.example.com", DefaultRegistry)
//
//	// Local/airgap registry
//	reg := RegistryClient("file:///opt/bazel-registry")
//
//	// Mixed: local first, then remote fallback
//	reg := RegistryClient("file:///opt/local-modules", DefaultRegistry)
func RegistryClient(urls ...string) Registry {
	return registryWithTimeout(0, urls...)
}

// registryWithTimeout creates a registry with a custom timeout.
// If timeout is zero or negative, uses the default timeout.
func registryWithTimeout(timeout time.Duration, urls ...string) Registry {
	return registryWithHTTPClient(nil, timeout, urls...)
}

// registryWithHTTPClient creates a registry with an optional custom HTTP client.
// If httpClient is nil, creates default clients with connection pooling.
// If timeout is positive, it overrides the httpClient's timeout.
func registryWithHTTPClient(httpClient *http.Client, timeout time.Duration, urls ...string) Registry {
	return registryWithOptions(httpClient, nil, timeout, urls...)
}

// registryWithOptions creates a registry with all optional parameters.
// If httpClient is nil, creates default clients with connection pooling.
// If cache is nil, no external caching is used.
// If timeout is positive, it overrides the httpClient's timeout.
func registryWithOptions(httpClient *http.Client, cache ModuleCache, timeout time.Duration, urls ...string) Registry {
	return registryWithAllOptions(httpClient, cache, timeout, nil, urls...)
}

// registryWithAllOptions creates a registry with all optional parameters including logger.
// If httpClient is nil, creates default clients with connection pooling.
// If cache is nil, no external caching is used.
// If timeout is positive, it overrides the httpClient's timeout.
// If logger is nil, logging is disabled.
func registryWithAllOptions(httpClient *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger, urls ...string) Registry {
	return registryWithAllOptionsAndTrace(httpClient, cache, timeout, logger, nil, urls...)
}

func registryWithAllOptionsAndTrace(httpClient *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger, trace *registryFileTrace, urls ...string) Registry {
	log := logger
	if log == nil {
		log = slog.New(discardHandler{})
	}

	if len(urls) == 0 {
		// Use BCR + GitHub mirror for resilience
		chain, err := newRegistryChainWithAllOptionsAndTrace(DefaultRegistries, httpClient, cache, timeout, logger, trace)
		if err != nil {
			// This should never happen with DefaultRegistries, but fall back to BCR only
			log.Warn("failed to create default registry chain, falling back to BCR only", "error", err)
			return newRegistryClientWithAllOptionsAndTrace(DefaultRegistry, httpClient, cache, timeout, logger, trace)
		}
		return chain
	}
	if len(urls) == 1 {
		reg, err := createRegistryClientWithAllOptionsAndTrace(urls[0], httpClient, cache, timeout, logger, trace)
		if err != nil {
			// Fall back to treating it as a remote URL if parsing fails
			return newRegistryClientWithAllOptionsAndTrace(urls[0], httpClient, cache, timeout, logger, trace)
		}
		return reg
	}
	chain, err := newRegistryChainWithAllOptionsAndTrace(urls, httpClient, cache, timeout, logger, trace)
	if err != nil {
		// Fall back to treating URLs as remote registries without chain validation
		log.Warn("failed to create registry chain, using first URL only", "error", err, "urls", urls)
		return newRegistryClientWithAllOptionsAndTrace(urls[0], httpClient, cache, timeout, logger, trace)
	}
	return chain
}

// newRegistryClient creates a registryClient for the given registry URL.
func newRegistryClient(baseURL string) *registryClient {
	return newRegistryClientWithTimeout(baseURL, 0)
}

// newRegistryClientWithTimeout creates a registryClient with a custom timeout.
// If timeout is zero or negative, uses defaultRequestTimeout (15 seconds).
func newRegistryClientWithTimeout(baseURL string, timeout time.Duration) *registryClient {
	return newRegistryClientWithTimeoutAndTrace(baseURL, timeout, nil)
}

func newRegistryClientWithTimeoutAndTrace(baseURL string, timeout time.Duration, trace *registryFileTrace) *registryClient {
	return newRegistryClientWithHTTPClientAndTrace(baseURL, nil, timeout, trace)
}

// newRegistryClientWithHTTPClient creates a registryClient with an optional custom HTTP client.
// If client is nil, creates a default client with connection pooling.
// If timeout is positive, it overrides the client's timeout.
func newRegistryClientWithHTTPClient(baseURL string, client *http.Client, timeout time.Duration) *registryClient {
	return newRegistryClientWithHTTPClientAndTrace(baseURL, client, timeout, nil)
}

func newRegistryClientWithHTTPClientAndTrace(baseURL string, client *http.Client, timeout time.Duration, trace *registryFileTrace) *registryClient {
	return newRegistryClientWithOptionsAndTrace(baseURL, client, nil, timeout, trace)
}

// newRegistryClientWithOptions creates a registryClient with all optional parameters.
// If client is nil, creates a default client with connection pooling.
// If cache is nil, no external caching is used.
// If timeout is positive, it overrides the client's timeout.
func newRegistryClientWithOptions(baseURL string, client *http.Client, cache ModuleCache, timeout time.Duration) *registryClient {
	return newRegistryClientWithOptionsAndTrace(baseURL, client, cache, timeout, nil)
}

func newRegistryClientWithOptionsAndTrace(baseURL string, client *http.Client, cache ModuleCache, timeout time.Duration, trace *registryFileTrace) *registryClient {
	return newRegistryClientWithAllOptionsAndTrace(baseURL, client, cache, timeout, nil, trace)
}

// newRegistryClientWithAllOptions creates a registryClient with all optional parameters including logger.
// If client is nil, creates a default client with connection pooling.
// If cache is nil, no external caching is used.
// If timeout is positive, it overrides the client's timeout.
// If logger is nil, logging is disabled.
func newRegistryClientWithAllOptions(baseURL string, client *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger) *registryClient {
	return newRegistryClientWithAllOptionsAndTrace(baseURL, client, cache, timeout, logger, nil)
}

func newRegistryClientWithAllOptionsAndTrace(baseURL string, client *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger, trace *registryFileTrace) *registryClient {
	if client == nil {
		// Create default pooled client that honors HTTP_PROXY/HTTPS_PROXY env vars
		transport := &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        defaultMaxIdleConns,
			MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
			IdleConnTimeout:     defaultIdleConnTimeout,
			DisableCompression:  false,
		}

		actualTimeout := defaultRequestTimeout
		if timeout > 0 {
			actualTimeout = timeout
		}

		client = &http.Client{
			Timeout:   actualTimeout,
			Transport: transport,
		}
	} else if timeout > 0 {
		// Clone client with new timeout to avoid modifying the user's client
		client = &http.Client{
			Transport:     client.Transport,
			CheckRedirect: client.CheckRedirect,
			Jar:           client.Jar,
			Timeout:       timeout,
		}
	}

	return &registryClient{
		baseURL:       strings.TrimSuffix(baseURL, "/"),
		client:        client,
		externalCache: cache,
		logger:        logger,
		trace:         traceOrNew(trace),
	}
}

// GetModuleFile fetches and parses a MODULE.bazel file from the registry.
// Results are cached in memory, and optionally in an external cache if configured.
// Repeated calls for the same module@version are fast.
//
// Returns an error if the module doesn't exist, the network fails, or parsing fails.
// External cache errors are handled gracefully and do not cause resolution to fail.
func (r *registryClient) GetModuleFile(ctx context.Context, moduleName, version string) (*ModuleInfo, error) {
	cacheKey := moduleName + "@" + version
	logger := r.log()

	// 1. Check in-memory cache first (fastest)
	if cached, ok := r.cache.Load(cacheKey); ok {
		logger.Debug("module cache hit (memory)", "name", moduleName, "version", version)
		return cached.(*ModuleInfo), nil
	}

	// 2. Check external cache if configured.
	basePath := r.currentModuleBasePath()
	if r.trace != nil && r.trace.enabled {
		// When trace output is enabled, resolve the configured module_base_path
		// first so the recorded MODULE.bazel URL matches the registry config.
		basePath = r.getModuleBasePath(ctx)
	}
	path := fmt.Sprintf("%s/%s/%s/MODULE.bazel", basePath, moduleName, version)
	url := r.baseURL + "/" + path
	if r.externalCache != nil {
		if data, found, err := r.externalCache.Get(ctx, moduleName, version); err == nil && found {
			// Parse and validate the cached content
			moduleInfo, err := ParseModuleContent(string(data))
			if err == nil {
				logger.Debug("module cache hit (external)", "name", moduleName, "version", version)
				// Store in in-memory cache for faster subsequent access
				r.cache.Store(cacheKey, moduleInfo)
				r.trace.record(url, data)
				return moduleInfo, nil
			}
			logger.Debug("external cache contained invalid content", "name", moduleName, "version", version, "error", err)
			// Cache contained invalid content, fall through to fetch
		}
		// External cache error or miss, continue with registry fetch
	}

	// 3. Fetch from registry (with mirror fallback)
	path = fmt.Sprintf("%s/%s/%s/MODULE.bazel", basePath, moduleName, version)
	url = r.baseURL + "/" + path
	data, err := r.fetchWithMirrors(ctx, path, moduleName, version)
	if err != nil {
		if isNotFound(err) {
			r.trace.recordMissing(url)
		}
		return nil, err
	}

	moduleInfo, err := ParseModuleContent(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse module %s@%s: %w", moduleName, version, err)
	}

	logger.Debug("fetched module from registry", "name", moduleName, "version", version, "bytes", len(data))

	// 4. Store in external cache (errors ignored - don't break resolution)
	if r.externalCache != nil {
		// Best effort - don't fail resolution if cache write fails
		_ = r.externalCache.Put(ctx, moduleName, version, data)
	}

	// 5. Store in in-memory cache
	r.cache.Store(cacheKey, moduleInfo)
	r.trace.record(url, data)
	return moduleInfo, nil
}

// GetModuleSource fetches the source.json file for a module version.
// This describes how to fetch the module's source code (archive, git, or local_path).
// Results are cached, so repeated calls for the same module version are fast.
func (r *registryClient) GetModuleSource(ctx context.Context, moduleName, version string) (*registry.Source, error) {
	cacheKey := moduleName + "@" + version + ":source"
	logger := r.log()

	// Check in-memory cache first
	if cached, ok := r.cache.Load(cacheKey); ok {
		logger.Debug("source cache hit (memory)", "name", moduleName, "version", version)
		return cached.(*registry.Source), nil
	}

	// Fetch from registry (with mirror fallback)
	basePath := r.getModuleBasePath(ctx)
	path := fmt.Sprintf("%s/%s/%s/source.json", basePath, moduleName, version)
	url := r.baseURL + "/" + path

	data, err := r.fetchWithMirrors(ctx, path, moduleName, version)
	if err != nil {
		if isNotFound(err) {
			r.trace.recordMissing(url)
		}
		return nil, err
	}

	var source registry.Source
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("parse source %s@%s: %w", moduleName, version, err)
	}

	logger.Debug("fetched source from registry", "name", moduleName, "version", version, "type", source.Type)

	// Store in in-memory cache
	r.cache.Store(cacheKey, &source)
	r.trace.record(url, data)
	return &source, nil
}

// GetModuleMetadata fetches the metadata.json file for a module.
// This includes version list, yanked versions, maintainers, and deprecation info.
// Results are cached, so repeated calls for the same module are fast.
func (r *registryClient) GetModuleMetadata(ctx context.Context, moduleName string) (*registry.Metadata, error) {
	if cached, ok := r.metadataCache.Load(moduleName); ok {
		return cached.(*registry.Metadata), nil
	}

	// Fetch from registry (with mirror fallback)
	basePath := r.getModuleBasePath(ctx)
	path := fmt.Sprintf("%s/%s/metadata.json", basePath, moduleName)

	data, err := r.fetchWithMirrors(ctx, path, moduleName, "")
	if err != nil {
		return nil, err
	}

	var metadata registry.Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parse metadata for %s: %w", moduleName, err)
	}

	r.metadataCache.Store(moduleName, &metadata)
	return &metadata, nil
}

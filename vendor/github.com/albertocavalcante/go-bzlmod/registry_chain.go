package gobzlmod

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/albertocavalcante/go-bzlmod/registry"
)

// registryChain implements multi-registry lookup with fallback behavior.
// It tries registries in order and remembers which registry provides each module.
//
// Supports both remote (https://) and local (file://) registries,
// enabling airgap and offline workflows.
//
// Key behaviors matching Bazel's implementation (ModuleFileFunction.java:745-810):
//  1. Modules are looked up in registry order (first to last)
//  2. The first registry where a module is found is used for ALL versions of that module
//  3. If a module is not found in any registry, an error is returned
//
// Resilience improvements over Bazel:
//
// This implementation falls back to the next registry on ANY error, including:
//   - HTTP 404 (module not found)
//   - HTTP 5xx (server errors)
//   - TLS/certificate errors (Dec 2025 BCR outage)
//   - Network timeouts and connection failures
//
// Known Bazel bugs we work around:
//   - Bazel fails on source.json errors instead of trying next registry
//   - Bazel has issues with TLS error recovery across multiple URLs
//
// References:
//   - https://github.com/bazelbuild/bazel/issues/28101 (BCR TLS outage)
//   - https://github.com/bazelbuild/bazel/issues/28158 (TLS recovery bug)
//   - https://github.com/bazelbuild/bazel/issues/26442 (source.json fallback bug)
//
// By always falling back, we provide better resilience than Bazel itself.
type registryChain struct {
	clients []Registry
	trace   *registryFileTrace

	// moduleRegistry tracks which registry provides each module (by module name)
	// Once a module is found in a registry, all versions come from that registry
	moduleRegistry   map[string]int // module name -> registry index
	moduleRegistryMu sync.RWMutex
}

// newRegistryChain creates a chain of registries from URLs.
//
// Supports both remote and local registries:
//   - https:// or http:// - Remote registry
//   - file:// - Local filesystem registry (for airgap/offline)
//
// Returns an error if:
//   - No registry URLs are provided (nil or empty slice)
//   - All provided URLs are invalid or cannot be parsed
//
// If some URLs are invalid but at least one is valid, the invalid URLs
// are silently skipped and a chain is created with the valid ones.
func newRegistryChain(registryURLs []string) (*registryChain, error) {
	return newRegistryChainWithTimeout(registryURLs, 0)
}

// newRegistryChainWithTimeout creates a chain of registries with a custom timeout.
// If timeout is zero or negative, uses the default timeout.
func newRegistryChainWithTimeout(registryURLs []string, timeout time.Duration) (*registryChain, error) {
	return newRegistryChainWithHTTPClient(registryURLs, nil, timeout)
}

// newRegistryChainWithHTTPClient creates a chain of registries with an optional custom HTTP client.
// If httpClient is nil, creates default clients with connection pooling.
// If timeout is positive, it overrides the httpClient's timeout.
func newRegistryChainWithHTTPClient(registryURLs []string, httpClient *http.Client, timeout time.Duration) (*registryChain, error) {
	return newRegistryChainWithOptions(registryURLs, httpClient, nil, timeout)
}

// newRegistryChainWithOptions creates a chain of registries with all optional parameters.
// If httpClient is nil, creates default clients with connection pooling.
// If cache is nil, no external caching is used.
// If timeout is positive, it overrides the httpClient's timeout.
func newRegistryChainWithOptions(registryURLs []string, httpClient *http.Client, cache ModuleCache, timeout time.Duration) (*registryChain, error) {
	return newRegistryChainWithAllOptions(registryURLs, httpClient, cache, timeout, nil)
}

// newRegistryChainWithAllOptions creates a chain of registries with all optional parameters including logger.
// If httpClient is nil, creates default clients with connection pooling.
// If cache is nil, no external caching is used.
// If timeout is positive, it overrides the httpClient's timeout.
// If logger is nil, logging is disabled.
func newRegistryChainWithAllOptions(registryURLs []string, httpClient *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger) (*registryChain, error) {
	return newRegistryChainWithAllOptionsAndTrace(registryURLs, httpClient, cache, timeout, logger, nil)
}

func newRegistryChainWithAllOptionsAndTrace(registryURLs []string, httpClient *http.Client, cache ModuleCache, timeout time.Duration, logger *slog.Logger, trace *registryFileTrace) (*registryChain, error) {
	if len(registryURLs) == 0 {
		return nil, errors.New("no registry URLs provided")
	}

	clients := make([]Registry, 0, len(registryURLs))
	for _, url := range registryURLs {
		client, err := createRegistryClientWithAllOptionsAndTrace(url, httpClient, cache, timeout, logger, trace)
		if err != nil {
			// Log error but continue with other registries
			// In production, consider adding a warning mechanism
			continue
		}
		clients = append(clients, client)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("no valid registries could be created from %d URLs", len(registryURLs))
	}

	return &registryChain{
		clients:        clients,
		trace:          trace,
		moduleRegistry: make(map[string]int),
	}, nil
}

// GetModuleFile fetches a MODULE.bazel file using the registry chain.
// It tries registries in order for the first request for a module name,
// then caches which registry provides that module.
func (rc *registryChain) GetModuleFile(ctx context.Context, moduleName, version string) (*ModuleInfo, error) {
	// Check if we've already determined which registry provides this module
	rc.moduleRegistryMu.RLock()
	registryIdx, found := rc.moduleRegistry[moduleName]
	rc.moduleRegistryMu.RUnlock()

	if found {
		// Fast path: try the cached registry first.
		moduleInfo, err := rc.clients[registryIdx].GetModuleFile(ctx, moduleName, version)
		if err == nil {
			return moduleInfo, nil
		}

		// If the cached registry can't serve this version, fallback to others.
		// This improves resilience for partial mirrors/inconsistent registries.
		notFoundErrors := []string{fmt.Sprintf("%s: %v", rc.clients[registryIdx].BaseURL(), err)}
		for i, client := range rc.clients {
			if i == registryIdx {
				continue
			}
			moduleInfo, err := client.GetModuleFile(ctx, moduleName, version)
			if err == nil {
				return moduleInfo, nil
			}
			notFoundErrors = append(notFoundErrors, fmt.Sprintf("%s: %v", client.BaseURL(), err))
		}

		if len(notFoundErrors) == 1 {
			return nil, fmt.Errorf("module %s@%s not found: %s", moduleName, version, notFoundErrors[0])
		}
		return nil, fmt.Errorf("module %s@%s not found in any registry:\n  - %s",
			moduleName, version, strings.Join(notFoundErrors, "\n  - "))
	}

	// Try each registry in order to find the module
	var notFoundErrors []string
	for i, client := range rc.clients {
		moduleInfo, err := client.GetModuleFile(ctx, moduleName, version)
		if err == nil {
			// Success! Remember this registry for future requests for this module
			rc.moduleRegistryMu.Lock()
			if _, exists := rc.moduleRegistry[moduleName]; !exists {
				rc.moduleRegistry[moduleName] = i
			}
			rc.moduleRegistryMu.Unlock()
			return moduleInfo, nil
		}

		// Check if it's a 404 (module not found in this registry)
		if isNotFound(err) {
			notFoundErrors = append(notFoundErrors, fmt.Sprintf("%s: %v", client.BaseURL(), err))
			continue
		}

		// For other errors (TLS, network, server errors, etc.), continue to next registry.
		// This provides resilience against infrastructure issues like:
		//   - TLS certificate expiration
		//     https://github.com/bazelbuild/bazel/issues/28101
		//     https://github.com/bazelbuild/bazel/issues/28158
		//   - Server errors when source.json is missing
		//     https://github.com/bazelbuild/bazel/issues/26442
		//   - Network timeouts and connection failures
		notFoundErrors = append(notFoundErrors, fmt.Sprintf("%s: %v", client.BaseURL(), err))
		continue
	}

	// Module not found in any registry
	if len(notFoundErrors) == 1 {
		return nil, fmt.Errorf("module %s@%s not found: %s", moduleName, version, notFoundErrors[0])
	}
	return nil, fmt.Errorf("module %s@%s not found in any registry:\n  - %s",
		moduleName, version, strings.Join(notFoundErrors, "\n  - "))
}

// GetModuleMetadata fetches metadata using the registry that provides this module.
func (rc *registryChain) GetModuleMetadata(ctx context.Context, moduleName string) (*registry.Metadata, error) {
	// Check if we've already determined which registry provides this module
	rc.moduleRegistryMu.RLock()
	registryIdx, found := rc.moduleRegistry[moduleName]
	rc.moduleRegistryMu.RUnlock()

	if found {
		// Fast path: try cached registry first.
		metadata, err := rc.clients[registryIdx].GetModuleMetadata(ctx, moduleName)
		if err == nil {
			return metadata, nil
		}

		// Fallback to other registries if cached registry fails.
		var lastErr error = err
		for i, client := range rc.clients {
			if i == registryIdx {
				continue
			}
			metadata, err := client.GetModuleMetadata(ctx, moduleName)
			if err == nil {
				return metadata, nil
			}
			lastErr = err
		}

		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("metadata for module %s not found in any registry", moduleName)
	}

	// Try each registry in order to find the metadata
	var lastErr error
	for i, client := range rc.clients {
		metadata, err := client.GetModuleMetadata(ctx, moduleName)
		if err == nil {
			// Success! Remember this registry for future requests for this module
			rc.moduleRegistryMu.Lock()
			if _, exists := rc.moduleRegistry[moduleName]; !exists {
				rc.moduleRegistry[moduleName] = i
			}
			rc.moduleRegistryMu.Unlock()
			return metadata, nil
		}

		// Check if it's a 404 (module not found in this registry)
		if isNotFound(err) {
			lastErr = err
			continue
		}

		// For other errors, continue to next registry
		lastErr = err
		continue
	}

	// Module metadata not found in any registry
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("metadata for module %s not found in any registry", moduleName)
}

// BaseURL returns the URL of the first registry in the chain.
// This is used for display purposes and backwards compatibility.
func (rc *registryChain) BaseURL() string {
	if len(rc.clients) == 0 {
		return ""
	}
	return rc.clients[0].BaseURL()
}

// GetRegistryForModule returns the registry URL that provides the given module.
// Returns empty string if the module hasn't been looked up yet.
func (rc *registryChain) GetRegistryForModule(moduleName string) string {
	rc.moduleRegistryMu.RLock()
	defer rc.moduleRegistryMu.RUnlock()

	if idx, found := rc.moduleRegistry[moduleName]; found {
		return rc.clients[idx].BaseURL()
	}
	return ""
}

func (rc *registryChain) registryFileHashesSnapshot() map[string]*string {
	if rc.trace != nil {
		return rc.trace.snapshot()
	}

	var hashes map[string]*string
	for _, client := range rc.clients {
		provider, ok := client.(registryFileTraceProvider)
		if !ok {
			continue
		}
		hashes = mergeRegistryFileHashes(hashes, provider.registryFileHashesSnapshot())
	}
	return hashes
}

func (rc *registryChain) registryFileTrace() *registryFileTrace {
	return rc.trace
}

// GetModuleSource fetches source.json using the registry that provides this module.
func (rc *registryChain) GetModuleSource(ctx context.Context, moduleName, version string) (*registry.Source, error) {
	// Check if we've already determined which registry provides this module
	rc.moduleRegistryMu.RLock()
	registryIdx, found := rc.moduleRegistry[moduleName]
	rc.moduleRegistryMu.RUnlock()

	if found {
		// Fast path: try cached registry first.
		source, err := rc.clients[registryIdx].GetModuleSource(ctx, moduleName, version)
		if err == nil {
			return source, nil
		}

		// Fallback to other registries if cached registry fails.
		var lastErr error = err
		for i, client := range rc.clients {
			if i == registryIdx {
				continue
			}
			source, err := client.GetModuleSource(ctx, moduleName, version)
			if err == nil {
				return source, nil
			}
			lastErr = err
		}

		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("source.json for module %s@%s not found in any registry", moduleName, version)
	}

	// Try each registry in order to find the source
	var lastErr error
	for i, client := range rc.clients {
		source, err := client.GetModuleSource(ctx, moduleName, version)
		if err == nil {
			// Success! Remember this registry for future requests for this module
			rc.moduleRegistryMu.Lock()
			if _, exists := rc.moduleRegistry[moduleName]; !exists {
				rc.moduleRegistry[moduleName] = i
			}
			rc.moduleRegistryMu.Unlock()
			return source, nil
		}

		// Check if it's a 404 (module not found in this registry)
		if isNotFound(err) {
			lastErr = err
			continue
		}

		// For other errors, continue to next registry
		lastErr = err
		continue
	}

	// Module source not found in any registry
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("source.json for module %s@%s not found in any registry", moduleName, version)
}

// Registry provides access to Bazel module registries.
// Implementations fetch MODULE.bazel files and module metadata from registries.
//
// This interface is exported to allow users to implement custom registries
// or create mocks for testing.
type Registry interface {
	// GetModuleFile fetches and parses a MODULE.bazel file for the given module version.
	GetModuleFile(ctx context.Context, moduleName, version string) (*ModuleInfo, error)

	// GetModuleMetadata fetches metadata for a module (available versions, yanked info, etc).
	GetModuleMetadata(ctx context.Context, moduleName string) (*registry.Metadata, error)

	// GetModuleSource fetches the source.json file for a module version.
	// This describes how to fetch the module's source code (archive, git, or local_path).
	GetModuleSource(ctx context.Context, moduleName, version string) (*registry.Source, error)

	// BaseURL returns the base URL of this registry.
	BaseURL() string
}

// Verify that both types implement the interface
var _ Registry = (*registryClient)(nil)
var _ Registry = (*registryChain)(nil)

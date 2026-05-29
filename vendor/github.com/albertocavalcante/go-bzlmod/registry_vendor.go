package gobzlmod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/albertocavalcante/go-bzlmod/registry"
)

// vendorRegistry provides module data from a vendor directory.
// This enables offline/airgap workflows where modules are vendored locally.
//
// The vendor directory should have the following structure:
//
//	{vendor}/modules/{name}/{version}/MODULE.bazel
//	{vendor}/modules/{name}/{version}/source.json  (optional)
//	{vendor}/modules/{name}/metadata.json          (optional)
//
// This matches Bazel's --vendor_dir flag behavior.
//
// Reference: IndexRegistry.java lines 217-230
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/IndexRegistry.java
type vendorRegistry struct {
	local *localRegistry
}

// newVendorRegistry creates a registry client for a vendor directory.
// Returns an error if the vendor directory doesn't exist.
func newVendorRegistry(vendorDir string) (*vendorRegistry, error) {
	// Verify the vendor directory exists
	info, err := os.Stat(vendorDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("vendor directory does not exist: %s", vendorDir)
		}
		return nil, fmt.Errorf("cannot access vendor directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vendor path is not a directory: %s", vendorDir)
	}

	return &vendorRegistry{
		local: newLocalRegistry(vendorDir),
	}, nil
}

// BaseURL returns the file:// URL for the vendor directory.
func (v *vendorRegistry) BaseURL() string {
	return v.local.BaseURL()
}

// GetModuleFile reads a MODULE.bazel file from the vendor directory.
func (v *vendorRegistry) GetModuleFile(ctx context.Context, moduleName, version string) (*ModuleInfo, error) {
	return v.local.GetModuleFile(ctx, moduleName, version)
}

// GetModuleMetadata reads metadata.json from the vendor directory.
// If metadata.json doesn't exist, returns a synthesized metadata with just the
// versions found in the vendor directory.
func (v *vendorRegistry) GetModuleMetadata(ctx context.Context, moduleName string) (*registry.Metadata, error) {
	// First try to read the actual metadata.json
	metadata, err := v.local.GetModuleMetadata(ctx, moduleName)
	if err == nil {
		return metadata, nil
	}

	// If metadata.json doesn't exist, synthesize it from vendored versions
	if isNotFound(err) {
		return v.synthesizeMetadata(moduleName)
	}

	return nil, fmt.Errorf("get vendor module metadata %s: %w", moduleName, err)
}

// GetModuleSource reads source.json from the vendor directory.
// For vendored modules, the source.json is optional since the module
// is already available locally.
func (v *vendorRegistry) GetModuleSource(ctx context.Context, moduleName, version string) (*registry.Source, error) {
	source, err := v.local.GetModuleSource(ctx, moduleName, version)
	if err == nil {
		return source, nil
	}

	// If source.json doesn't exist, synthesize a local_path source
	if isNotFound(err) {
		modulePath := filepath.Join(v.local.rootPath, "modules", moduleName, version)
		return &registry.Source{
			Type: "local_path",
			Path: modulePath,
		}, nil
	}

	return nil, fmt.Errorf("get vendor module source %s@%s: %w", moduleName, version, err)
}

// synthesizeMetadata creates a Metadata object by scanning the vendor directory
// for available versions of a module.
func (v *vendorRegistry) synthesizeMetadata(moduleName string) (*registry.Metadata, error) {
	moduleDir := filepath.Join(v.local.rootPath, "modules", moduleName)

	// Check if the module directory exists
	if _, err := os.Stat(moduleDir); err != nil {
		if os.IsNotExist(err) {
			return nil, &RegistryError{
				StatusCode: 404,
				ModuleName: moduleName,
				URL:        pathToFileURL(moduleDir),
			}
		}
		return nil, fmt.Errorf("cannot access module directory: %w", err)
	}

	// List all version directories
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return nil, fmt.Errorf("read module directory %s: %w", moduleDir, err)
	}

	var versions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Skip non-version files like metadata.json
		versionDir := entry.Name()
		moduleBazelPath := filepath.Join(moduleDir, versionDir, "MODULE.bazel")
		if _, err := os.Stat(moduleBazelPath); err == nil {
			versions = append(versions, versionDir)
		}
	}

	if len(versions) == 0 {
		return nil, &RegistryError{
			StatusCode: 404,
			ModuleName: moduleName,
			URL:        pathToFileURL(moduleDir),
		}
	}

	return &registry.Metadata{
		Versions: versions,
	}, nil
}

// HasModule checks if a module exists in the vendor directory.
func (v *vendorRegistry) HasModule(moduleName string) bool {
	moduleDir := filepath.Join(v.local.rootPath, "modules", moduleName)
	info, err := os.Stat(moduleDir)
	return err == nil && info.IsDir()
}

// HasVersion checks if a specific version of a module exists in the vendor directory.
func (v *vendorRegistry) HasVersion(moduleName, version string) bool {
	modulePath := filepath.Join(v.local.rootPath, "modules", moduleName, version, "MODULE.bazel")
	_, err := os.Stat(modulePath)
	return err == nil
}

// Verify vendorRegistry implements Registry
var _ Registry = (*vendorRegistry)(nil)

// vendorChain wraps a vendor registry with a fallback to remote registries.
// It tries the vendor registry first, and falls back to the remote registry
// if the module is not found in the vendor directory.
type vendorChain struct {
	vendor *vendorRegistry
	remote Registry
}

// newVendorChain creates a registry chain that checks the vendor directory first.
func newVendorChain(vendor *vendorRegistry, remote Registry) *vendorChain {
	return &vendorChain{
		vendor: vendor,
		remote: remote,
	}
}

// BaseURL returns the URL of the remote registry.
func (vc *vendorChain) BaseURL() string {
	return vc.remote.BaseURL()
}

func (vc *vendorChain) registryFileHashesSnapshot() map[string]*string {
	provider, ok := vc.remote.(registryFileTraceProvider)
	if !ok {
		return nil
	}
	return provider.registryFileHashesSnapshot()
}

func (vc *vendorChain) registryFileTrace() *registryFileTrace {
	carrier, ok := vc.remote.(registryFileTraceCarrier)
	if !ok {
		return nil
	}
	return carrier.registryFileTrace()
}

// GetModuleFile tries the vendor directory first, then falls back to remote.
func (vc *vendorChain) GetModuleFile(ctx context.Context, moduleName, version string) (*ModuleInfo, error) {
	// Check if the module exists in the vendor directory
	if vc.vendor.HasVersion(moduleName, version) {
		return vc.vendor.GetModuleFile(ctx, moduleName, version)
	}

	// Fall back to remote registry
	return vc.remote.GetModuleFile(ctx, moduleName, version)
}

// GetModuleMetadata tries the vendor directory first, then falls back to remote.
func (vc *vendorChain) GetModuleMetadata(ctx context.Context, moduleName string) (*registry.Metadata, error) {
	// Check if the module exists in the vendor directory
	if vc.vendor.HasModule(moduleName) {
		return vc.vendor.GetModuleMetadata(ctx, moduleName)
	}

	// Fall back to remote registry
	return vc.remote.GetModuleMetadata(ctx, moduleName)
}

// GetModuleSource tries the vendor directory first, then falls back to remote.
func (vc *vendorChain) GetModuleSource(ctx context.Context, moduleName, version string) (*registry.Source, error) {
	// Check if the module exists in the vendor directory
	if vc.vendor.HasVersion(moduleName, version) {
		return vc.vendor.GetModuleSource(ctx, moduleName, version)
	}

	// Fall back to remote registry
	return vc.remote.GetModuleSource(ctx, moduleName, version)
}

// Verify vendorChain implements Registry
var _ Registry = (*vendorChain)(nil)

package gobzlmod

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	registrytypes "github.com/albertocavalcante/go-bzlmod/registry"
)

type registryFileTrace struct {
	enabled bool
	mu      sync.Mutex
	hashes  map[string]*string
}

func newRegistryFileTrace() *registryFileTrace {
	return &registryFileTrace{
		enabled: true,
		hashes:  make(map[string]*string),
	}
}

func (t *registryFileTrace) record(url string, data []byte) {
	if t == nil || !t.enabled || url == "" {
		return
	}

	hash := sha256HexBytes(data)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.hashes[url] = stringPointer(hash)
}

func (t *registryFileTrace) recordMissing(url string) {
	if t == nil || !t.enabled || url == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.hashes[url] = nil
}

func (t *registryFileTrace) snapshot() map[string]*string {
	if t == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	return cloneRegistryFileHashes(t.hashes)
}

type registryFileTraceProvider interface {
	registryFileHashesSnapshot() map[string]*string
}

type registryFileTraceCarrier interface {
	registryFileTrace() *registryFileTrace
}

func cloneRegistryFileHashes(src map[string]*string) map[string]*string {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]*string, len(src))
	for url, hash := range src {
		dst[url] = cloneStringPointer(hash)
	}
	return dst
}

func mergeRegistryFileHashes(dst, src map[string]*string) map[string]*string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]*string, len(src))
	}
	for url, hash := range src {
		dst[url] = cloneStringPointer(hash)
	}
	return dst
}

func cloneStringPointer(s *string) *string {
	if s == nil {
		return nil
	}
	return stringPointer(*s)
}

func stringPointer(s string) *string {
	v := s
	return &v
}

func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func collectRegistryFileHashes(reg Registry) map[string]*string {
	provider, ok := reg.(registryFileTraceProvider)
	if !ok {
		return nil
	}
	return provider.registryFileHashesSnapshot()
}

func sharedRegistryFileTrace(reg Registry) *registryFileTrace {
	carrier, ok := reg.(registryFileTraceCarrier)
	if !ok {
		return nil
	}
	return carrier.registryFileTrace()
}

func traceOrNew(trace *registryFileTrace) *registryFileTrace {
	if trace != nil {
		return trace
	}
	return &registryFileTrace{}
}

func newRegistryTraceIfEnabled(enabled bool) *registryFileTrace {
	if !enabled {
		return nil
	}
	return newRegistryFileTrace()
}

func overrideIndex(overrides []Override) map[string]Override {
	if len(overrides) == 0 {
		return nil
	}

	index := make(map[string]Override, len(overrides))
	for _, override := range overrides {
		if override.ModuleName == "" {
			continue
		}
		index[override.ModuleName] = override
	}
	return index
}

func isNonRegistryOverride(override Override) bool {
	switch override.Type {
	case overrideTypeGit, overrideTypeLocalPath, overrideTypeArchive:
		return true
	default:
		return false
	}
}

func registryURLForModule(defaultRegistry, moduleName string, overrides map[string]Override) string {
	if override, ok := overrides[moduleName]; ok {
		if isNonRegistryOverride(override) {
			return ""
		}
		if override.Registry != "" {
			return override.Registry
		}
	}
	return defaultRegistry
}

func sourceInfoFromRegistry(source *registrytypes.Source) *SourceInfo {
	if source == nil {
		return nil
	}

	sourceType := source.Type
	if sourceType == "" {
		sourceType = "archive"
	}

	return &SourceInfo{
		Type:        sourceType,
		URL:         source.URL,
		Integrity:   source.Integrity,
		StripPrefix: source.StripPrefix,
		Remote:      source.Remote,
		Commit:      source.Commit,
		Tag:         source.Tag,
		Path:        source.Path,
	}
}

func sourceInfoFromOverride(override Override) *SourceInfo {
	if override.Type != overrideTypeLocalPath || override.Path == "" {
		return nil
	}

	return &SourceInfo{
		Type: "local_path",
		Path: override.Path,
	}
}

func enrichResolutionList(ctx context.Context, reg Registry, opts ResolutionOptions, overrides []Override, list *ResolutionList) error {
	if !opts.TraceRegistryFiles || list == nil || reg == nil {
		return nil
	}

	overridesByModule := overrideIndex(overrides)
	trace := sharedRegistryFileTrace(reg)

	for i := range list.Modules {
		module := &list.Modules[i]

		if override, ok := overridesByModule[module.Name]; ok {
			if isNonRegistryOverride(override) {
				module.Source = sourceInfoFromOverride(override)
				continue
			}
			if override.Registry != "" {
				overrideRegistry := registryWithAllOptionsAndTrace(
					opts.HTTPClient,
					opts.Cache,
					opts.Timeout,
					opts.Logger,
					trace,
					override.Registry,
				)
				source, err := overrideRegistry.GetModuleSource(ctx, module.Name, module.Version)
				if err != nil {
					return fmt.Errorf("fetch source for %s@%s: %w", module.Name, module.Version, err)
				}
				module.Source = sourceInfoFromRegistry(source)
				continue
			}
		}
		if module.Registry == "" {
			continue
		}

		source, err := reg.GetModuleSource(ctx, module.Name, module.Version)
		if err != nil {
			return fmt.Errorf("fetch source for %s@%s: %w", module.Name, module.Version, err)
		}
		module.Source = sourceInfoFromRegistry(source)
	}

	if hashes := collectRegistryFileHashes(reg); len(hashes) > 0 {
		list.RegistryFileHashes = hashes
	}

	return nil
}

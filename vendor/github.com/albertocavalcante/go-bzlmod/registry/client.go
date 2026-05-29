package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client configuration defaults.
const (
	DefaultMaxIdleConns        = 50
	DefaultMaxIdleConnsPerHost = 20
	DefaultIdleConnTimeout     = 90 * time.Second
	DefaultRequestTimeout      = 15 * time.Second
)

// Client fetches and validates data from a Bazel module registry.
type Client struct {
	baseURL   string
	client    *http.Client
	validator *Validator

	// Cache for metadata and source files
	metadataCache sync.Map // map[string]*Metadata keyed by module name
	sourceCache   sync.Map // map[string]*Source keyed by "name@version"

	// Options
	validateResponses bool
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithValidation enables or disables JSON Schema validation of responses.
func WithValidation(enabled bool) ClientOption {
	return func(c *Client) {
		c.validateResponses = enabled
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.client = client
	}
}

// WithTimeout sets a custom HTTP request timeout.
// Zero or negative values fall back to the default timeout (15 seconds).
// This option is useful for slow networks or testing scenarios.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		if timeout > 0 {
			c.client.Timeout = timeout
		} else {
			c.client.Timeout = DefaultRequestTimeout
		}
	}
}

// NewClient creates a client for the given registry URL.
//
// By default, responses are validated against BCR JSON schemas.
// Use WithValidation(false) to disable validation for performance.
func NewClient(baseURL string, opts ...ClientOption) *Client {
	transport := &http.Transport{
		MaxIdleConns:        DefaultMaxIdleConns,
		MaxIdleConnsPerHost: DefaultMaxIdleConnsPerHost,
		IdleConnTimeout:     DefaultIdleConnTimeout,
		DisableCompression:  false,
	}

	c := &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client: &http.Client{
			Timeout:   DefaultRequestTimeout,
			Transport: transport,
		},
		validator:         NewValidator(),
		validateResponses: true,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// BaseURL returns the registry base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// GetMetadata fetches and parses a module's metadata.json.
// Results are cached by module name.
func (c *Client) GetMetadata(ctx context.Context, moduleName string) (*Metadata, error) {
	if cached, ok := c.metadataCache.Load(moduleName); ok {
		return cached.(*Metadata), nil
	}

	url := fmt.Sprintf("%s/modules/%s/metadata.json", c.baseURL, moduleName)
	data, err := c.fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata for %s: %w", moduleName, err)
	}

	if c.validateResponses {
		if err := c.validator.ValidateMetadata(data); err != nil {
			return nil, fmt.Errorf("metadata validation failed for %s: %w", moduleName, err)
		}
	}

	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata for %s: %w", moduleName, err)
	}

	c.metadataCache.Store(moduleName, &metadata)
	return &metadata, nil
}

// GetSource fetches and parses a module version's source.json.
// Results are cached by "name@version".
func (c *Client) GetSource(ctx context.Context, moduleName, version string) (*Source, error) {
	cacheKey := moduleName + "@" + version
	if cached, ok := c.sourceCache.Load(cacheKey); ok {
		return cached.(*Source), nil
	}

	url := fmt.Sprintf("%s/modules/%s/%s/source.json", c.baseURL, moduleName, version)
	data, err := c.fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch source for %s@%s: %w", moduleName, version, err)
	}

	if c.validateResponses {
		if err := c.validator.ValidateSource(data); err != nil {
			return nil, fmt.Errorf("source validation failed for %s@%s: %w", moduleName, version, err)
		}
	}

	var source Source
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("failed to parse source for %s@%s: %w", moduleName, version, err)
	}

	c.sourceCache.Store(cacheKey, &source)
	return &source, nil
}

// GetModuleFile fetches the raw MODULE.bazel content for a module version.
func (c *Client) GetModuleFile(ctx context.Context, moduleName, version string) ([]byte, error) {
	url := fmt.Sprintf("%s/modules/%s/%s/MODULE.bazel", c.baseURL, moduleName, version)
	return c.fetch(ctx, url)
}

// GetRegistryConfig fetches the registry's bazel_registry.json configuration.
func (c *Client) GetRegistryConfig(ctx context.Context) (*RegistryConfig, error) {
	url := fmt.Sprintf("%s/bazel_registry.json", c.baseURL)
	data, err := c.fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry config: %w", err)
	}

	var config RegistryConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse registry config: %w", err)
	}

	return &config, nil
}

// ClearCache removes all cached data.
func (c *Client) ClearCache() {
	c.metadataCache = sync.Map{}
	c.sourceCache = sync.Map{}
}

// fetch performs an HTTP GET and returns the response body.
func (c *Client) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

// ModuleVersionInfo combines metadata and source for a specific version.
type ModuleVersionInfo struct {
	Name     string
	Version  string
	Metadata *Metadata
	Source   *Source
}

// GetModuleVersion fetches both metadata and source for a module version.
func (c *Client) GetModuleVersion(ctx context.Context, moduleName, version string) (*ModuleVersionInfo, error) {
	metadata, err := c.GetMetadata(ctx, moduleName)
	if err != nil {
		return nil, err
	}

	if !metadata.HasVersion(version) {
		return nil, fmt.Errorf("version %s not found for module %s", version, moduleName)
	}

	source, err := c.GetSource(ctx, moduleName, version)
	if err != nil {
		return nil, err
	}

	return &ModuleVersionInfo{
		Name:     moduleName,
		Version:  version,
		Metadata: metadata,
		Source:   source,
	}, nil
}

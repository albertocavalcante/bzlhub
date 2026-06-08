package artifactory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/albertocavalcante/go-bcr-httpstore"
)

// Properties is a client for Artifactory's Properties API. Each
// artifact path in Artifactory carries a sidecar key-value store
// where values are lists of strings — useful for tagging modules
// with build metadata, release status, source provenance, etc.
//
// Construct via NewProperties(backend, repo). Backend must be a
// httpstore.Backend pointed at the Artifactory root URL; the
// repo name participates in path construction (same convention
// as Layout).
type Properties struct {
	backend *httpstore.Backend
	repo    string
}

// NewProperties constructs a Properties client. Returns
// httpstore.ErrInvalidOptions when backend is nil or repo is empty.
func NewProperties(backend *httpstore.Backend, repo string) (Properties, error) {
	if backend == nil {
		return Properties{}, fmt.Errorf("%w: backend is required", httpstore.ErrInvalidOptions)
	}
	if repo == "" {
		return Properties{}, fmt.Errorf("%w: repo name is required", httpstore.ErrInvalidOptions)
	}
	return Properties{backend: backend, repo: repo}, nil
}

// propertiesResponse is Artifactory's ?properties response shape:
//
//	{
//	  "uri":        "https://.../api/storage/<repo>/<path>",
//	  "properties": {"key1": ["val1"], "key2": ["v2a","v2b"]}
//	}
type propertiesResponse struct {
	URI        string              `json:"uri"`
	Properties map[string][]string `json:"properties"`
}

// Get returns all properties at relPath under the configured repo.
// Returns:
//
//   - Properties map (key → list of values; values can be empty
//     list if Artifactory stored a key with no values, though
//     that's rare).
//   - httpstore.ErrUpstream404 wrapped, when the path has no
//     properties (Artifactory returns 404 for both "path doesn't
//     exist" and "path has no properties" — operators can't
//     distinguish via this API alone; use Stat for existence).
//   - httpstore.ErrIndexUnreadable on JSON parse failure.
//   - httpstore.ErrUnauthorized / ErrForbidden / ErrUpstreamStatus
//     for the corresponding HTTP statuses.
func (p Properties) Get(ctx context.Context, relPath string) (map[string][]string, error) {
	storagePath := path.Join("api/storage", p.repo, relPath)
	query := url.Values{"properties": []string{""}}
	resp, err := p.backend.Do(ctx, http.MethodGet, storagePath, query, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := mapPropertiesStatus(resp, http.MethodGet, storagePath); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read properties body for %s: %v",
			httpstore.ErrIndexUnreadable, storagePath, err)
	}
	var parsed propertiesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: parse properties body for %s: %v",
			httpstore.ErrIndexUnreadable, storagePath, err)
	}
	if parsed.Properties == nil {
		// 200 with no properties field — return empty map so callers
		// can range over the result without a nil-check.
		return map[string][]string{}, nil
	}
	return parsed.Properties, nil
}

// Set writes the given properties at relPath under the configured
// repo. Additive semantic: keys present in props are upserted
// (their existing values replaced); keys absent from props are
// preserved on the upstream. To remove keys explicitly, use Delete.
//
// Multi-value keys are supported (Artifactory's native shape):
//
//	props = map[string][]string{
//	    "build.id":     {"1234"},
//	    "release.tags": {"stable", "lts", "production"},
//	}
//
// Empty props is a no-op (returns nil; no upstream call).
//
// Returns httpstore.ErrUnauthorized / ErrForbidden /
// ErrUpstreamStatus for the corresponding HTTP statuses.
func (p Properties) Set(ctx context.Context, relPath string, props map[string][]string) error {
	if len(props) == 0 {
		return nil
	}
	storagePath := path.Join("api/storage", p.repo, relPath)
	query := url.Values{
		"properties": []string{encodeProperties(props)},
		"recursive":  []string{"0"},
	}
	resp, err := p.backend.Do(ctx, http.MethodPut, storagePath, query, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return mapPropertiesStatus(resp, http.MethodPut, storagePath)
}

// Delete removes the named property keys at relPath. Properties
// at relPath whose keys aren't in `keys` are preserved.
//
// Empty `keys` is a no-op (returns nil; no upstream call).
//
// Returns httpstore.ErrUnauthorized / ErrForbidden /
// ErrUpstreamStatus for the corresponding HTTP statuses.
func (p Properties) Delete(ctx context.Context, relPath string, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	storagePath := path.Join("api/storage", p.repo, relPath)
	query := url.Values{"properties": []string{strings.Join(keys, ",")}}
	resp, err := p.backend.Do(ctx, http.MethodDelete, storagePath, query, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return mapPropertiesStatus(resp, http.MethodDelete, storagePath)
}

// encodeProperties renders a map[string][]string as Artifactory's
// `?properties=` query value: keys joined by '|', values joined by
// ','. Sorted for determinism (so test fixtures remain stable
// across map-iteration randomisation).
//
// Example: {"build.id": ["1234"], "release.tags": ["stable", "lts"]}
// → "build.id=1234|release.tags=stable,lts"
func encodeProperties(props map[string][]string) string {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strings.Join(props[k], ","))
	}
	return strings.Join(parts, "|")
}

// mapPropertiesStatus translates an Artifactory response status
// into a typed sentinel. Mirrors httpstore's mapWriteStatus for
// write methods, plus 404 handling for the read path.
func mapPropertiesStatus(resp *http.Response, method, storagePath string) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s %s", httpstore.ErrUpstream404, method, storagePath)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s %s", httpstore.ErrUnauthorized, method, storagePath)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s %s", httpstore.ErrForbidden, method, storagePath)
	default:
		return fmt.Errorf("%w: %s %s -> %d %s",
			httpstore.ErrUpstreamStatus, method, storagePath, resp.StatusCode, resp.Status)
	}
}

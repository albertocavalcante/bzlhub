package artifactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/albertocavalcante/go-bcr-httpstore"
)

// Layout implements httpstore.Layout against Artifactory's storage
// API. Construct via New(repo).
//
// The zero value is invalid (empty repo); always go through New so
// the empty-repo check fires loudly at construction rather than
// silently producing 404s at first call.
type Layout struct {
	// repo is the Artifactory repository name. Baked into URL
	// construction as the /api/storage/<repo>/... prefix.
	repo string
}

// Compile-time guard: Layout implements httpstore.Layout.
var _ httpstore.Layout = Layout{}

// New constructs a Layout for the given Artifactory repository name.
// Returns httpstore.ErrInvalidOptions when repo is empty — there's
// no useful default and silently allowing an empty repo would build
// invalid URLs that 404 confusingly at first use.
//
// The returned value is safe for concurrent use (it carries no
// mutable state beyond the repo name).
func New(repo string) (Layout, error) {
	if repo == "" {
		return Layout{}, fmt.Errorf("%w: repo name is required (the Artifactory repository to list under)",
			httpstore.ErrInvalidOptions)
	}
	return Layout{repo: repo}, nil
}

// Repo returns the configured Artifactory repository name. Exposed
// for diagnostics + logging — production code rarely needs it after
// construction.
func (l Layout) Repo() string { return l.repo }

// storageEntry is one entry inside Artifactory's storage response.
// Kept unexported so this package owns the wire format; consumers
// only see the public Layout interface.
//
// Artifactory's documented schema:
//
//	{
//	  "uri":    "/<name>",   // leading slash, relative to the parent dir
//	  "folder": true/false
//	}
type storageEntry struct {
	URI    string `json:"uri"`
	Folder bool   `json:"folder"`
}

// storageResponse is Artifactory's full GET /api/storage/<repo>/<path>
// response shape. Other fields (createdBy, lastModified, etc.) are
// present but irrelevant for listing.
type storageResponse struct {
	Repo     string         `json:"repo"`
	Path     string         `json:"path"`
	Children []storageEntry `json:"children"`
}

// ListModules enumerates `/api/storage/<repo>/modules/` and returns
// folder-typed children's names, sorted. Non-folder entries
// (e.g. _index.json if present) are filtered out.
//
// Soft-fail on 404: returns (nil, nil) so consumers can render
// "no modules yet" instead of an error banner. Mirrors the
// httpstore.HTMLAutoindex.ListModules contract.
func (l Layout) ListModules(ctx context.Context, r httpstore.Reader) ([]string, error) {
	resp, err := l.readStorage(ctx, r, "modules")
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	names := folderNames(resp.Children)
	sort.Strings(names)
	return names, nil
}

// ListVersions enumerates `/api/storage/<repo>/modules/<module>/`
// and returns the folder-typed children's names (the versions),
// sorted. Non-folder entries (notably `metadata.json`, which lives
// alongside the version directories) are filtered.
//
// Returns httpstore.ErrModuleNotFound on 404 of the module
// directory — distinguishable from "module exists but no versions
// yet" (200 with empty children list → returns []string{}).
func (l Layout) ListVersions(ctx context.Context, r httpstore.Reader, module string) ([]string, error) {
	resp, err := l.readStorage(ctx, r, path.Join("modules", module))
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s", httpstore.ErrModuleNotFound, module)
		}
		return nil, err
	}
	names := folderNames(resp.Children)
	sort.Strings(names)
	return names, nil
}

// readStorage performs the GET /api/storage/<repo>/<subPath> call
// and parses the response. subPath is appended to the API prefix
// verbatim; callers pass paths like "modules" or "modules/foo".
//
// Returns the wrapped errHTTP404 sentinel from Reader.ReadIndex
// pass-through on 404 (callers map to soft-fail or
// ErrModuleNotFound). httpstore.ErrIndexUnreadable on JSON parse
// failure. Other errors surface verbatim.
func (l Layout) readStorage(ctx context.Context, r httpstore.Reader, subPath string) (*storageResponse, error) {
	relPath := path.Join("api/storage", l.repo, subPath)
	body, err := r.ReadIndex(ctx, relPath)
	if err != nil {
		return nil, err
	}
	var resp storageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: parse Artifactory storage response for %s: %v",
			httpstore.ErrIndexUnreadable, relPath, err)
	}
	return &resp, nil
}

// folderNames extracts the names of folder-typed children from an
// Artifactory storage response, stripping the leading-slash
// convention on the URI field. Returns a fresh slice; callers are
// free to sort or mutate.
func folderNames(children []storageEntry) []string {
	names := make([]string, 0, len(children))
	for _, c := range children {
		if !c.Folder {
			continue
		}
		name := strings.TrimPrefix(c.URI, "/")
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// isNotFound returns true when err is httpstore's path-generic 404
// sentinel (ErrUpstream404, exported in httpstore v0.2.1). Layout
// implementations use this to map 404s into their preferred
// semantic — soft-fail for ListModules, ErrModuleNotFound for
// ListVersions.
func isNotFound(err error) bool {
	return errors.Is(err, httpstore.ErrUpstream404)
}

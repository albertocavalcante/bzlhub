package httpstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Reader is the narrow interface Layout implementations receive.
// It provides exactly the two capabilities a Layout needs: read an
// arbitrary path under BaseURL, and know what that BaseURL is for
// error messages.
//
// Layout impls authored against Reader (rather than *Backend) get a
// clean contract — they can't accidentally reach into Backend's
// Auth credentials or Cache state. *Backend implements Reader.
type Reader interface {
	// ReadIndex reads bytes at relPath under the configured store.
	// Routes through the configured Cache (so Layout's index file
	// also benefits from ETag-aware caching). Returns the raw
	// ErrUpstream404-wrapped error on 404, ErrUpstreamStatus on other
	// non-2xx, body bytes on 2xx — same semantics as the BCR-typed
	// Read* methods but path-generic.
	ReadIndex(ctx context.Context, relPath string) ([]byte, error)

	// BaseURL returns the normalised root URL the Reader reads from.
	// Useful for layout impls building diagnostic error messages.
	BaseURL() *url.URL
}

// Layout abstracts "how do I enumerate modules / versions in this
// store?" Plain HTTP has no universal listing protocol; this
// interface lets consumers pick the right discovery mechanism
// for their substrate.
//
// Implementations receive a Reader so they can issue authenticated,
// cache-aware reads through the calling Backend. They MUST NOT
// construct their own *http.Client (so the configured Auth +
// http.Client + Cache stay load-bearing).
type Layout interface {
	// ListModules returns every module name discoverable via this
	// layout. Soft-fails (empty list, nil error) when the index
	// is missing — operators surface that in the UI as "no
	// modules indexed yet" rather than an error page. Hard
	// parse failures of an existing index surface as
	// ErrIndexUnreadable.
	ListModules(ctx context.Context, r Reader) ([]string, error)

	// ListVersions returns the versions of a module. Returns
	// ErrModuleNotFound when the layout's index knows the
	// module is absent.
	ListVersions(ctx context.Context, r Reader, module string) ([]string, error)
}

// HTMLAutoindex enumerates modules + versions by parsing autoindex
// HTML pages — nginx's `autoindex on;`, Caddy's `file_server browse`,
// or any HTTP server that returns an HTML listing for directory
// requests. Use this for vanilla nginx-fronted BCR mirrors where
// the operator can't (or won't) publish a structured index.
//
// Parsing tolerates the most common autoindex shapes:
//
//   - nginx default: `<pre>` block with `<a href="name/">name/</a>`
//   - Caddy file_server: `<tbody>` with `<tr><td><a href="...">`
//   - Apache mod_autoindex: similar to nginx with extra `<img>` icons
//
// Entries are filtered to directory-shaped hrefs (ending with `/`)
// since BCR modules and versions are always directories. Hidden
// dotfiles (".", "..", anything starting with ".") are skipped.
// Query strings on hrefs (?C=N;O=A sort links) are stripped before
// extracting the name.
//
// Soft-fail behaviour: a 404 on the autoindex page returns
// `(nil, nil)` from ListModules so the consumer can render "no
// modules yet" rather than an error.
//
// Caveats: parsing HTML across server implementations is inherently
// fuzzy. If the operator has a custom theme, custom XSL transform,
// or returns JSON for directory requests instead of HTML, this
// layout won't enumerate correctly. Write a custom Layout if this
// shape doesn't fit your substrate.
type HTMLAutoindex struct{}

// Compile-time guard.
var _ Layout = HTMLAutoindex{}

// ListModules enumerates `<BaseURL>/modules/` and returns the
// directory entries (each entry = one module name).
func (HTMLAutoindex) ListModules(ctx context.Context, r Reader) ([]string, error) {
	names, err := listDirHTML(ctx, r, "modules/")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// ListVersions enumerates `<BaseURL>/modules/<module>/` and
// returns the directory entries minus `metadata.json` (the only
// non-directory entry expected in a well-formed module dir).
// Returns ErrModuleNotFound on 404 of the module directory.
func (HTMLAutoindex) ListVersions(ctx context.Context, r Reader, module string) ([]string, error) {
	names, err := listDirHTML(ctx, r, path.Join("modules", module)+"/")
	if err != nil {
		if errors.Is(err, ErrUpstream404) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, err
	}
	if names == nil {
		// Empty page (server returned 200 with no entries) — treat
		// as "module exists but no versions yet" — empty slice.
		return []string{}, nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == "metadata.json" {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// listDirHTML fetches the autoindex page at relPath and returns
// the directory entries (without trailing slashes). Returns
// ErrUpstream404 unwrapped so callers translate at the typed boundary.
func listDirHTML(ctx context.Context, r Reader, relPath string) ([]string, error) {
	body, err := r.ReadIndex(ctx, relPath)
	if err != nil {
		return nil, err
	}
	return parseAutoindexHTML(body)
}

// parseAutoindexHTML walks the HTML token stream extracting every
// <a href="..."> that points at a directory (ends with `/`).
// Skips parent (`../`), current (`./`), hidden (`.*`), and any
// non-directory hrefs. Public-but-not-exported so the layout
// internals stay testable from layout_test.go.
func parseAutoindexHTML(body []byte) ([]string, error) {
	tok := html.NewTokenizer(strings.NewReader(string(body)))
	var names []string
	for {
		switch tok.Next() {
		case html.ErrorToken:
			err := tok.Err()
			if errors.Is(err, html.ErrBufferExceeded) {
				return nil, fmt.Errorf("%w: autoindex page buffer exceeded", ErrIndexUnreadable)
			}
			// Any other error here is io.EOF in practice — return
			// what we've collected.
			return names, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tok.TagName()
			if string(name) != "a" || !hasAttr {
				continue
			}
			href := findHref(tok)
			if name := extractAutoindexEntry(href); name != "" {
				names = append(names, name)
			}
		}
	}
}

// findHref iterates the current tag's attributes looking for href.
func findHref(tok *html.Tokenizer) string {
	for {
		key, val, more := tok.TagAttr()
		if string(key) == "href" {
			return string(val)
		}
		if !more {
			return ""
		}
	}
}

// extractAutoindexEntry filters and normalises one href into a
// directory entry name. Returns "" for hrefs that should be
// skipped — parent links, dotfiles, query-only links, absolute
// URLs (which appear in some Caddy themes as full URLs back to
// the same path).
func extractAutoindexEntry(href string) string {
	// Strip query string + fragment.
	if i := strings.IndexAny(href, "?#"); i >= 0 {
		href = href[:i]
	}
	// Reject absolute URLs (Caddy themes sometimes emit them).
	// We're only interested in relative directory entries within
	// the current page.
	if strings.Contains(href, "://") {
		return ""
	}
	// Trim leading "./" if present.
	href = strings.TrimPrefix(href, "./")
	// Skip parent / current / empty.
	if href == "" || href == "/" || href == "../" || href == ".." {
		return ""
	}
	// Skip hidden dot-prefixed entries.
	if strings.HasPrefix(href, ".") {
		return ""
	}
	// Directory entries end with `/`. Strip and return.
	if strings.HasSuffix(href, "/") {
		return strings.TrimSuffix(href, "/")
	}
	// Non-directory hrefs (links to files): keep them — caller
	// decides via list semantics (ListVersions filters
	// metadata.json; ListModules returns them all since modules
	// are always dirs and the caller can defensively assume so).
	return href
}

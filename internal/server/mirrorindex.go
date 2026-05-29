package server

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// mirrorIndex maps an upstream-URL-without-scheme (host+path[+?query]) to
// the sha256-hex content address of the blob holding that archive's bytes.
//
// Bazel's HTTP archive mirror semantics (per HttpDownloader): when
// bazel_registry.json declares a mirror prefix, Bazel takes each source
// archive URL, strips the scheme, and appends the rest to each mirror
// prefix. So a mirror request URL looks like:
//
//     <mirror-prefix><host>[:port]/<path>[?<query>]
//
// The handler routes that suffix back through mirrorIndex to find the
// blob we stored under blobs/<sha256-hex>.
//
// The index is built by walking modules/*/*/source.json under the mirror
// root. Build is cheap (small JSON files, hundreds of entries at scale)
// and happens lazily on first lookup + refreshes on miss in case a fresh
// ingest just landed.
type mirrorIndex struct {
	root string

	mu      sync.RWMutex
	urlToHex map[string]string
	built    bool
}

func newMirrorIndex(root string) *mirrorIndex {
	return &mirrorIndex{root: root, urlToHex: map[string]string{}}
}

// Lookup returns the sha256-hex content address for an upstream URL
// keyed by host+path (scheme stripped). Lazily rebuilds on miss so a
// fresh ingest is picked up without restart.
func (m *mirrorIndex) Lookup(urlKey string) (string, bool) {
	urlKey = strings.TrimPrefix(urlKey, "/")
	m.mu.RLock()
	hex, ok := m.urlToHex[urlKey]
	built := m.built
	m.mu.RUnlock()
	if ok {
		return hex, true
	}
	if built {
		// Already built and missed. Try a single rebuild to catch a
		// fresh ingest, then re-look-up. Multiple goroutines hitting
		// this branch deduplicates via mu (build is serialized).
		m.rebuild()
		m.mu.RLock()
		hex, ok = m.urlToHex[urlKey]
		m.mu.RUnlock()
		return hex, ok
	}
	m.rebuild()
	m.mu.RLock()
	hex, ok = m.urlToHex[urlKey]
	m.mu.RUnlock()
	return hex, ok
}

// rebuild walks the mirror root and refreshes the URL→hex map.
// Source.json files with no/unusable URL or integrity are skipped.
func (m *mirrorIndex) rebuild() {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := map[string]string{}
	modulesDir := filepath.Join(m.root, "modules")
	_ = filepath.WalkDir(modulesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "source.json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var s struct {
			URL       string `json:"url"`
			Integrity string `json:"integrity"`
		}
		if json.Unmarshal(b, &s) != nil || s.URL == "" || s.Integrity == "" {
			return nil
		}
		hex, ok := integrityToHex(s.Integrity)
		if !ok {
			return nil
		}
		key, ok := urlKey(s.URL)
		if !ok {
			return nil
		}
		next[key] = hex
		return nil
	})
	m.urlToHex = next
	m.built = true
}

// integrityToHex parses "sha256-<base64>" and returns the lowercase hex
// content address. Returns (_, false) for any other algorithm or shape.
func integrityToHex(integrity string) (string, bool) {
	algo, b64, ok := strings.Cut(integrity, "-")
	if !ok || algo != "sha256" {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	return hex.EncodeToString(raw), true
}

// urlKey strips the scheme from an upstream URL and returns the
// host[:port]+path[+?query] form Bazel uses when constructing mirror
// requests. Returns ok=false if the input isn't a parseable absolute URL.
func urlKey(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	key := u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key, true
}

// urlKeyFromMirrorRequest reverses the routing: given the captured "rest"
// of a /m/<rest> mirror request, returns the lookup key. Currently this
// is the identity transform — the path arrives in the same shape Bazel
// constructed it from the upstream URL. Kept as a named function for
// clarity and as a place to live if the rewriting rule evolves.
func urlKeyFromMirrorRequest(rest, rawQuery string) string {
	rest = strings.TrimPrefix(rest, "/")
	if rawQuery != "" {
		return fmt.Sprintf("%s?%s", rest, rawQuery)
	}
	return rest
}

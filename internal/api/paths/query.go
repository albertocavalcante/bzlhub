package paths

import (
	"net/http"
	"strconv"
	"strings"
)

// Query-parameter parsing helpers for handlers. Mirrors the TS-side
// conventions in ui/src/lib/url-state/codecs.ts so a URL shared by a
// UI user is curl-equivalent on the API.
//
// Convention table (kept aligned with docs/plans/14-permalinks.md):
//
//   String       ?q=cc_library                  paths.QueryString
//   String list  ?class=github-archive,vendor   paths.QueryList
//   Tristate     ?tainted=only | exclude | --   paths.QueryTristate
//   Boolean      ?recursive=true (or absent)    paths.QueryBool
//   Integer      ?page=2                        paths.QueryInt
//
// All helpers tolerate junk values gracefully — bad input is treated
// as "absent" rather than 4xx'd. Permalinks are persistent; we never
// reject a URL just because a filter looks wrong.

// QueryString returns the param value, trimmed; empty if absent.
func QueryString(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}

// QueryList parses a comma-separated list. Whitespace around commas
// is trimmed; empty entries dropped; missing key → empty slice.
func QueryList(r *http.Request, key string) []string {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Tristate captures the three-state semantics of a ?key=only|exclude
// filter: unset / positive-filter / negative-filter.
type Tristate int

const (
	TristateUnset Tristate = iota
	// TristateOnly means "include only matching rows."
	TristateOnly
	// TristateExclude means "include only non-matching rows."
	TristateExclude
)

// QueryTristate parses a tristate filter. Invalid values are tolerated
// as TristateUnset.
func QueryTristate(r *http.Request, key string) Tristate {
	switch r.URL.Query().Get(key) {
	case "only":
		return TristateOnly
	case "exclude":
		return TristateExclude
	default:
		return TristateUnset
	}
}

// QueryBool returns true iff ?key=true or ?key (bare) is present.
// Any other value (including missing) → false.
func QueryBool(r *http.Request, key string) bool {
	v, ok := r.URL.Query()[key]
	if !ok {
		return false
	}
	if len(v) == 0 {
		return true // bare ?key
	}
	return v[0] == "true" || v[0] == ""
}

// QueryInt parses an integer param; bad input falls back to the
// supplied default.
func QueryInt(r *http.Request, key string, defaultValue int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return n
}

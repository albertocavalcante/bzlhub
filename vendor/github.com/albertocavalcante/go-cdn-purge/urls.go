package cdnpurge

import (
	"fmt"
	"net/url"
	"strings"
)

// validateURLs pre-validates a URL list before any HTTP egress.
// Returns ErrInvalidOptions for any empty / whitespace-only /
// malformed / non-http(s) URL. Whole-batch reject — providers don't
// silently send some-valid-some-invalid (the upstream would burn
// quota on the invalid ones).
//
// Shared across providers — Cloudflare, Fastly, and future CloudFront /
// Bunny adapters all use the same input contract. Lives in this file
// rather than each provider's source so the validation rules are
// authoritative + one-place-to-edit.
func validateURLs(urls []string) error {
	for i, u := range urls {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("%w: URL at index %d is empty/whitespace", ErrInvalidOptions, i)
		}
		parsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("%w: URL at index %d %q is malformed: %v", ErrInvalidOptions, i, u, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("%w: URL at index %d %q must be http/https; got scheme %q",
				ErrInvalidOptions, i, u, parsed.Scheme)
		}
		if parsed.Host == "" {
			return fmt.Errorf("%w: URL at index %d %q has empty host", ErrInvalidOptions, i, u)
		}
	}
	return nil
}

// dedupeURLs returns urls with duplicates removed, preserving
// first-occurrence order. Case-sensitive byte equality per
// re-review Δ8 (URLs are case-sensitive per RFC 3986; operators
// wanting hostname normalization do it before passing to Purge).
//
// Shared across providers; centralising here means a future
// CloudFront / Bunny adapter inherits the same dedup semantics
// without copy-paste drift.
func dedupeURLs(urls []string) []string {
	if len(urls) <= 1 {
		return urls
	}
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

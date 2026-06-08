package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CascadeHit carries the upstream's source.json contents for a
// (module, version) that already exists in the upstream BCR. The
// admit pipeline reads it when the original request has no
// source_url of its own — cascade-auto-passed requests don't need
// the user to know the URL.
type CascadeHit struct {
	// URL is the archive download address.
	URL string `json:"url"`
	// Integrity is the SRI-formatted hash ("sha256-..."). Used by
	// Bazel for verification + by admit as the BlobRef integrity
	// when no other source is available.
	Integrity string `json:"integrity"`
	// StripPrefix matches the BCR source.json field. May be empty.
	StripPrefix string `json:"strip_prefix,omitempty"`
}

// CascadeProbe asks an upstream BCR registry whether it already
// publishes a given (module, version) AND returns the upstream's
// source location when it does. Hits enable two things:
//   1. Preflight auto_pass — skip human review for known modules.
//   2. Admit fetch — when the request had no source_url, fall
//      back to the cascade-supplied URL so admit can still proceed.
//
// Implementations should return nil + nil err for "not found"
// (the canonical 404 signal in BCR-shape registries), and (nil,
// err) for any other failure so the checker degrades safely
// (Plan 73 Q4 — never fail-open).
type CascadeProbe interface {
	Has(ctx context.Context, module, version string) (*CascadeHit, error)
}

// BCRProbe is a CascadeProbe that fetches
// <baseURL>/modules/<m>/<v>/source.json. The presence + parseability
// of source.json is the canonical "this version is published"
// signal in BCR-shape registries.
type BCRProbe struct {
	baseURL string
	client  *http.Client
}

// NewBCRProbe constructs a probe rooted at baseURL (e.g.
// "https://bcr.bazel.build"). client may be nil — http.DefaultClient
// is used in that case.
func NewBCRProbe(baseURL string, client *http.Client) *BCRProbe {
	if client == nil {
		client = http.DefaultClient
	}
	return &BCRProbe{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
	}
}

// Has implements CascadeProbe. Returns the parsed source.json as
// a CascadeHit on 2xx, nil on 404, error on any other status or
// network/parse failure.
func (p *BCRProbe) Has(ctx context.Context, module, version string) (*CascadeHit, error) {
	url := fmt.Sprintf("%s/modules/%s/%s/source.json", p.baseURL, module, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("cascade GET %s: HTTP %d", url, resp.StatusCode)
	}
	// Bounded read — a malicious or buggy upstream shouldn't be
	// able to ship gigabytes of "source.json." 64 KiB is far more
	// than any real BCR source.json.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("cascade GET %s: read body: %w", url, err)
	}
	var hit CascadeHit
	if err := json.Unmarshal(body, &hit); err != nil {
		return nil, fmt.Errorf("cascade GET %s: parse source.json: %w", url, err)
	}
	if hit.URL == "" || hit.Integrity == "" {
		return nil, fmt.Errorf("cascade GET %s: source.json missing url or integrity", url)
	}
	return &hit, nil
}

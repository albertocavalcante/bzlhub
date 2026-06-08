// Package admit drives the procurement state-machine's
// "approved → fetching → indexed" leg: fetches the source archive
// from the request's source_url, materializes the BCR-shape entry
// via the publish package, and commits it back to the registry git
// repo.
package admit

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Fetcher streams a URL's body to a sink, bounded by a size cap.
// The publish package's BlobSink satisfies io.Writer; the fetcher
// is agnostic.
type Fetcher interface {
	Fetch(ctx context.Context, url string, sink io.Writer) (size int64, err error)
}

// HTTPFetcher is the production Fetcher. It uses a caller-supplied
// *http.Client (typically fetch.NewClient().HTTP so the allowlist
// + egress policy + 5-minute timeout apply uniformly) and enforces
// a per-request size cap.
type HTTPFetcher struct {
	client   *http.Client
	maxBytes int64
}

// NewHTTPFetcher constructs an HTTPFetcher. maxBytes ≤ 0 disables
// the size cap.
func NewHTTPFetcher(client *http.Client, maxBytes int64) *HTTPFetcher {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPFetcher{client: client, maxBytes: maxBytes}
}

// Fetch GETs url and streams the response body to sink. Returns the
// number of bytes written and the first error encountered (if any).
//
// Errors:
//   - non-2xx HTTP status → "HTTP <code> ..."
//   - response body larger than maxBytes → "too large (>N bytes)"
//   - network / I/O / context-cancel → wrapped as-is
func (f *HTTPFetcher) Fetch(ctx context.Context, url string, sink io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		// Network-level error (DNS, connection refused, TLS, timeout,
		// peer reset). All transient from canopy's perspective —
		// retry-with-backoff may succeed once the blip clears.
		return 0, fmt.Errorf("%w: http get %s: %w", ErrTransient, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isTransientStatus(resp.StatusCode) {
			return 0, fmt.Errorf("%w: HTTP %d from %s", ErrTransient, resp.StatusCode, url)
		}
		return 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	src := resp.Body.(io.Reader)
	if f.maxBytes > 0 {
		// +1 so io.Copy sees one extra byte when the body is exactly
		// at-cap, which lets us distinguish "fits" from "exceeds."
		src = io.LimitReader(src, f.maxBytes+1)
	}
	n, err := io.Copy(sink, src)
	if err != nil {
		// Body read interrupted (connection dropped mid-stream) —
		// transient; the upstream may serve cleanly on retry.
		return n, fmt.Errorf("%w: read body: %w", ErrTransient, err)
	}
	if f.maxBytes > 0 && n > f.maxBytes {
		return n, fmt.Errorf("response too large (>%d bytes) from %s", f.maxBytes, url)
	}
	return n, nil
}

// isTransientStatus reports whether an HTTP status code is worth
// retrying. 408 (request timeout), 425 (too early), 429 (rate
// limit), and every 5xx are transient. 4xx other than those above
// represent caller/upstream-level mistakes that won't fix themselves.
func isTransientStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout,    // 408
		http.StatusTooEarly,           // 425
		http.StatusTooManyRequests:    // 429
		return true
	}
	return code >= 500 && code <= 599
}

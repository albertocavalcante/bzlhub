// Package featureflags is canopy's 12-factor configuration surface.
//
// Every operational knob that influences runtime behavior is read from
// environment variables here, in one place. There is no global state:
// the parsed Flags struct is constructed once in main() and threaded
// down to whatever needs it. Tests construct Flags literals directly
// instead of mutating env vars.
//
// The package draws a sharp line between *public* fields (those that
// the UI is allowed to see — see PublicSnapshot) and operational fields
// like the rate-limit bypass list, which never leave the server.
package featureflags

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Flags is the parsed feature-flag state. Values are immutable after
// Parse — pass the struct by value if a goroutine wants its own copy.
type Flags struct {
	// IngestWriteEnabled gates POST /api/ingest-recursive. When false
	// (the default), the endpoint returns 503 regardless of any other
	// gate. This is the master kill-switch — flip it off without a
	// deploy by editing compose env + `docker compose up -d`.
	//
	// SECURITY-TODO: Cloudflare Access is not yet in front of canopy.
	// Until it is, the safety story for ingest-write is:
	//   (1) this flag defaults to false
	//   (2) the production canonical URL is assumed to be not
	//       well-known (security by minor obscurity, NOT a real
	//       defense — useful only as a brief interim)
	//   (3) per-IP rate limit + global concurrency semaphore
	// Once Access lands, the per-IP allowlist below should become a
	// per-user allowlist and the (2) assumption goes away.
	IngestWriteEnabled bool

	// RegistryURL is the default upstream BCR-shape registry used by
	// ingest when the request body does not (or is not allowed to)
	// supply one. Empty means "no default" and forces clients to
	// supply Upstream explicitly — useful in airgapped setups.
	RegistryURL string

	// IngestAllowCustomUpstream decides whether a client may set
	// body.upstream on POST /api/ingest-recursive. UI clients should
	// always go through the server-configured RegistryURL; this flag
	// exists so CLI / MCP operators can still point at a private
	// registry through the same HTTP endpoint when explicitly enabled.
	//
	// Default: false (SSRF guard — the server only fetches the
	// configured RegistryURL).
	IngestAllowCustomUpstream bool

	// IngestRateLimitPerMin caps how many ingest requests a single
	// remote address may submit per minute. 0 disables the limiter.
	IngestRateLimitPerMin int

	// IngestRateBypassIPs lists exact remote addresses (IP literals,
	// no CIDR) that bypass the per-IP rate limiter. The global
	// concurrency semaphore still applies — bypass means "you can
	// queue faster," not "you can DoS the server."
	IngestRateBypassIPs []string

	// IngestMaxConcurrent is the global concurrency cap on in-flight
	// ingest jobs. 0 means unlimited (not recommended). The semaphore
	// is intentionally not per-IP — its job is to bound *server*
	// resource usage, not to be fair across clients.
	IngestMaxConcurrent int

	// AttrsInterpret turns on the Tier-3 attrs extractor (assay/interp)
	// during ingest: rules whose attrs the AST-only path couldn't
	// extract are re-resolved by actually evaluating their .bzl file
	// in a sandboxed Bazel-Starlark interpreter. Higher cost per
	// ingest but eliminates most "dynamic schema" UI fallbacks. Opt-in
	// because the interpreter dep is heavyweight and operators with
	// constrained boxes may not want the extra wall-time per Bump.
	AttrsInterpret bool

	// DemoMode flags this instance as a public demo: the UI renders a
	// "demo instance" badge in the footer so users on a public-facing
	// instance know they're not on a private corporate deployment,
	// and that data may be reset / ingestion may be locked. Pure UI
	// hint — does not gate any endpoint.
	DemoMode bool

	// DemoBanner overrides the default "demo instance" badge text when
	// non-empty. Lets operators surface a custom note ("staging",
	// "read-only mirror", etc.) without code changes.
	DemoBanner string
}

// Parse reads the canopy feature-flag env vars and returns the Flags.
// Returns an error if any var is set to something we can't parse —
// silent fallbacks are how operators end up with mystery behavior.
func Parse() (Flags, error) {
	var f Flags
	var errs []error

	f.IngestWriteEnabled = envBool("CANOPY_INGEST_WRITE_ENABLED", false, &errs)
	f.RegistryURL = strings.TrimSpace(os.Getenv("CANOPY_REGISTRY_URL"))
	if f.RegistryURL == "" {
		f.RegistryURL = "https://bcr.bazel.build"
	}
	f.IngestAllowCustomUpstream = envBool("CANOPY_INGEST_ALLOW_CUSTOM_UPSTREAM", false, &errs)
	f.IngestRateLimitPerMin = envInt("CANOPY_INGEST_RATE_LIMIT_PER_MIN", 5, &errs)
	f.IngestMaxConcurrent = envInt("CANOPY_INGEST_MAX_CONCURRENT", 1, &errs)
	f.IngestRateBypassIPs = envCSV("CANOPY_INGEST_RATE_BYPASS_IPS")
	f.AttrsInterpret = envBool("CANOPY_ATTRS_INTERPRET", false, &errs)
	f.DemoMode = envBool("CANOPY_DEMO_MODE", false, &errs)
	f.DemoBanner = strings.TrimSpace(os.Getenv("CANOPY_DEMO_BANNER"))

	if f.IngestRateLimitPerMin < 0 {
		errs = append(errs, fmt.Errorf("CANOPY_INGEST_RATE_LIMIT_PER_MIN must be >= 0, got %d", f.IngestRateLimitPerMin))
	}
	if f.IngestMaxConcurrent < 0 {
		errs = append(errs, fmt.Errorf("CANOPY_INGEST_MAX_CONCURRENT must be >= 0, got %d", f.IngestMaxConcurrent))
	}

	if len(errs) > 0 {
		return Flags{}, errors.Join(errs...)
	}
	return f, nil
}

// IsRateBypassIP reports whether remoteAddr is on the bypass list.
// Exact-match only; CIDRs are intentionally not supported (operators
// who need them can set a very large RateLimitPerMin instead — keeps
// the bypass list small + readable + auditable).
func (f Flags) IsRateBypassIP(remoteAddr string) bool {
	return slices.Contains(f.IngestRateBypassIPs, remoteAddr)
}

// PublicSnapshot is the subset of Flags that the UI may see via
// GET /api/features. It deliberately omits the bypass list, registry
// URL, and concurrency cap — those are server-side operational
// concerns the UI has no need to render.
type PublicSnapshot struct {
	IngestWriteEnabled bool   `json:"ingest_write_enabled"`
	DemoMode           bool   `json:"demo_mode"`
	DemoBanner         string `json:"demo_banner,omitempty"`
}

// Public returns the UI-safe view of Flags.
func (f Flags) Public() PublicSnapshot {
	return PublicSnapshot{
		IngestWriteEnabled: f.IngestWriteEnabled,
		DemoMode:           f.DemoMode,
		DemoBanner:         f.DemoBanner,
	}
}

func envBool(name string, def bool, errs *[]error) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q: %w", name, raw, err))
		return def
	}
	return v
}

func envInt(name string, def int, errs *[]error) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q: %w", name, raw, err))
		return def
	}
	return v
}

func envCSV(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
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

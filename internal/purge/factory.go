package purge

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	cdnpurge "github.com/albertocavalcante/go-cdn-purge"
)

// Config configures FromEnv. Operators set BZLHUB_CDN_VENDOR to
// pick a backend; the vendor-specific fields are required only for
// that vendor.
//
// Recognized vendors (case-insensitive):
//   - "" or "noop"      — NoOp{} (no CDN). Default; safe for local
//                         dev, on-prem, demos with no CDN in front.
//   - "cloudflare"      — cdnpurge.NewCloudflare(...). Requires
//                         APIToken + ZoneID.
//   - "fastly"          — cdnpurge.NewFastly(...). Requires
//                         APIToken + ServiceID.
type Config struct {
	Vendor string

	// Cloudflare fields (consulted only when Vendor=cloudflare).
	CloudflareAPIToken string
	CloudflareZoneID   string

	// Fastly fields (consulted only when Vendor=fastly).
	FastlyAPIToken  string
	FastlyServiceID string

	// HTTPClient is shared across vendors. nil → http.DefaultClient.
	// Production callers should pass canopy's egress-policy-wrapped
	// client (fetch.NewClient().HTTP) so CDN calls flow through the
	// same audit + allowlist + timeout posture as other egress.
	HTTPClient *http.Client

	// Log is required (slog.Default() is fine when no scoped logger
	// is available).
	Log *slog.Logger
}

// Build constructs a Provider from cfg. The returned Provider is
// always non-nil — a vendor-misconfiguration error explains what
// was missing, and falls back to NoOp{} alongside that error so
// callers can decide whether to surface (corp deploy: fatal) or
// log-and-continue (demo deploy: still useful with NoOp).
//
// NoOp is returned with err=nil when Vendor is empty or "noop".
func Build(cfg Config) (Provider, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Vendor)) {
	case "", "noop":
		return NoOp{}, nil
	case "cloudflare":
		if cfg.CloudflareAPIToken == "" {
			return NoOp{}, fmt.Errorf("purge: cloudflare vendor needs CloudflareAPIToken")
		}
		if cfg.CloudflareZoneID == "" {
			return NoOp{}, fmt.Errorf("purge: cloudflare vendor needs CloudflareZoneID")
		}
		cf, err := cdnpurge.NewCloudflare(cdnpurge.CloudflareConfig{
			APIToken:   cfg.CloudflareAPIToken,
			ZoneID:     cfg.CloudflareZoneID,
			HTTPClient: client,
		})
		if err != nil {
			return NoOp{}, fmt.Errorf("purge: cloudflare init: %w", err)
		}
		return NewAdapter(cf, cfg.Log), nil
	case "fastly":
		if cfg.FastlyAPIToken == "" {
			return NoOp{}, fmt.Errorf("purge: fastly vendor needs FastlyAPIToken")
		}
		if cfg.FastlyServiceID == "" {
			return NoOp{}, fmt.Errorf("purge: fastly vendor needs FastlyServiceID")
		}
		f, err := cdnpurge.NewFastly(cdnpurge.FastlyConfig{
			APIToken:   cfg.FastlyAPIToken,
			ServiceID:  cfg.FastlyServiceID,
			HTTPClient: client,
		})
		if err != nil {
			return NoOp{}, fmt.Errorf("purge: fastly init: %w", err)
		}
		return NewAdapter(f, cfg.Log), nil
	default:
		return NoOp{}, fmt.Errorf("purge: unknown vendor %q (want one of: noop, cloudflare, fastly)", cfg.Vendor)
	}
}


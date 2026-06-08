package purge

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestBuild_NoOpDefault(t *testing.T) {
	p, err := Build(Config{Log: slog.Default()})
	if err != nil {
		t.Fatalf("err=%v, want nil", err)
	}
	if p.Name() != "noop" {
		t.Errorf("Name=%q, want noop", p.Name())
	}
}

func TestBuild_NoOpExplicit(t *testing.T) {
	for _, v := range []string{"noop", "NOOP", "  noop  "} {
		p, err := Build(Config{Vendor: v, Log: slog.Default()})
		if err != nil || p.Name() != "noop" {
			t.Errorf("vendor=%q: name=%q err=%v", v, p.Name(), err)
		}
	}
}

func TestBuild_CloudflareMissingToken(t *testing.T) {
	p, err := Build(Config{Vendor: "cloudflare", CloudflareZoneID: "z"})
	if err == nil || !strings.Contains(err.Error(), "CloudflareAPIToken") {
		t.Errorf("err=%v, want contains CloudflareAPIToken", err)
	}
	if p.Name() != "noop" {
		t.Errorf("fallback Name=%q, want noop", p.Name())
	}
}

func TestBuild_CloudflareMissingZone(t *testing.T) {
	_, err := Build(Config{Vendor: "cloudflare", CloudflareAPIToken: "t"})
	if err == nil || !strings.Contains(err.Error(), "CloudflareZoneID") {
		t.Errorf("err=%v, want contains CloudflareZoneID", err)
	}
}

func TestBuild_CloudflareHappyPath(t *testing.T) {
	p, err := Build(Config{
		Vendor:             "cloudflare",
		CloudflareAPIToken: "t",
		CloudflareZoneID:   "z",
		HTTPClient:         http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("err=%v, want nil", err)
	}
	if p.Name() != "cloudflare" {
		t.Errorf("Name=%q, want cloudflare", p.Name())
	}
}

func TestBuild_FastlyHappyPath(t *testing.T) {
	p, err := Build(Config{
		Vendor:          "fastly",
		FastlyAPIToken:  "t",
		FastlyServiceID: "s",
		HTTPClient:      http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("err=%v, want nil", err)
	}
	if p.Name() != "fastly" {
		t.Errorf("Name=%q, want fastly", p.Name())
	}
}

func TestBuild_UnknownVendor(t *testing.T) {
	_, err := Build(Config{Vendor: "akamai"})
	if err == nil || !strings.Contains(err.Error(), "unknown vendor") {
		t.Errorf("err=%v, want contains 'unknown vendor'", err)
	}
}

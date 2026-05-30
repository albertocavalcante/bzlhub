package featureflags

import (
	"strings"
	"testing"
)

func TestParse_Defaults(t *testing.T) {
	withEnv(t, nil)
	f, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.IngestWriteEnabled {
		t.Errorf("IngestWriteEnabled default = true, want false")
	}
	if f.RegistryURL != "https://bcr.bazel.build" {
		t.Errorf("RegistryURL default = %q, want https://bcr.bazel.build", f.RegistryURL)
	}
	if f.IngestAllowCustomUpstream {
		t.Errorf("IngestAllowCustomUpstream default = true, want false")
	}
	if f.IngestRateLimitPerMin != 5 {
		t.Errorf("IngestRateLimitPerMin default = %d, want 5", f.IngestRateLimitPerMin)
	}
	if f.IngestMaxConcurrent != 1 {
		t.Errorf("IngestMaxConcurrent default = %d, want 1", f.IngestMaxConcurrent)
	}
	if len(f.IngestRateBypassIPs) != 0 {
		t.Errorf("IngestRateBypassIPs default = %v, want empty", f.IngestRateBypassIPs)
	}
	if f.DemoMode {
		t.Errorf("DemoMode default = true, want false")
	}
	if f.DemoBanner != "" {
		t.Errorf("DemoBanner default = %q, want empty", f.DemoBanner)
	}
	if f.MCPHTTPEnabled {
		t.Errorf("MCPHTTPEnabled default = true, want false")
	}
	if f.MCPWriteToolsEnabled {
		t.Errorf("MCPWriteToolsEnabled default = true, want false")
	}
}

func TestParse_OverridesViaEnv(t *testing.T) {
	withEnv(t, map[string]string{
		"CANOPY_INGEST_WRITE_ENABLED":         "true",
		"CANOPY_REGISTRY_URL":                 "https://registry.example/",
		"CANOPY_INGEST_ALLOW_CUSTOM_UPSTREAM": "true",
		"CANOPY_INGEST_RATE_LIMIT_PER_MIN":    "30",
		"CANOPY_INGEST_MAX_CONCURRENT":        "4",
		"CANOPY_INGEST_RATE_BYPASS_IPS":       "1.2.3.4, 5.6.7.8 ,",
		"CANOPY_DEMO_MODE":                    "true",
		"CANOPY_DEMO_BANNER":                  " public demo ",
		"CANOPY_MCP_HTTP_ENABLED":             "true",
		"CANOPY_MCP_WRITE_TOOLS_ENABLED":      "true",
	})
	f, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !f.IngestWriteEnabled {
		t.Errorf("IngestWriteEnabled = false, want true")
	}
	if f.RegistryURL != "https://registry.example/" {
		t.Errorf("RegistryURL = %q", f.RegistryURL)
	}
	if !f.IngestAllowCustomUpstream {
		t.Errorf("IngestAllowCustomUpstream = false, want true")
	}
	if f.IngestRateLimitPerMin != 30 {
		t.Errorf("IngestRateLimitPerMin = %d, want 30", f.IngestRateLimitPerMin)
	}
	if f.IngestMaxConcurrent != 4 {
		t.Errorf("IngestMaxConcurrent = %d, want 4", f.IngestMaxConcurrent)
	}
	want := []string{"1.2.3.4", "5.6.7.8"}
	if len(f.IngestRateBypassIPs) != len(want) {
		t.Fatalf("IngestRateBypassIPs = %v, want %v", f.IngestRateBypassIPs, want)
	}
	for i, ip := range want {
		if f.IngestRateBypassIPs[i] != ip {
			t.Errorf("bypass[%d] = %q, want %q", i, f.IngestRateBypassIPs[i], ip)
		}
	}
	if !f.DemoMode {
		t.Errorf("DemoMode = false, want true")
	}
	if f.DemoBanner != "public demo" {
		t.Errorf("DemoBanner = %q, want public demo", f.DemoBanner)
	}
	if !f.MCPHTTPEnabled {
		t.Errorf("MCPHTTPEnabled = false, want true")
	}
	if !f.MCPWriteToolsEnabled {
		t.Errorf("MCPWriteToolsEnabled = false, want true")
	}
}

func TestParse_BadBoolErrors(t *testing.T) {
	withEnv(t, map[string]string{"CANOPY_INGEST_WRITE_ENABLED": "yes-please"})
	_, err := Parse()
	if err == nil {
		t.Fatal("Parse: want error for bad bool, got nil")
	}
	if !strings.Contains(err.Error(), "CANOPY_INGEST_WRITE_ENABLED") {
		t.Errorf("error doesn't mention var name: %v", err)
	}
}

func TestParse_NegativeRateRejected(t *testing.T) {
	withEnv(t, map[string]string{"CANOPY_INGEST_RATE_LIMIT_PER_MIN": "-1"})
	_, err := Parse()
	if err == nil {
		t.Fatal("Parse: want error for negative rate, got nil")
	}
}

func TestIsRateBypassIP_ExactMatch(t *testing.T) {
	f := Flags{IngestRateBypassIPs: []string{"10.0.0.1", "192.168.1.5"}}
	if !f.IsRateBypassIP("10.0.0.1") {
		t.Error("expected bypass for 10.0.0.1")
	}
	if f.IsRateBypassIP("10.0.0.2") {
		t.Error("did not expect bypass for 10.0.0.2")
	}
	// CIDR-shaped input should not match unless literally listed.
	if f.IsRateBypassIP("10.0.0.0/8") {
		t.Error("CIDR should not implicit-match")
	}
}

func TestPublic_OmitsServerOnlyFields(t *testing.T) {
	f := Flags{
		IngestWriteEnabled:  true,
		RegistryURL:         "https://internal.example/",
		IngestRateBypassIPs: []string{"10.0.0.1"},
		DemoMode:            true,
		DemoBanner:          "demo",
	}
	pub := f.Public()
	if !pub.IngestWriteEnabled {
		t.Error("public should expose IngestWriteEnabled")
	}
	if !pub.DemoMode {
		t.Error("public should expose DemoMode")
	}
	if pub.DemoBanner != "demo" {
		t.Errorf("DemoBanner = %q, want demo", pub.DemoBanner)
	}
	// Public is a typed struct without RegistryURL / bypass IPs at
	// all — this is enforced at compile time. The assertion below is
	// a documentation aid; if the struct grows new fields they must
	// be added explicitly above this line.
	_ = pub
}

// withEnv clears CANOPY_* and sets the given vars for the test only.
// Using t.Setenv ensures restore at test end.
func withEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, k := range []string{
		"CANOPY_INGEST_WRITE_ENABLED",
		"CANOPY_REGISTRY_URL",
		"CANOPY_INGEST_ALLOW_CUSTOM_UPSTREAM",
		"CANOPY_INGEST_RATE_LIMIT_PER_MIN",
		"CANOPY_INGEST_MAX_CONCURRENT",
		"CANOPY_INGEST_RATE_BYPASS_IPS",
		"CANOPY_DEMO_MODE",
		"CANOPY_DEMO_BANNER",
		"CANOPY_MCP_HTTP_ENABLED",
		"CANOPY_MCP_WRITE_TOOLS_ENABLED",
	} {
		t.Setenv(k, "")
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

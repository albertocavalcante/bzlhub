package config

import (
	"strings"
	"testing"
)

func TestLoadEnvironment_Default(t *testing.T) {
	t.Setenv(EnvProfile, "")
	t.Setenv(EnvAllowedHosts, "")
	cfg, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != ProfileDefault || cfg.Egress.Mode != "allow" {
		t.Fatalf("config = profile %v mode %q, want default/allow", cfg.Profile, cfg.Egress.Mode)
	}
}

func TestLoadEnvironment_MirrorOnly(t *testing.T) {
	t.Setenv(EnvProfile, "mirror-only")
	t.Setenv(EnvAllowedHosts, "")
	cfg, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Egress.Mode != "deny" {
		t.Fatalf("mode = %q, want deny", cfg.Egress.Mode)
	}
}

func TestLoadEnvironment_SyncRunnerRequiresAllowlist(t *testing.T) {
	t.Setenv(EnvProfile, "sync-runner")
	t.Setenv(EnvAllowedHosts, "")
	if _, err := LoadEnvironment(); err == nil || !strings.Contains(err.Error(), "requires at least one") {
		t.Fatalf("error = %v, want allowlist diagnostic", err)
	}
}

func TestLoadEnvironment_SyncRunner(t *testing.T) {
	t.Setenv(EnvProfile, "sync-runner")
	t.Setenv(EnvAllowedHosts, "bcr.bazel.build, api.github.com")
	cfg, err := LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Egress.Mode != "audit" {
		t.Fatalf("mode = %q, want audit", cfg.Egress.Mode)
	}
	if len(cfg.Egress.Allow) != 2 {
		t.Fatalf("allow = %v, want two hosts", cfg.Egress.Allow)
	}
}

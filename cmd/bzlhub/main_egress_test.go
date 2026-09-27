package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/config"
	"github.com/albertocavalcante/bzlhub/internal/egress"
)

func TestConfigureProcessEgress_DefaultAllowlist(t *testing.T) {
	t.Setenv(config.EnvProfile, "default")
	t.Setenv(config.EnvAllowedHosts, "registry.example.test")
	t.Setenv(envEgressAuditFile, "")
	t.Cleanup(func() {
		egress.ConfigureDefault(egress.Policy{Mode: egress.ModeAllow}, egress.NopSink{})
	})

	closeEgress, err := configureProcessEgress()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeEgress() })

	p := egress.PolicyFromContext(context.Background())
	if p.Mode != egress.ModeAllow || len(p.Allow) != 1 || p.Allow[0] != "registry.example.test" {
		t.Fatalf("default policy = %+v", p)
	}
}

func TestConfigureProcessEgress_RestrictedProfilesRequireAuditFile(t *testing.T) {
	for _, profile := range []string{"mirror-only", "sync-runner"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv(config.EnvProfile, profile)
			t.Setenv(config.EnvAllowedHosts, "")
			if profile == "sync-runner" {
				t.Setenv(config.EnvAllowedHosts, "registry.example.test")
			}
			t.Setenv(envEgressAuditFile, "")
			if _, err := configureProcessEgress(); err == nil {
				t.Fatal("configureProcessEgress() error = nil, want missing audit-file error")
			}
		})
	}
}

func TestConfigureProcessEgress_CreatesPrivateAuditFile(t *testing.T) {
	t.Setenv(config.EnvProfile, "mirror-only")
	t.Setenv(config.EnvAllowedHosts, "")
	path := filepath.Join(t.TempDir(), "audit", "egress.jsonl")
	t.Setenv(envEgressAuditFile, path)
	t.Cleanup(func() {
		egress.ConfigureDefault(egress.Policy{Mode: egress.ModeAllow}, egress.NopSink{})
	})

	closeEgress, err := configureProcessEgress()
	if err != nil {
		t.Fatal(err)
	}
	if err := closeEgress(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("audit file mode = %o, want 600", got)
	}
	if p := egress.PolicyFromContext(context.Background()); p.Mode != egress.ModeDeny {
		t.Fatalf("policy mode = %v, want deny", p.Mode)
	}
}

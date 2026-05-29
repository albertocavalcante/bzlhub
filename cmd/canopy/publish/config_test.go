package publish

import (
	"strings"
	"testing"
)

// clearPublishEnv neutralizes every env var resolvePublishConfig reads.
// Pair with t.Setenv per-case for the specific vars under test.
func clearPublishEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CANOPY_REGISTRY_WORKTREE", "CANOPY_FORGE_KIND", "CANOPY_FORGE_BASE_URL",
		"CANOPY_REGISTRY_BASE_BRANCH", "CANOPY_FORGE_REPO", "CANOPY_FORGE_TOKEN_ENV",
		"CANOPY_GITHUB_TOKEN", "CANOPY_GITLAB_TOKEN", "CANOPY_BITBUCKET_TOKEN",
		"CANOPY_FORGEJO_TOKEN", "CANOPY_BOT_NAME", "CANOPY_BOT_EMAIL",
	} {
		t.Setenv(k, "")
	}
}

// basePublishFlags returns the minimum publishFlags for a happy-path
// PR-mode resolve. requesterName/Email are set so the function never
// shells out to `git config`.
func basePublishFlags(worktree string) publishFlags {
	return publishFlags{
		worktree:       worktree,
		forge:          "github",
		repo:           "o/r",
		tokenEnv:       "FAKE_TOKEN",
		requesterName:  "Tester",
		requesterEmail: "t@example.test",
	}
}

func TestResolvePublishConfig_HappyPath(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("FAKE_TOKEN", "ghp_abc")

	cfg, err := resolvePublishConfig(basePublishFlags("/tmp/w"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.worktree != "/tmp/w" || cfg.forge != "github" ||
		cfg.repo.Owner != "o" || cfg.repo.Name != "r" ||
		cfg.token != "ghp_abc" || cfg.baseBranch != "main" {
		t.Errorf("unexpected cfg: %+v", cfg)
	}
}

func TestResolvePublishConfig_FlagBeatsEnv(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("CANOPY_REGISTRY_WORKTREE", "/from/env")
	t.Setenv("CANOPY_FORGE_REPO", "envo/envr")
	t.Setenv("FAKE_TOKEN", "ghp_abc")

	f := basePublishFlags("/from/flag")
	f.repo = "flago/flagr"
	cfg, err := resolvePublishConfig(f)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.worktree != "/from/flag" {
		t.Errorf("flag must beat env on worktree: %q", cfg.worktree)
	}
	if cfg.repo.Owner != "flago" {
		t.Errorf("flag must beat env on repo: %+v", cfg.repo)
	}
}

func TestResolvePublishConfig_EnvFallback(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("CANOPY_REGISTRY_WORKTREE", "/from/env")
	t.Setenv("CANOPY_FORGE_REPO", "envo/envr")
	t.Setenv("FAKE_TOKEN", "ghp_abc")

	f := basePublishFlags("")
	f.repo = ""
	cfg, err := resolvePublishConfig(f)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.worktree != "/from/env" || cfg.repo.Owner != "envo" || cfg.repo.Name != "envr" {
		t.Errorf("env fallback failed: %+v", cfg)
	}
}

func TestResolvePublishConfig_CommitRequiresAllowDirect(t *testing.T) {
	clearPublishEnv(t)
	f := basePublishFlags("/tmp/w")
	f.commit = true
	f.allowDirect = false
	_, err := resolvePublishConfig(f)
	if err == nil || !strings.Contains(err.Error(), "--allow-direct") {
		t.Fatalf("want --allow-direct gate error, got %v", err)
	}
}

func TestResolvePublishConfig_CommitModeSkipsForgeConfig(t *testing.T) {
	clearPublishEnv(t)
	// Commit mode doesn't need --repo / token / forge.
	cfg, err := resolvePublishConfig(publishFlags{
		worktree:       "/tmp/w",
		commit:         true,
		allowDirect:    true,
		requesterName:  "Tester",
		requesterEmail: "t@example.test",
	})
	if err != nil {
		t.Fatalf("commit mode should not require forge config: %v", err)
	}
	if !cfg.commitMode {
		t.Error("commitMode not propagated")
	}
}

func TestResolvePublishConfig_Errors(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(f *publishFlags)
		wantSubs string
	}{
		{"missing worktree", func(f *publishFlags) { f.worktree = "" }, "--worktree is required"},
		{"missing repo", func(f *publishFlags) { f.repo = "" }, "--repo is required"},
		{"bad repo format", func(f *publishFlags) { f.repo = "no-slash" }, "must be <owner>/<name>"},
		{"unknown forge", func(f *publishFlags) { f.forge = "gerrit" }, `not supported`},
		{"bitbucketdc needs base-url", func(f *publishFlags) { f.forge = "bitbucketdc"; f.baseURL = "" }, "--base-url is required"},
		{"token env empty", func(f *publishFlags) { f.tokenEnv = "DEFINITELY_NOT_SET_XYZ" }, "not set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearPublishEnv(t)
			t.Setenv("FAKE_TOKEN", "ghp_abc")
			f := basePublishFlags("/tmp/w")
			tc.mutate(&f)
			_, err := resolvePublishConfig(f)
			if err == nil || !strings.Contains(err.Error(), tc.wantSubs) {
				t.Fatalf("want error containing %q, got %v", tc.wantSubs, err)
			}
		})
	}
}

func TestResolvePublishConfig_BitbucketDCTokenDefault(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("CANOPY_BITBUCKET_TOKEN", "bbdc-secret")
	f := basePublishFlags("/tmp/w")
	f.forge = "bitbucketdc"
	f.baseURL = "https://bb.example.test"
	f.tokenEnv = "" // exercise the per-forge default selection
	cfg, err := resolvePublishConfig(f)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.tokenEnv != "CANOPY_BITBUCKET_TOKEN" || cfg.token != "bbdc-secret" {
		t.Errorf("bbdc default token env not picked: env=%q token=%q", cfg.tokenEnv, cfg.token)
	}
}

func TestResolvePublishConfig_GitLabTokenDefault(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("CANOPY_GITLAB_TOKEN", "glpat-secret")
	f := basePublishFlags("/tmp/w")
	f.forge = "gitlab"
	f.tokenEnv = "" // exercise the per-forge default selection
	cfg, err := resolvePublishConfig(f)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.tokenEnv != "CANOPY_GITLAB_TOKEN" || cfg.token != "glpat-secret" {
		t.Errorf("gitlab default token env not picked: env=%q token=%q", cfg.tokenEnv, cfg.token)
	}
}

func TestResolvePublishConfig_ForgejoTokenDefaultAndBaseURLRequired(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("CANOPY_FORGEJO_TOKEN", "fjo-secret")
	f := basePublishFlags("/tmp/w")
	f.forge = "forgejo"
	f.tokenEnv = "" // exercise the per-forge default selection

	// First: without base-url, must reject (forgejo has no canonical instance).
	_, err := resolvePublishConfig(f)
	if err == nil || !strings.Contains(err.Error(), "--base-url is required when --forge=forgejo") {
		t.Fatalf("forgejo must require --base-url, got %v", err)
	}

	// With base-url set, the default token env is CANOPY_FORGEJO_TOKEN.
	f.baseURL = "https://codeberg.org"
	cfg, err := resolvePublishConfig(f)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.tokenEnv != "CANOPY_FORGEJO_TOKEN" || cfg.token != "fjo-secret" {
		t.Errorf("forgejo default token env not picked: env=%q token=%q", cfg.tokenEnv, cfg.token)
	}
}

package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearWatchEnv neutralizes every env var resolveWatchConfig reads,
// plus the shared publish/watch env vars (forge kind, base URL, etc.)
// so a happy-path resolve uses flag values verbatim.
func clearWatchEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CANOPY_REGISTRY_WORKTREE", "CANOPY_FORGE_KIND", "CANOPY_FORGE_BASE_URL",
		"CANOPY_REGISTRY_BASE_BRANCH", "CANOPY_FORGE_REPO", "CANOPY_FORGE_TOKEN_ENV",
		"CANOPY_GITHUB_TOKEN", "CANOPY_GITLAB_TOKEN", "CANOPY_BITBUCKET_TOKEN",
		"CANOPY_FORGEJO_TOKEN", "CANOPY_DB_PATH", "CANOPY_WATCH_STATE_FILE",
		"CANOPY_WATCH_INTERVAL",
	} {
		t.Setenv(k, "")
	}
}

// gitWorktree returns a temp dir with a .git subdirectory — enough for
// resolveWatchConfig's "is this a git checkout?" stat-check to pass.
func gitWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return dir
}

func baseWatchFlags(worktree, stateFile string) watchFlags {
	return watchFlags{
		worktree:    worktree,
		forge:       "github",
		repo:        "o/r",
		tokenEnv:    "FAKE_TOKEN",
		stateFile:   stateFile,
		interval:    30 * time.Second,
		maxInterval: 2 * time.Minute,
	}
}

func TestResolveWatchConfig_HappyPath(t *testing.T) {
	clearWatchEnv(t)
	t.Setenv("FAKE_TOKEN", "ghp_abc")
	wt := gitWorktree(t)
	sf := filepath.Join(t.TempDir(), "state.json")

	cfg, err := resolveWatchConfig(baseWatchFlags(wt, sf))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.repo.Owner != "o" || cfg.repo.Name != "r" || cfg.token != "ghp_abc" ||
		cfg.remote != "origin" || cfg.interval != 30*time.Second {
		t.Errorf("unexpected cfg: %+v", cfg)
	}
}

func TestResolveWatchConfig_Errors(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(f *watchFlags, wt string) string
		wantSubs string
	}{
		{
			"missing worktree",
			func(f *watchFlags, _ string) string { f.worktree = ""; return "" },
			"--worktree is required",
		},
		{
			"worktree not a git dir",
			func(f *watchFlags, _ string) string {
				d := t.TempDir() // no .git inside
				f.worktree = d
				return d
			},
			"not a git working tree",
		},
		{
			"missing repo",
			func(f *watchFlags, _ string) string { f.repo = ""; return "" },
			"--repo is required",
		},
		{
			"bad repo format",
			func(f *watchFlags, _ string) string { f.repo = "no-slash"; return "" },
			"must be <owner>/<name>",
		},
		{
			"unknown forge",
			func(f *watchFlags, _ string) string { f.forge = "gerrit"; return "" },
			"not supported",
		},
		{
			"bitbucketdc needs base-url",
			func(f *watchFlags, _ string) string { f.forge = "bitbucketdc"; f.baseURL = ""; return "" },
			"--base-url is required",
		},
		{
			"token env empty",
			func(f *watchFlags, _ string) string { f.tokenEnv = "DEFINITELY_NOT_SET_XYZ"; return "" },
			"not set",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearWatchEnv(t)
			t.Setenv("FAKE_TOKEN", "ghp_abc")
			wt := gitWorktree(t)
			sf := filepath.Join(t.TempDir(), "state.json")
			f := baseWatchFlags(wt, sf)
			tc.mutate(&f, wt)
			_, err := resolveWatchConfig(f)
			if err == nil || !strings.Contains(err.Error(), tc.wantSubs) {
				t.Fatalf("want error containing %q, got %v", tc.wantSubs, err)
			}
		})
	}
}

func TestResolveWatchConfig_IntervalDefaults(t *testing.T) {
	clearWatchEnv(t)
	t.Setenv("FAKE_TOKEN", "ghp_abc")
	wt := gitWorktree(t)
	sf := filepath.Join(t.TempDir(), "state.json")
	f := baseWatchFlags(wt, sf)
	f.interval = 0    // exercise default
	f.maxInterval = 0 // exercise 5x interval default

	cfg, err := resolveWatchConfig(f)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.interval != 60*time.Second {
		t.Errorf("interval default: got %v, want 60s", cfg.interval)
	}
	if cfg.maxInterval != 5*cfg.interval {
		t.Errorf("maxInterval default: got %v, want 5*interval", cfg.maxInterval)
	}
}

func TestResolveWatchConfig_IntervalFromEnv(t *testing.T) {
	clearWatchEnv(t)
	t.Setenv("FAKE_TOKEN", "ghp_abc")
	t.Setenv("CANOPY_WATCH_INTERVAL", "45s")
	wt := gitWorktree(t)
	sf := filepath.Join(t.TempDir(), "state.json")
	f := baseWatchFlags(wt, sf)
	f.interval = 0

	cfg, err := resolveWatchConfig(f)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.interval != 45*time.Second {
		t.Errorf("env interval: got %v, want 45s", cfg.interval)
	}
}

func TestResolveWatchConfig_BadIntervalEnv(t *testing.T) {
	clearWatchEnv(t)
	t.Setenv("FAKE_TOKEN", "ghp_abc")
	t.Setenv("CANOPY_WATCH_INTERVAL", "not-a-duration")
	wt := gitWorktree(t)
	sf := filepath.Join(t.TempDir(), "state.json")
	f := baseWatchFlags(wt, sf)
	f.interval = 0

	_, err := resolveWatchConfig(f)
	if err == nil || !strings.Contains(err.Error(), "CANOPY_WATCH_INTERVAL") {
		t.Fatalf("want CANOPY_WATCH_INTERVAL parse error, got %v", err)
	}
}

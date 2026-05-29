package watch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/canopy/cmd/canopy/forge"
)

// watchConfig is the resolved configuration after env + flag layering.
type watchConfig struct {
	worktree   string
	forge      string
	repo       bigorna.Repo
	baseURL    string
	tokenEnv   string
	token      string
	baseBranch string
	remote     string

	dbPath    string // empty → sync-only mode (no re-ingest)
	stateFile string

	interval    time.Duration
	maxInterval time.Duration

	verbose bool
}

func resolveWatchConfig(f watchFlags) (watchConfig, error) {
	cfg := watchConfig{
		worktree:    firstNonEmpty(f.worktree, os.Getenv("CANOPY_REGISTRY_WORKTREE")),
		forge:       firstNonEmpty(f.forge, os.Getenv("CANOPY_FORGE_KIND"), "github"),
		baseURL:     firstNonEmpty(f.baseURL, os.Getenv("CANOPY_FORGE_BASE_URL")),
		baseBranch:  firstNonEmpty(f.baseBranch, os.Getenv("CANOPY_REGISTRY_BASE_BRANCH"), "main"),
		remote:      firstNonEmpty(f.remote, "origin"),
		dbPath:      firstNonEmpty(f.dbPath, os.Getenv("CANOPY_DB_PATH")),
		stateFile:   firstNonEmpty(f.stateFile, os.Getenv("CANOPY_WATCH_STATE_FILE")),
		interval:    f.interval,
		maxInterval: f.maxInterval,
		verbose:     f.verbose,
	}

	// Worktree presence (cheap string check first; filesystem stat
	// deferred until after cheaper validations land).
	if cfg.worktree == "" {
		return cfg, errors.New("canopy watch: --worktree is required (or set $CANOPY_REGISTRY_WORKTREE)")
	}

	// Forge + base-url validation (same shape as publish).
	switch cfg.forge {
	case "github":
		// OK; baseURL optional.
	case "gitlab":
		// OK; baseURL optional.
	case "bitbucketdc":
		if cfg.baseURL == "" {
			return cfg, errors.New(
				"canopy watch: --base-url is required when --forge=bitbucketdc " +
					"(no default; set $CANOPY_FORGE_BASE_URL or pass --base-url)")
		}
	case "forgejo":
		if cfg.baseURL == "" {
			return cfg, errors.New(
				"canopy watch: --base-url is required when --forge=forgejo " +
					"(no canonical instance; set $CANOPY_FORGE_BASE_URL or pass --base-url, e.g. https://codeberg.org)")
		}
	default:
		return cfg, fmt.Errorf(
			"canopy watch: --forge=%q not supported (valid: github | gitlab | bitbucketdc | forgejo)",
			cfg.forge)
	}

	// Repo.
	repoStr := firstNonEmpty(f.repo, os.Getenv("CANOPY_FORGE_REPO"))
	if repoStr == "" {
		return cfg, errors.New("canopy watch: --repo is required (or set $CANOPY_FORGE_REPO)")
	}
	parts := strings.SplitN(repoStr, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return cfg, fmt.Errorf("canopy watch: --repo must be <owner>/<name>, got %q", repoStr)
	}
	cfg.repo = bigorna.Repo{Owner: parts[0], Name: parts[1]}

	// Token.
	defaultTokenEnv := "CANOPY_GITHUB_TOKEN"
	switch cfg.forge {
	case "bitbucketdc":
		defaultTokenEnv = "CANOPY_BITBUCKET_TOKEN"
	case "gitlab":
		defaultTokenEnv = "CANOPY_GITLAB_TOKEN"
	case "forgejo":
		defaultTokenEnv = "CANOPY_FORGEJO_TOKEN"
	}
	cfg.tokenEnv = firstNonEmpty(f.tokenEnv, os.Getenv("CANOPY_FORGE_TOKEN_ENV"), defaultTokenEnv)
	cfg.token = os.Getenv(cfg.tokenEnv)
	if cfg.token == "" {
		return cfg, fmt.Errorf(
			"canopy watch: $%s not set (or empty)\n  Set it to a %s PAT with read access to %s/%s.",
			cfg.tokenEnv, forge.DisplayName(cfg.forge), cfg.repo.Owner, cfg.repo.Name)
	}

	// Worktree-is-git filesystem check (deferred until after cheaper
	// validations so a misconfigured --forge / --repo / --token-env
	// surfaces its specific error message rather than getting masked
	// by an os.Stat failure on the worktree).
	if st, err := os.Stat(filepath.Join(cfg.worktree, ".git")); err != nil || !st.IsDir() {
		return cfg, fmt.Errorf("canopy watch: --worktree %s is not a git working tree", cfg.worktree)
	}

	// State file default.
	if cfg.stateFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return cfg, fmt.Errorf("canopy watch: cannot determine home dir for default state file: %w", err)
		}
		cfg.stateFile = filepath.Join(home, ".canopy", "watch-state.json")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.stateFile), 0o755); err != nil {
		return cfg, fmt.Errorf("canopy watch: create state-file dir: %w", err)
	}

	// Interval defaults.
	if cfg.interval == 0 {
		if envInt := os.Getenv("CANOPY_WATCH_INTERVAL"); envInt != "" {
			d, err := time.ParseDuration(envInt)
			if err != nil {
				return cfg, fmt.Errorf("canopy watch: $CANOPY_WATCH_INTERVAL %q: %w", envInt, err)
			}
			cfg.interval = d
		} else {
			cfg.interval = 60 * time.Second
		}
	}
	if cfg.maxInterval == 0 {
		cfg.maxInterval = 5 * cfg.interval
	}

	return cfg, nil
}
